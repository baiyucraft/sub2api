package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type authSessionRepoFake struct {
	mu        sync.Mutex
	record    *UpstreamAuthSessionRecord
	gets      int
	saves     int
	getErr    error
	saveErr   error
	getErrAt  int
	saveErrAt int
}

func (r *authSessionRepoFake) Get(context.Context, int64) (*UpstreamAuthSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets++
	if r.getErr != nil && (r.getErrAt == 0 || r.gets == r.getErrAt) {
		return nil, r.getErr
	}
	if r.record == nil {
		return nil, nil
	}
	copy := *r.record
	return &copy, nil
}
func (r *authSessionRepoFake) Save(_ context.Context, record *UpstreamAuthSessionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves++
	if r.saveErr != nil && (r.saveErrAt == 0 || r.saves == r.saveErrAt) {
		return r.saveErr
	}
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
	logins               int
	restores             int
	seeds                int
	refreshes            int
	serializes           int
	refreshErr           error
	refreshInput         *UpstreamAuthHandle
	serializeErr         error
	serializeErrAt       int
	restoreExpired       bool
	restoreRefreshed     bool
	restoreAuthenticated bool
	seedExpired          bool
	seedRefreshed        bool
	disableLogin         bool
	legacyLifecycle      bool
}

func (s *authSessionStrategyFake) DurableAuthLifecycle() bool         { return !s.legacyLifecycle }
func (s *authSessionStrategyFake) Fingerprint(*UpstreamConfig) string { return "fp" }
func (s *authSessionStrategyFake) Seed(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error) {
	s.seeds++
	if s.seedRefreshed || s.seedExpired {
		handle := &UpstreamAuthHandle{Value: "seeded", Refreshed: s.seedRefreshed}
		if s.seedExpired {
			expired := time.Now().UTC().Add(-time.Hour)
			handle.ExpiresAt = &expired
		}
		return handle, nil
	}
	return nil, nil
}
func (s *authSessionStrategyFake) Restore(context.Context, *UpstreamConfig, string, *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error) {
	s.restores++
	h := &UpstreamAuthHandle{Value: "restored"}
	if s.restoreExpired {
		expired := time.Now().UTC().Add(-time.Hour)
		h.ExpiresAt = &expired
	}
	h.Refreshed = s.restoreRefreshed
	h.Authenticated = s.restoreAuthenticated
	return h, nil
}
func (s *authSessionStrategyFake) Login(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error) {
	s.logins++
	return &UpstreamAuthHandle{Value: "logged", Authenticated: true}, nil
}
func (s *authSessionStrategyFake) Refresh(_ context.Context, _ *UpstreamConfig, _ string, handle *UpstreamAuthHandle) (*UpstreamAuthHandle, error) {
	s.refreshes++
	s.refreshInput = handle
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return &UpstreamAuthHandle{Value: "refreshed", Refreshed: true}, nil
}
func (s *authSessionStrategyFake) Serialize(handle *UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error) {
	s.serializes++
	if s.serializeErr != nil && (s.serializeErrAt == 0 || s.serializes == s.serializeErrAt) {
		return nil, s.serializeErr
	}
	return &UpstreamAuthSessionSecret{Provider: "fake", Data: map[string]any{"ok": true, "value": handle.Value}}, nil
}
func (*authSessionStrategyFake) ClassifyAuthError(err error) UpstreamAuthErrorCategory {
	if err == nil {
		return UpstreamAuthErrorUnknown
	}
	switch {
	case errors.Is(err, errUpstreamAuthHandleExpired):
		return UpstreamAuthErrorExpired
	case strings.Contains(err.Error(), "409"):
		return UpstreamAuthErrorConflict
	case strings.Contains(err.Error(), "401"):
		return UpstreamAuthErrorUnauthorized
	case strings.Contains(err.Error(), "403"):
		return UpstreamAuthErrorPermanent
	case strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "timeout"):
		return UpstreamAuthErrorTransport
	default:
		return UpstreamAuthErrorUnknown
	}
}
func (s *authSessionStrategyFake) CanLogin(*UpstreamConfig) bool { return !s.disableLogin }

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

