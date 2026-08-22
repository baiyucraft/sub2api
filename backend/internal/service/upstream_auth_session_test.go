package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type authSessionRepoFake struct {
	mu     sync.Mutex
	record *UpstreamAuthSessionRecord
}

func (r *authSessionRepoFake) Get(context.Context, int64) (*UpstreamAuthSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return nil, nil
	}
	copy := *r.record
	return &copy, nil
}
func (r *authSessionRepoFake) Save(_ context.Context, record *UpstreamAuthSessionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *record
	r.record = &copy
	return nil
}
func (r *authSessionRepoFake) Delete(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record = nil
	return nil
}
func (r *authSessionRepoFake) ClearCooldown(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record != nil {
		r.record.CooldownUntil = nil
		r.record.ConsecutiveAuthFailures = 0
	}
	return nil
}

type authSessionEncryptorFake struct{}

func (authSessionEncryptorFake) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (authSessionEncryptorFake) Decrypt(value string) (string, error) {
	if len(value) < 4 || value[:4] != "enc:" {
		return "", errors.New("bad ciphertext")
	}
	return value[4:], nil
}

type authSessionStrategyFake struct {
	logins         int
	restores       int
	refreshes      int
	refreshErr     error
	restoreExpired bool
}

func (s *authSessionStrategyFake) Fingerprint(*UpstreamConfig) string { return "fp" }
func (s *authSessionStrategyFake) Seed(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error) {
	return nil, nil
}
func (s *authSessionStrategyFake) Restore(context.Context, *UpstreamConfig, string, *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error) {
	s.restores++
	h := &UpstreamAuthHandle{Value: "restored"}
	if s.restoreExpired {
		expired := time.Now().UTC().Add(-time.Hour)
		h.ExpiresAt = &expired
	}
	return h, nil
}
func (s *authSessionStrategyFake) Login(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error) {
	s.logins++
	return &UpstreamAuthHandle{Value: "logged"}, nil
}
func (s *authSessionStrategyFake) Refresh(context.Context, *UpstreamConfig, string, *UpstreamAuthHandle) (*UpstreamAuthHandle, error) {
	s.refreshes++
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return &UpstreamAuthHandle{Value: "refreshed"}, nil
}
func (*authSessionStrategyFake) Serialize(*UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error) {
	return &UpstreamAuthSessionSecret{Provider: "fake", Data: map[string]any{"ok": true}}, nil
}
func (*authSessionStrategyFake) ClassifyAuthError(err error) UpstreamAuthErrorCategory {
	if strings.Contains(err.Error(), "409") {
		return UpstreamAuthErrorConflict
	}
	return UpstreamAuthErrorUnauthorized
}
func (*authSessionStrategyFake) CanLogin(*UpstreamConfig) bool { return true }

func TestUpstreamAuthSessionManagerReusesPersistedSession(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 1, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	var operations int
	operation := func(context.Context, *UpstreamAuthHandle) error { operations++; return nil }
	_, err := manager.Run(context.Background(), cfg, "", strategy, operation)
	require.NoError(t, err)
	_, err = manager.Run(context.Background(), cfg, "", strategy, operation)
	require.NoError(t, err)
	require.Equal(t, 1, strategy.logins)
	require.Equal(t, 1, strategy.restores)
	require.Equal(t, 2, operations)
}

func TestUpstreamAuthSessionManagerConflictEntersCooldown(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 2, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
		return errors.New("upstream returned status 409")
	})
	require.Error(t, err)
	require.Equal(t, int64(1), repo.record.CooldownCount)
	require.NotNil(t, repo.record.CooldownUntil)
	_, err = manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.ErrorIs(t, err, ErrUpstreamAuthCooldown)
	require.Equal(t, 1, strategy.logins)
}

func TestUpstreamAuthSessionManagerDoesNotLoginAfterSuccessfulRefreshThen401(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 3, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	// First run creates a persisted session.
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	// A restored handle gets a 401, refresh succeeds, and the retried operation
	// gets another 401. The coordinator must stop there instead of logging in.
	_, err = manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
		if handle.Value == "refreshed" {
			return errors.New("401 unauthorized")
		}
		return errors.New("401 unauthorized")
	})
	require.Error(t, err)
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 1, strategy.logins)
	require.Equal(t, int64(0), repo.record.CooldownCount)
}

func TestUpstreamAuthSessionManagerExpiredRestoreFallsBackToLogin(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 4, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	expired := time.Now().UTC().Add(-time.Hour)
	repo.record.ExpiresAt = &expired
	strategy.restoreExpired = true
	_, err = manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	require.Equal(t, 2, strategy.logins)
}

func TestNewAPIHandleSerializesCookieTransport(t *testing.T) {
	session := &newAPISession{
		rootURL: "https://newapi.example.test",
		userID:  42,
		client: &http.Client{Transport: newAPIAuthTransport{
			base:   http.DefaultTransport,
			cookie: "session=opaque-cookie",
		}},
	}
	handle := newAPIHandle(session)
	value, ok := handle.Value.(newAPIAuthValue)
	require.True(t, ok)
	require.Equal(t, "session=opaque-cookie", value.Cookie)
}
