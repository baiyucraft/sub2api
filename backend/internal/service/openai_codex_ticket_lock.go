package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Eight harvest attempts can each perform a dynamic-proxy probe followed by a
// fixed-proxy verification. Ten minutes keeps the lock alive across that full
// retry envelope while still providing bounded crash recovery.
const openAICodexTicketHarvestLockTTL = 10 * time.Minute

type openAICodexTicketHarvestLockResult string

const (
	openAICodexTicketHarvestLockAcquired    openAICodexTicketHarvestLockResult = "acquired"
	openAICodexTicketHarvestLockHeld        openAICodexTicketHarvestLockResult = "held"
	openAICodexTicketHarvestLockUnavailable openAICodexTicketHarvestLockResult = "unavailable"
)

// SetCodexTicketHarvestLockBackends injects the fork-owned coordination
// backends without changing the upstream OpenAI gateway constructor.
func (s *OpenAIGatewayService) SetCodexTicketHarvestLockBackends(cache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = cache
	s.db = db
}

func openAICodexTicketHarvestLockKey(accountID int64, model, modelRevision, fixedProxyFingerprint string) string {
	encode := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(value)))
	}
	return fmt.Sprintf(
		"openai:codex-ticket:harvest:%d:%s:%s:%s",
		accountID,
		encode(strings.ToLower(model)),
		encode(modelRevision),
		encode(fixedProxyFingerprint),
	)
}

// acquireOpenAICodexTicketHarvestLock coordinates one account/model harvest
// across instances. Redis contention is authoritative and skips the cycle;
// Redis errors fall back to PostgreSQL. When coordination was configured but
// every configured backend fails, harvesting is denied to avoid a stampede.
// A deployment with no coordination backend is treated as an explicit
// single-instance setup and may run locally.
func (s *OpenAIGatewayService) acquireOpenAICodexTicketHarvestLock(
	ctx context.Context,
	accountID int64,
	model string,
	modelRevision string,
	fixedProxyFingerprint string,
) (func(), openAICodexTicketHarvestLockResult) {
	if ctx == nil {
		ctx = context.Background()
	}

	var cache LeaderLockCache
	var db *sql.DB
	if s != nil {
		cache = s.lockCache
		db = s.db
	}

	key := openAICodexTicketHarvestLockKey(accountID, model, modelRevision, fixedProxyFingerprint)
	owner := uuid.NewString()

	if cache != nil {
		acquired, err := cache.TryAcquireLeaderLock(ctx, key, owner, openAICodexTicketHarvestLockTTL)
		if err == nil {
			if !acquired {
				return nil, openAICodexTicketHarvestLockHeld
			}
			release := func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = cache.ReleaseLeaderLock(releaseCtx, key, owner)
			}
			return release, openAICodexTicketHarvestLockAcquired
		}
	}

	if db != nil {
		release, acquired, err := tryAcquireDBAdvisoryLockWithError(ctx, db, hashAdvisoryLockID(key))
		if err != nil {
			return nil, openAICodexTicketHarvestLockUnavailable
		}
		if !acquired {
			return nil, openAICodexTicketHarvestLockHeld
		}
		return release, openAICodexTicketHarvestLockAcquired
	}

	if cache != nil {
		return nil, openAICodexTicketHarvestLockUnavailable
	}

	return func() {}, openAICodexTicketHarvestLockAcquired
}

// acquireCodexTicketHarvestLock is the harvester-facing compatibility shape.
// Both peer contention and unavailable configured coordination backends mean
// the current cycle must be skipped; tests use the detailed helper above to
// verify those causes remain distinct.
func (s *OpenAIGatewayService) acquireCodexTicketHarvestLock(
	ctx context.Context,
	accountID int64,
	model string,
	modelRevision string,
	fixedProxyFingerprint string,
) (func(), bool) {
	release, result := s.acquireOpenAICodexTicketHarvestLock(
		ctx,
		accountID,
		model,
		modelRevision,
		fixedProxyFingerprint,
	)
	return release, result == openAICodexTicketHarvestLockAcquired
}