func TestUpstreamAuthSessionManagerExpiredRestoreRefreshesWithoutLogin(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 4, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	expired := time.Now().UTC().Add(-time.Hour)
	repo.record.ExpiresAt = &expired
	strategy.restoreExpired = true
	var operatedValues []any
	_, err = manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
		operatedValues = append(operatedValues, handle.Value)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, strategy.logins)
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 1, strategy.seeds)
	require.Equal(t, "restored", strategy.refreshInput.Value)
	require.True(t, expiredHandle(strategy.refreshInput, time.Now().UTC()))
	require.Equal(t, []any{"refreshed"}, operatedValues)
	require.Equal(t, int64(1), repo.record.RefreshCount)
	require.Zero(t, repo.record.ReuseCount)
}

func TestUpstreamAuthSessionManagerPersistsTokensRefreshedDuringRestore(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{}
	cfg := &UpstreamConfig{ID: 5, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	strategy.restoreRefreshed = true
	_, err = manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
	require.NoError(t, err)
	require.Equal(t, int64(1), repo.record.RefreshCount)
	require.NotEmpty(t, repo.record.LastRefreshedAt)
}

func TestUpstreamAuthSessionManagerPersistsTokensRefreshedDuringSeed(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{seedRefreshed: true}
	cfg := &UpstreamConfig{ID: 6, Provider: "fake", AuthMode: "manual_jwt", SiteURL: "https://example.test"}

	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })

	require.NoError(t, err)
	require.Equal(t, int64(1), repo.record.RefreshCount)
	require.NotEmpty(t, repo.record.LastRefreshedAt)
	require.Equal(t, int64(0), repo.record.LoginCount)
}

func TestUpstreamAuthSessionManagerRefreshesExpiredSeedOnce(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{seedExpired: true, disableLogin: true}
	cfg := &UpstreamConfig{ID: 7, Provider: "fake", AuthMode: "manual_jwt", SiteURL: "https://example.test"}
	var operatedValues []any

	_, err := manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
		operatedValues = append(operatedValues, handle.Value)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 0, strategy.logins)
	require.Equal(t, []any{"refreshed"}, operatedValues)
	require.Equal(t, int64(1), repo.record.RefreshCount)
}

func TestUpstreamAuthSessionManagerPreservesExpiredSeedRefreshFailure(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{
		seedExpired:  true,
		disableLogin: true,
		refreshErr:   errors.New("refresh returned status 401"),
	}
	cfg := &UpstreamConfig{ID: 8, Provider: "fake", AuthMode: "manual_jwt", SiteURL: "https://example.test"}

	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
		return nil
	})

	require.EqualError(t, err, "refresh returned status 401")
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 0, strategy.logins)
	require.Equal(t, string(UpstreamAuthErrorUnauthorized), repo.record.LastErrorCategory)
}

func TestUpstreamAuthSessionManagerDoesNotRecoverTwiceAfterSeedRefresh(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{seedRefreshed: true}
	cfg := &UpstreamConfig{ID: 9, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}

	_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
		return errors.New("401 unauthorized")
	})

	require.EqualError(t, err, "401 unauthorized")
	require.Equal(t, 0, strategy.refreshes)
	require.Equal(t, 0, strategy.logins)
}

func TestUpstreamAuthSessionManagerLoginFallbackAfterRefreshFailure(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{seedExpired: true, refreshErr: errors.New("refresh returned status 401")}
	cfg := &UpstreamConfig{ID: 10, Provider: "fake", AuthMode: "user_login", SiteURL: "https://example.test"}
	var operatedValues []any

	_, err := manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
		operatedValues = append(operatedValues, handle.Value)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 1, strategy.logins)
	require.Equal(t, []any{"logged"}, operatedValues)
}

