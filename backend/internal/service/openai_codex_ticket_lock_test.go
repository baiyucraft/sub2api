package service

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type codexTicketLockCacheStub struct {
	acquired   bool
	acquireErr error
	key        string
	owner      string
	ttl        time.Duration
	released   bool
}

func (s *codexTicketLockCacheStub) TryAcquireLeaderLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	s.key = key
	s.owner = owner
	s.ttl = ttl
	return s.acquired, s.acquireErr
}

func (s *codexTicketLockCacheStub) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	if key == s.key && owner == s.owner {
		s.released = true
	}
	return nil
}

func TestOpenAICodexTicketHarvestLockKeyIsolation(t *testing.T) {
	base := openAICodexTicketHarvestLockKey(42, "gpt-6-astra", "revision-a", "proxy-a")
	keys := []string{
		base,
		openAICodexTicketHarvestLockKey(43, "gpt-6-astra", "revision-a", "proxy-a"),
		openAICodexTicketHarvestLockKey(42, "gpt-5.6-sol", "revision-a", "proxy-a"),
		openAICodexTicketHarvestLockKey(42, "gpt-6-astra", "revision-b", "proxy-a"),
		openAICodexTicketHarvestLockKey(42, "gpt-6-astra", "revision-a", "proxy-b"),
	}

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		require.NotEmpty(t, key)
		_, exists := seen[key]
		require.False(t, exists, "every account/model/revision/proxy tuple needs an isolated lock key")
		seen[key] = struct{}{}
	}
	require.Equal(
		t,
		base,
		openAICodexTicketHarvestLockKey(42, " GPT-6-ASTRA ", " revision-a ", " proxy-a "),
		"model casing and surrounding whitespace must not split the same logical job",
	)
}

func TestAcquireOpenAICodexTicketHarvestLockRedisSuccess(t *testing.T) {
	cache := &codexTicketLockCacheStub{acquired: true}
	svc := &OpenAIGatewayService{lockCache: cache}

	release, result := svc.acquireOpenAICodexTicketHarvestLock(
		context.Background(),
		42,
		"gpt-6-astra",
		"revision-a",
		"proxy-a",
	)

	require.Equal(t, openAICodexTicketHarvestLockAcquired, result)
	require.NotNil(t, release)
	require.Equal(t, openAICodexTicketHarvestLockTTL, cache.ttl)
	require.GreaterOrEqual(t, cache.ttl, 10*time.Minute)
	require.NotEmpty(t, cache.owner)
	release()
	require.True(t, cache.released)
}

func TestAcquireOpenAICodexTicketHarvestLockRedisHeldSkipsDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := &OpenAIGatewayService{
		lockCache: &codexTicketLockCacheStub{acquired: false},
		db:        db,
	}
	release, result := svc.acquireOpenAICodexTicketHarvestLock(
		context.Background(),
		42,
		"gpt-6-astra",
		"revision-a",
		"proxy-a",
	)

	require.Nil(t, release)
	require.Equal(t, openAICodexTicketHarvestLockHeld, result)
	require.NoError(t, mock.ExpectationsWereMet(), "Redis contention must not fall through to PostgreSQL")
}

func TestAcquireOpenAICodexTicketHarvestLockRedisErrorFallsBackToDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	key := openAICodexTicketHarvestLockKey(42, "gpt-6-astra", "revision-a", "proxy-a")
	lockID := hashAdvisoryLockID(key)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(lockID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	svc := &OpenAIGatewayService{
		lockCache: &codexTicketLockCacheStub{acquireErr: errors.New("redis unavailable")},
		db:        db,
	}
	release, result := svc.acquireOpenAICodexTicketHarvestLock(
		context.Background(),
		42,
		"gpt-6-astra",
		"revision-a",
		"proxy-a",
	)

	require.Equal(t, openAICodexTicketHarvestLockAcquired, result)
	require.NotNil(t, release)
	release()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcquireOpenAICodexTicketHarvestLockConfiguredBackendsFailClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WillReturnError(errors.New("postgres unavailable"))

	svc := &OpenAIGatewayService{
		lockCache: &codexTicketLockCacheStub{acquireErr: errors.New("redis unavailable")},
		db:        db,
	}
	release, result := svc.acquireOpenAICodexTicketHarvestLock(
		context.Background(),
		42,
		"gpt-6-astra",
		"revision-a",
		"proxy-a",
	)

	require.Nil(t, release)
	require.Equal(t, openAICodexTicketHarvestLockUnavailable, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcquireOpenAICodexTicketHarvestLockNoBackendRunsLocally(t *testing.T) {
	svc := &OpenAIGatewayService{}

	release, result := svc.acquireOpenAICodexTicketHarvestLock(
		context.Background(),
		42,
		"gpt-6-astra",
		"revision-a",
		"proxy-a",
	)

	require.Equal(t, openAICodexTicketHarvestLockAcquired, result)
	require.NotNil(t, release)
	require.NotPanics(t, release)
}