func TestUpstreamAuthSessionManagerPersistsAuthenticationBeforeFailedOperation(t *testing.T) {
	for _, mode := range []string{"login", "refresh", "restore_refresh", "seed_refresh"} {
		for _, failure := range []struct {
			name     string
			cause    error
			category UpstreamAuthErrorCategory
		}{
			{name: "unauthorized", cause: errors.New("401 unauthorized"), category: UpstreamAuthErrorUnauthorized},
			{name: "conflict", cause: errors.New("409 session limit"), category: UpstreamAuthErrorConflict},
			{name: "transport", cause: errors.New("connection timeout"), category: UpstreamAuthErrorTransport},
			{name: "business", cause: errors.New("business request failed"), category: UpstreamAuthErrorUnknown},
		} {
			t.Run(mode+"/"+failure.name, func(t *testing.T) {
				ctx := context.Background()
				repo := &authSessionRepoFake{}
				manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
				strategy := &authSessionStrategyFake{}
				cfg := &UpstreamConfig{ID: 11, Provider: "fake", AuthMode: "user_login"}
				if mode == "refresh" || mode == "restore_refresh" {
					_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
					require.NoError(t, err)
					repo.record.ConsecutiveAuthFailures = 1
					strategy.restoreRefreshed = mode == "restore_refresh"
				}
				strategy.seedRefreshed = mode == "seed_refresh"
				before := repo.record
				var operations int
				_, err := manager.Run(ctx, cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
					operations++
					if mode == "refresh" && handle.Value == "restored" {
						return errors.New("401 unauthorized")
					}
					// This assertion must hold inside the operation, before it fails.
					secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
					require.NoError(t, decryptErr)
					require.Equal(t, handle.Value, secret.Data["value"])
					if mode == "login" {
						require.Equal(t, int64(1), repo.record.LoginCount)
						require.NotNil(t, repo.record.LastAuthenticatedAt)
					} else {
						require.Equal(t, int64(1), repo.record.RefreshCount)
						require.NotNil(t, repo.record.LastRefreshedAt)
					}
					if before == nil {
						require.Nil(t, repo.record.LastUsedAt)
					} else {
						require.Equal(t, before.LastUsedAt, repo.record.LastUsedAt)
						require.Equal(t, before.ConsecutiveAuthFailures, repo.record.ConsecutiveAuthFailures)
					}
					return failure.cause
				})
				require.ErrorIs(t, err, failure.cause)
				secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
				require.NoError(t, decryptErr)
				wantValue := "logged"
				if mode == "refresh" {
					wantValue = "refreshed"
				} else if mode == "restore_refresh" {
					wantValue = "restored"
				} else if mode == "seed_refresh" {
					wantValue = "seeded"
				}
				require.Equal(t, wantValue, secret.Data["value"])
				require.Equal(t, string(failure.category), repo.record.LastErrorCategory)
				require.Zero(t, repo.record.ReuseCount)
				if mode == "refresh" {
					require.Equal(t, 1, strategy.refreshes)
					require.Equal(t, 2, operations)
				} else {
					require.Zero(t, strategy.refreshes)
					require.Equal(t, 1, operations)
				}
			})
		}
	}
}

func TestUpstreamAuthSessionManagerFailedReuseAccumulatesFailures(t *testing.T) {
	for _, cause := range []error{errors.New("403 forbidden"), errors.New("401 unauthorized")} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := context.Background()
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
			strategy := &authSessionStrategyFake{restoreAuthenticated: true}
			cfg := &UpstreamConfig{ID: 12, Provider: "fake", AuthMode: "user_login"}
			_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
			require.NoError(t, err)
			lastUsed := repo.record.LastUsedAt
			for attempt := 1; attempt <= UpstreamAuthSessionFailureThreshold; attempt++ {
				_, err = manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return cause })
				require.ErrorIs(t, err, cause)
				require.Equal(t, attempt, repo.record.ConsecutiveAuthFailures)
				require.Zero(t, repo.record.ReuseCount)
				require.Equal(t, lastUsed, repo.record.LastUsedAt)
				require.Equal(t, int64(1), repo.record.LoginCount)
			}
			require.Equal(t, 2, UpstreamAuthSessionFailureThreshold)
			require.Equal(t, 30*time.Minute, UpstreamAuthSessionCooldown)
			require.Equal(t, int64(1), repo.record.CooldownCount)
			require.Equal(t, UpstreamAuthSessionCooldown, repo.record.CooldownUntil.Sub(*repo.record.LastErrorAt))
			_, err = manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
				t.Fatal("operation must not run during cooldown")
				return nil
			})
			require.ErrorIs(t, err, ErrUpstreamAuthCooldown)
			require.Equal(t, 2, strategy.restores)
			require.Equal(t, 1, strategy.logins)
		})
	}
}

func TestUpstreamAuthSessionManagerSuccessfulOperationSavesLatestSnapshot(t *testing.T) {
	for _, mode := range []string{"login", "reuse", "refresh", "restore_refresh", "seed_refresh"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
			strategy := &authSessionStrategyFake{restoreAuthenticated: true}
			cfg := &UpstreamConfig{ID: 13, Provider: "fake", AuthMode: "user_login"}
			if mode == "reuse" || mode == "refresh" || mode == "restore_refresh" {
				_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
				require.NoError(t, err)
				repo.record.ConsecutiveAuthFailures = 1
				repo.record.LastErrorCategory = string(UpstreamAuthErrorUnauthorized)
				lastError := time.Now().UTC().Add(-time.Hour)
				repo.record.LastErrorAt = &lastError
			}
			strategy.restoreRefreshed = mode == "restore_refresh"
			strategy.seedRefreshed = mode == "seed_refresh"
			strategy.serializes = 0
			expires := time.Now().UTC().Add(2 * time.Hour)
			_, err := manager.Run(ctx, cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
				if mode == "refresh" && handle.Value == "restored" {
					return errors.New("401 unauthorized")
				}
				handle.Value = map[string]any{"cookie": "rotated-by-operation"}
				handle.ExpiresAt = &expires
				return nil
			})
			require.NoError(t, err)
			secret, err := manager.decrypt(repo.record.SecretCiphertext)
			require.NoError(t, err)
			require.Equal(t, "rotated-by-operation", secret.Data["value"].(map[string]any)["cookie"])
			require.Equal(t, &expires, repo.record.ExpiresAt)
			require.NotNil(t, repo.record.LastUsedAt)
			require.Zero(t, repo.record.ConsecutiveAuthFailures)
			require.Empty(t, repo.record.LastErrorCategory)
			require.Nil(t, repo.record.LastErrorAt)
			wantLogins, wantRefreshes, wantReuses := int64(1), int64(0), int64(0)
			if mode == "refresh" || mode == "restore_refresh" || mode == "seed_refresh" {
				wantRefreshes = 1
			}
			if mode == "reuse" || mode == "restore_refresh" {
				wantReuses = 1
			}
			if mode == "seed_refresh" {
				wantLogins = 0
			}
			require.Equal(t, wantLogins, repo.record.LoginCount)
			require.Equal(t, wantRefreshes, repo.record.RefreshCount)
			require.Equal(t, wantReuses, repo.record.ReuseCount)
			require.Zero(t, repo.record.ReloginCount)
			if mode == "reuse" {
				require.Equal(t, 1, strategy.serializes)
			} else {
				require.Equal(t, 2, strategy.serializes)
			}
		})
	}
}

type authSessionRefreshPolicyFake struct {
	*authSessionStrategyFake
	allowLogin   bool
	refreshError error
}

func (s *authSessionRefreshPolicyFake) CanLoginAfterRefreshError(err error) bool {
	s.refreshError = err
	return s.allowLogin
}

func TestUpstreamAuthSessionManagerRefreshFallbackHonorsOptionalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cause      error
		allowLogin bool
		conflict   bool
	}{
		{name: "transport", cause: errors.New("connection timeout")},
		{name: "forbidden", cause: errors.New("403 forbidden")},
		{name: "unauthorized_without_revocation", cause: errors.New("401 unauthorized")},
		{name: "conflict", cause: errors.New("409 session limit"), allowLogin: true, conflict: true},
		{name: "unsupported", cause: errors.New("refresh is unsupported"), allowLogin: true},
		{name: "revoked", cause: errors.New("401 session revoked"), allowLogin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
			strategy := &authSessionRefreshPolicyFake{
				authSessionStrategyFake: &authSessionStrategyFake{seedExpired: true, refreshErr: tc.cause},
				allowLogin:              tc.allowLogin,
			}
			cfg := &UpstreamConfig{ID: 14, Provider: "fake", AuthMode: "user_login"}
			var operations int
			_, err := manager.Run(context.Background(), cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
				operations++
				return nil
			})
			require.Equal(t, 1, strategy.refreshes)
			if tc.allowLogin && !tc.conflict {
				require.NoError(t, err)
				require.Equal(t, 1, strategy.logins)
				require.Equal(t, 1, operations)
			} else {
				require.ErrorIs(t, err, tc.cause)
				require.Zero(t, strategy.logins)
				require.Zero(t, operations)
				require.Equal(t, string(strategy.ClassifyAuthError(tc.cause)), repo.record.LastErrorCategory)
			}
			if tc.conflict {
				require.Nil(t, strategy.refreshError)
				require.Equal(t, int64(1), repo.record.CooldownCount)
			} else {
				require.ErrorIs(t, strategy.refreshError, tc.cause)
			}
		})
	}
}

func TestUpstreamAuthSessionManagerLegacyRefreshTransportFailureAllowsLogin(t *testing.T) {
	repo := &authSessionRepoFake{}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	strategy := &authSessionStrategyFake{seedExpired: true, refreshErr: errors.New("connection timeout")}
	cfg := &UpstreamConfig{ID: 15, Provider: "fake", AuthMode: "user_login"}
	_, err := manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
		require.Equal(t, "logged", handle.Value)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, strategy.refreshes)
	require.Equal(t, 1, strategy.logins)
}

func TestUpstreamAuthSessionManagerDoesNotRecoverTwiceAfterExpiredRestoreOrSeedRefresh(t *testing.T) {
	for _, restore := range []bool{false, true} {
		name := "seed"
		if restore {
			name = "restore"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
			strategy := &authSessionStrategyFake{}
			cfg := &UpstreamConfig{ID: 16, Provider: "fake", AuthMode: "user_login"}
			if restore {
				_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
				require.NoError(t, err)
				strategy.restoreRefreshed, strategy.restoreExpired = true, true
			} else {
				strategy.seedRefreshed, strategy.seedExpired = true, true
			}
			loginsBefore := strategy.logins
			_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error {
				t.Fatal("expired handle must not reach the business operation")
				return nil
			})
			require.ErrorIs(t, err, errUpstreamAuthHandleExpired)
			require.Zero(t, strategy.refreshes)
			require.Equal(t, loginsBefore, strategy.logins)
			require.Equal(t, int64(1), repo.record.RefreshCount)
			require.NotEmpty(t, repo.record.SecretCiphertext)
		})
	}
}

func TestUpstreamAuthSessionManagerPropagatesSnapshotStorageErrors(t *testing.T) {
	for _, tc := range []struct {
		name           string
		getErrAt       int
		saveErrAt      int
		serializeErrAt int
		operated       bool
		preserved      bool
		operationFails bool
	}{
		{name: "authentication_read", getErrAt: 2},
		{name: "authentication_serialize", serializeErrAt: 1},
		{name: "authentication_save", saveErrAt: 1},
		{name: "authentication_counter_save", saveErrAt: 2, preserved: true},
		{name: "operation_read", getErrAt: 3, operated: true, preserved: true},
		{name: "operation_serialize", serializeErrAt: 2, operated: true, preserved: true},
		{name: "operation_save", saveErrAt: 3, operated: true, preserved: true},
		{name: "failure_read", getErrAt: 3, operated: true, preserved: true, operationFails: true},
		{name: "failure_save", saveErrAt: 3, operated: true, preserved: true, operationFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageErr := errors.New("session storage unavailable")
			repo := &authSessionRepoFake{getErrAt: tc.getErrAt, saveErrAt: tc.saveErrAt}
			if tc.getErrAt > 0 {
				repo.getErr = storageErr
			}
			if tc.saveErrAt > 0 {
				repo.saveErr = storageErr
			}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
			strategy := &authSessionStrategyFake{serializeErrAt: tc.serializeErrAt}
			if tc.serializeErrAt > 0 {
				strategy.serializeErr = storageErr
			}
			cfg := &UpstreamConfig{ID: 17, Provider: "fake", AuthMode: "user_login"}
			operated := false
			_, err := manager.Run(context.Background(), cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
				operated = true
				handle.Value = "rotated-by-operation"
				if tc.operationFails {
					return errors.New("403 forbidden")
				}
				return nil
			})
			require.ErrorIs(t, err, storageErr)
			require.Equal(t, tc.operated, operated)
			if tc.preserved {
				require.NotNil(t, repo.record)
				secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
				require.NoError(t, decryptErr)
				require.Equal(t, "logged", secret.Data["value"])
				if tc.name == "authentication_counter_save" {
					require.Zero(t, repo.record.LoginCount)
				} else {
					require.Equal(t, int64(1), repo.record.LoginCount)
				}
				require.Nil(t, repo.record.LastUsedAt)
			} else {
				require.Nil(t, repo.record)
			}
		})
	}
}

// Embedding the required interface hides optional capabilities, as old providers do.
type authSessionNoLifecycleFake struct{ UpstreamAuthStrategy }

func TestUpstreamAuthSessionManagerLegacyExpiredRestoreUsesSeedOrLogin(t *testing.T) {
	for _, capabilityMissing := range []bool{false, true} {
		for _, seedRefresh := range []bool{false, true} {
			name := "explicit_false/login"
			if capabilityMissing {
				name = "capability_missing/login"
			}
			if seedRefresh {
				name = strings.TrimSuffix(name, "login") + "seed"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repo := &authSessionRepoFake{}
				manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
				fake := &authSessionStrategyFake{legacyLifecycle: true}
				var strategy UpstreamAuthStrategy = fake
				if capabilityMissing {
					strategy = authSessionNoLifecycleFake{UpstreamAuthStrategy: fake}
				}
				require.False(t, durableAuthLifecycle(strategy))
				cfg := &UpstreamConfig{ID: 18, Provider: "fake", AuthMode: "user_login"}
				_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
				require.NoError(t, err)
				fake.restoreExpired, fake.seedRefreshed = true, seedRefresh
				var values []any
				_, err = manager.Run(ctx, cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
					values = append(values, handle.Value)
					secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
					require.NoError(t, decryptErr)
					require.Equal(t, handle.Value, secret.Data["value"])
					require.Equal(t, int64(1), repo.record.LoginCount)
					require.Zero(t, repo.record.RefreshCount)
					return nil
				})
				require.NoError(t, err)
				require.Equal(t, 1, fake.restores)
				require.Equal(t, 2, fake.seeds)
				require.Zero(t, fake.refreshes)
				require.Zero(t, repo.record.ReuseCount)
				require.Zero(t, repo.record.ReloginCount)
				if seedRefresh {
					require.Equal(t, []any{"seeded"}, values)
					require.Equal(t, 1, fake.logins)
					require.Equal(t, int64(1), repo.record.LoginCount)
					require.Equal(t, int64(1), repo.record.RefreshCount)
				} else {
					require.Equal(t, []any{"logged"}, values)
					require.Equal(t, 2, fake.logins)
					require.Equal(t, int64(2), repo.record.LoginCount)
					require.Zero(t, repo.record.RefreshCount)
				}
			})
		}
	}
}

func TestUpstreamAuthSessionManagerLegacyReuseCountsAndResetsBeforeOperation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "success"},
		{name: "transport", cause: errors.New("connection timeout")},
		{name: "forbidden", cause: errors.New("403 forbidden")},
		{name: "unauthorized", cause: errors.New("401 unauthorized")},
		{name: "conflict", cause: errors.New("409 session limit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
			strategy := &authSessionStrategyFake{legacyLifecycle: true, restoreAuthenticated: true}
			cfg := &UpstreamConfig{ID: 19, Provider: "fake", AuthMode: "user_login"}
			_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
			require.NoError(t, err)
			repo.record.ConsecutiveAuthFailures = 1
			repo.record.LastErrorCategory = string(UpstreamAuthErrorUnauthorized)
			lastError := time.Now().UTC().Add(-time.Hour)
			repo.record.LastErrorAt = &lastError
			attempts := 1
			if tc.name == "unauthorized" {
				attempts = 2
			}
			for attempt := 1; attempt <= attempts; attempt++ {
				var operations int
				_, err = manager.Run(ctx, cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
					operations++
					// Reuse retains its old attempt counter, even when business fails.
					require.Equal(t, int64(attempt), repo.record.ReuseCount)
					require.Zero(t, repo.record.ConsecutiveAuthFailures)
					require.Empty(t, repo.record.LastErrorCategory)
					require.NotNil(t, repo.record.LastUsedAt)
					require.NotNil(t, repo.record.LastErrorAt)
					require.Equal(t, int64(1), repo.record.LoginCount)
					require.Zero(t, repo.record.RefreshCount)
					return tc.cause
				})
				if tc.cause == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, tc.cause)
				}
				require.Equal(t, int64(attempt), repo.record.ReuseCount)
				require.Equal(t, 1, strategy.logins)
				require.Zero(t, repo.record.RefreshCount)
				if tc.name == "unauthorized" {
					require.Equal(t, 2, operations)
					require.Equal(t, attempt, strategy.refreshes)
					require.Equal(t, 1, repo.record.ConsecutiveAuthFailures)
					require.Zero(t, repo.record.CooldownCount)
					secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
					require.NoError(t, decryptErr)
					require.Equal(t, "refreshed", secret.Data["value"])
				} else {
					require.Equal(t, 1, operations)
					require.Zero(t, strategy.refreshes)
					if tc.name == "conflict" {
						require.Equal(t, 1, repo.record.ConsecutiveAuthFailures)
						require.Equal(t, int64(1), repo.record.CooldownCount)
					} else {
						require.Zero(t, repo.record.ConsecutiveAuthFailures)
						require.Empty(t, repo.record.LastErrorCategory)
						require.Equal(t, &lastError, repo.record.LastErrorAt)
					}
				}
			}
		})
	}
}

func TestUpstreamAuthSessionManagerLegacyAuthenticationCountsOnlyAfterBusinessSuccess(t *testing.T) {
	for _, mode := range []string{"login", "refresh", "restore_refresh", "seed_refresh", "relogin"} {
		for _, result := range []string{"success", "unauthorized", "transport", "conflict"} {
			t.Run(mode+"/"+result, func(t *testing.T) {
				ctx := context.Background()
				repo := &authSessionRepoFake{}
				manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
				strategy := &authSessionStrategyFake{legacyLifecycle: true}
				cfg := &UpstreamConfig{ID: 20, Provider: "fake", AuthMode: "user_login"}
				initialLogins := int64(0)
				if mode == "refresh" || mode == "restore_refresh" || mode == "relogin" {
					_, err := manager.Run(ctx, cfg, "", strategy, func(context.Context, *UpstreamAuthHandle) error { return nil })
					require.NoError(t, err)
					initialLogins = 1
				}
				strategy.restoreRefreshed = mode == "restore_refresh"
				strategy.seedRefreshed = mode == "seed_refresh"
				if mode == "relogin" {
					strategy.refreshErr = errors.New("connection timeout")
				}
				var cause error
				if result == "unauthorized" {
					cause = errors.New("401 unauthorized")
				} else if result == "transport" {
					cause = errors.New("connection timeout")
				} else if result == "conflict" {
					cause = errors.New("409 session limit")
				}
				_, err := manager.Run(ctx, cfg, "", strategy, func(_ context.Context, handle *UpstreamAuthHandle) error {
					if (mode == "refresh" || mode == "relogin") && handle.Value == "restored" {
						return errors.New("401 unauthorized")
					}
					secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
					require.NoError(t, decryptErr)
					require.Equal(t, handle.Value, secret.Data["value"])
					require.Equal(t, initialLogins, repo.record.LoginCount)
					require.Zero(t, repo.record.RefreshCount)
					require.Zero(t, repo.record.ReloginCount)
					require.Nil(t, repo.record.LastRefreshedAt)
					if initialLogins == 0 {
						require.Nil(t, repo.record.LastAuthenticatedAt)
					}
					if cause == nil {
						handle.Value = "post-operation"
					}
					return cause
				})
				wantLogins, wantRefreshes, wantRelogins := initialLogins, int64(0), int64(0)
				if cause == nil {
					require.NoError(t, err)
					if mode == "login" || mode == "relogin" {
						wantLogins++
					} else {
						wantRefreshes++
					}
					if mode == "relogin" {
						wantRelogins = 1
					}
				} else {
					require.ErrorIs(t, err, cause)
				}
				require.Equal(t, wantLogins, repo.record.LoginCount)
				require.Equal(t, wantRefreshes, repo.record.RefreshCount)
				require.Equal(t, wantRelogins, repo.record.ReloginCount)
				if result == "conflict" {
					require.Equal(t, int64(1), repo.record.CooldownCount)
					require.Equal(t, UpstreamAuthSessionCooldown, repo.record.CooldownUntil.Sub(*repo.record.LastErrorAt))
				}
				secret, decryptErr := manager.decrypt(repo.record.SecretCiphertext)
				require.NoError(t, decryptErr)
				wantValue := "logged"
				if cause == nil {
					wantValue = "post-operation"
				} else if mode == "refresh" {
					wantValue = "refreshed"
				} else if mode == "restore_refresh" {
					wantValue = "restored"
				} else if mode == "seed_refresh" {
					wantValue = "seeded"
				}
				require.Equal(t, wantValue, secret.Data["value"])
				wantLoginAttempts, wantRefreshAttempts := int(initialLogins), 0
				if mode == "login" || mode == "relogin" {
					wantLoginAttempts++
				}
				if mode == "refresh" || mode == "relogin" {
					wantRefreshAttempts = 1
				}
				require.Equal(t, wantLoginAttempts, strategy.logins)
				require.Equal(t, wantRefreshAttempts, strategy.refreshes)
			})
		}
	}
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

func TestNewAPIAuthSessionDoesNotMutateSharedHTTPClient(t *testing.T) {
	proxyURL := "http://127.0.0.1:49123"
	shared, err := sub2APIHTTPClient(proxyURL)
	require.NoError(t, err)
	originalTransport := shared.Transport
	originalJar := shared.Jar

	cfg := &UpstreamConfig{SiteURL: "https://newapi.example.test"}
	session, err := newAPIAuthSession(context.Background(), cfg, proxyURL, 42, "session=opaque-cookie", "opaque-access-token")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotSame(t, shared, session.client)
	require.Same(t, originalTransport, shared.Transport)
	require.Equal(t, originalJar, shared.Jar)
	require.NotNil(t, session.client.Jar)
	transport, ok := session.client.Transport.(newAPIAuthTransport)
	require.True(t, ok)
	require.Empty(t, transport.cookie)
	cookieRequest, err := http.NewRequest(http.MethodGet, cfg.SiteURL, nil)
	require.NoError(t, err)
	cookies := session.client.Jar.Cookies(cookieRequest.URL)
	require.Len(t, cookies, 1)
	require.Equal(t, "session", cookies[0].Name)
	require.Equal(t, "opaque-cookie", cookies[0].Value)
	require.Equal(t, "Bearer opaque-access-token", transport.accessToken)
}

func TestNewAPIAuthSessionDoesNotOverrideSharedClientRequestHeaders(t *testing.T) {
	observed := make(chan http.Header, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)

	shared, err := sub2APIHTTPClient(proxy.URL)
	require.NoError(t, err)
	_, err = newAPIAuthSession(
		context.Background(),
		&UpstreamConfig{SiteURL: "https://newapi.example.test"},
		proxy.URL,
		42,
		"session=newapi-cookie",
		"newapi-access-token",
	)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, "http://sub2api.example.test/api/v1/keys", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer sub2api-access-token")
	resp, err := shared.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	headers := <-observed
	require.Equal(t, "Bearer sub2api-access-token", headers.Get("Authorization"))
	require.Empty(t, headers.Get("Cookie"))
}
