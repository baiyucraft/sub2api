package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	UpstreamAuthSessionCooldown         = 30 * time.Minute
	UpstreamAuthSessionFailureThreshold = 2
	upstreamAuthSessionExpirySkew       = time.Minute
)

type UpstreamAuthErrorCategory string

const (
	UpstreamAuthErrorUnauthorized UpstreamAuthErrorCategory = "unauthorized"
	UpstreamAuthErrorConflict     UpstreamAuthErrorCategory = "conflict"
	UpstreamAuthErrorExpired      UpstreamAuthErrorCategory = "expired"
	UpstreamAuthErrorPermanent    UpstreamAuthErrorCategory = "permanent"
	UpstreamAuthErrorTransport    UpstreamAuthErrorCategory = "transport"
	UpstreamAuthErrorDecrypt      UpstreamAuthErrorCategory = "decrypt"
	UpstreamAuthErrorCooldown     UpstreamAuthErrorCategory = "cooldown"
	UpstreamAuthErrorUnknown      UpstreamAuthErrorCategory = "unknown"
)

var ErrUpstreamAuthCooldown = errors.New("upstream authentication is in cooldown")

var (
	errUpstreamAuthHandleExpired = errors.New("upstream authentication handle expired")
	errUpstreamAuthHandleMissing = errors.New("upstream authentication did not produce a usable session")
)

// UpstreamAuthSessionRecord is the non-secret persistence projection used by
// the service layer. SecretCiphertext is never returned by admin handlers.
type UpstreamAuthSessionRecord struct {
	ID                      int64
	UpstreamConfigID        int64
	Provider                string
	AuthMode                string
	CredentialFingerprint   string
	SecretCiphertext        string
	ExpiresAt               *time.Time
	LastAuthenticatedAt     *time.Time
	LastRefreshedAt         *time.Time
	LastUsedAt              *time.Time
	CooldownUntil           *time.Time
	ConsecutiveAuthFailures int
	LastErrorCategory       string
	LastErrorAt             *time.Time
	LoginCount              int64
	ReuseCount              int64
	RefreshCount            int64
	ReloginCount            int64
	CooldownCount           int64
}

type UpstreamAuthSessionRepository interface {
	Get(ctx context.Context, upstreamConfigID int64) (*UpstreamAuthSessionRecord, error)
	Save(ctx context.Context, record *UpstreamAuthSessionRecord) error
	Delete(ctx context.Context, upstreamConfigID int64) error
	ClearCooldown(ctx context.Context, upstreamConfigID int64) error
}

type UpstreamAuthHandle struct {
	// Value is provider-owned and must never be serialized or logged by the
	// coordinator. Strategies convert it to/from Secret before persistence.
	Value         any
	ExpiresAt     *time.Time
	Refreshed     bool
	Authenticated bool
}

type UpstreamAuthSessionSecret struct {
	Provider string         `json:"provider"`
	Data     map[string]any `json:"data"`
}

type UpstreamAuthStrategy interface {
	Fingerprint(*UpstreamConfig) string
	Seed(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error)
	Restore(context.Context, *UpstreamConfig, string, *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error)
	Login(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error)
	Refresh(context.Context, *UpstreamConfig, string, *UpstreamAuthHandle) (*UpstreamAuthHandle, error)
	Serialize(*UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error)
	ClassifyAuthError(error) UpstreamAuthErrorCategory
	CanLogin(*UpstreamConfig) bool
}

// Providers opt in to authentication-time counters and refreshable expired restores.
func durableAuthLifecycle(strategy UpstreamAuthStrategy) bool {
	capability, ok := strategy.(interface{ DurableAuthLifecycle() bool })
	return ok && capability.DurableAuthLifecycle()
}

type UpstreamAuthOperation func(context.Context, *UpstreamAuthHandle) error

type upstreamAuthSessionLock interface {
	WithUpstreamConfigSyncLock(context.Context, int64, func(context.Context) error) error
}

type upstreamConfigSyncLockHeldKey struct{}

func withUpstreamConfigSyncLockHeld(ctx context.Context) context.Context {
	return context.WithValue(ctx, upstreamConfigSyncLockHeldKey{}, true)
}

// UpstreamAuthSessionManager coordinates persistence, locking, retries and
// cooldown. Provider adapters only implement protocol-specific strategies.
type UpstreamAuthSessionManager interface {
	Run(context.Context, *UpstreamConfig, string, UpstreamAuthStrategy, UpstreamAuthOperation) (*UpstreamAuthHandle, error)
	Clear(context.Context, int64) error
	ClearCooldown(context.Context, int64) error
	ForceReauth(context.Context, int64) error
	Status(context.Context, int64, *UpstreamConfig) (*UpstreamAuthSessionStatus, error)
}

type UpstreamAuthSessionStatus struct {
	Provider            string     `json:"provider"`
	AuthMode            string     `json:"auth_mode"`
	Reusable            bool       `json:"reusable"`
	LastAuthenticatedAt *time.Time `json:"last_authenticated_at,omitempty"`
	LastRefreshedAt     *time.Time `json:"last_refreshed_at,omitempty"`
	LastUsedAt          *time.Time `json:"last_used_at,omitempty"`
	CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
	LastErrorCategory   string     `json:"last_error_category,omitempty"`
	LoginCount          int64      `json:"login_count"`
	ReuseCount          int64      `json:"reuse_count"`
	RefreshCount        int64      `json:"refresh_count"`
	ReloginCount        int64      `json:"relogin_count"`
	CooldownCount       int64      `json:"cooldown_count"`
}

type upstreamAuthSessionManager struct {
	repo      UpstreamAuthSessionRepository
	locker    upstreamAuthSessionLock
	encryptor SecretEncryptor
	group     singleflight.Group
	localMu   sync.Mutex
}

func NewUpstreamAuthSessionManager(repo UpstreamAuthSessionRepository, locker upstreamAuthSessionLock, encryptor SecretEncryptor) UpstreamAuthSessionManager {
	return &upstreamAuthSessionManager{repo: repo, locker: locker, encryptor: encryptor}
}

func (m *upstreamAuthSessionManager) Run(ctx context.Context, cfg *UpstreamConfig, proxyURL string, strategy UpstreamAuthStrategy, operation UpstreamAuthOperation) (*UpstreamAuthHandle, error) {
	if cfg == nil || cfg.ID <= 0 || strategy == nil || operation == nil {
		return nil, errors.New("invalid upstream auth session request")
	}
	key := fmt.Sprintf("%d:%s", cfg.ID, strategy.Fingerprint(cfg))
	value, err, _ := m.group.Do(key, func() (any, error) {
		var handle *UpstreamAuthHandle
		run := func(lockCtx context.Context) error {
			var runErr error
			handle, runErr = m.runLocked(lockCtx, cfg, proxyURL, strategy, operation)
			return runErr
		}
		if m.locker != nil && ctx.Value(upstreamConfigSyncLockHeldKey{}) != true {
			if err := m.locker.WithUpstreamConfigSyncLock(ctx, cfg.ID, run); err != nil {
				return nil, err
			}
		} else if err := run(ctx); err != nil {
			return nil, err
		}
		return handle, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*UpstreamAuthHandle), nil
}

func (m *upstreamAuthSessionManager) runLocked(ctx context.Context, cfg *UpstreamConfig, proxyURL string, strategy UpstreamAuthStrategy, operation UpstreamAuthOperation) (*UpstreamAuthHandle, error) {
	durable := durableAuthLifecycle(strategy)
	fingerprint := strategy.Fingerprint(cfg)
	record, err := m.repo.Get(ctx, cfg.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if record != nil && record.CredentialFingerprint != fingerprint {
		if err := m.repo.Delete(ctx, cfg.ID); err != nil {
			return nil, err
		}
		record = nil
		slog.Info("auth_session_invalidated", "upstream_config_id", cfg.ID, "reason", "credential_changed")
	}
	if record != nil && record.CooldownUntil != nil && record.CooldownUntil.After(now) {
		return nil, ErrUpstreamAuthCooldown
	}

	var handle *UpstreamAuthHandle
	restored := false
	if record != nil && strings.TrimSpace(record.SecretCiphertext) != "" {
		secret, decryptErr := m.decrypt(record.SecretCiphertext)
		if decryptErr == nil {
			handle, err = strategy.Restore(ctx, cfg, proxyURL, secret)
			restored = err == nil && handle != nil
			if err == nil && !durable && expiredHandle(handle, now) {
				if handle != nil && handle.Refreshed {
					if saveErr := m.persistAuthentication(ctx, cfg, strategy, handle, true, false); saveErr != nil {
						return nil, saveErr
					}
				}
				// Legacy providers recover an expired restore through Seed/Login.
				handle, record, restored = nil, nil, false
			}
		} else {
			record.LastErrorCategory = string(UpstreamAuthErrorDecrypt)
			record.LastErrorAt = &now
			if err := m.repo.Save(ctx, record); err != nil {
				return nil, err
			}
			if err := m.repo.Delete(ctx, cfg.ID); err != nil {
				return nil, err
			}
			record = nil
		}
	}
	if handle == nil {
		handle, err = strategy.Seed(ctx, cfg, proxyURL)
	}
	if err == nil && handle != nil {
		// Restore and Seed may already have rotated credentials. Persist them
		// before any business request can fail or mutate the provider's cookie jar.
		if handle.Refreshed || !restored && handle.Authenticated {
			if saveErr := m.persistAuthentication(ctx, cfg, strategy, handle, handle.Refreshed, false); saveErr != nil {
				return nil, saveErr
			}
		}
		if expiredHandle(handle, time.Now().UTC()) {
			// Keep the expired handle: it may carry the only usable refresh token.
			err = errUpstreamAuthHandleExpired
		} else {
			if restored && !durable {
				if saveErr := m.persistReuseAttempt(ctx, cfg.ID); saveErr != nil {
					return nil, saveErr
				}
				slog.Info("auth_session_reused", "upstream_config_id", cfg.ID, "provider", cfg.Provider)
			}
			if opErr := operation(ctx, handle); opErr == nil {
				return m.persistOperationSuccess(ctx, cfg, strategy, handle, restored, handle.Refreshed, false)
			} else {
				category := strategy.ClassifyAuthError(opErr)
				if !isRecoverableAuthError(category) {
					if !durable && category != UpstreamAuthErrorConflict {
						return nil, opErr
					}
					return nil, m.recordFailure(ctx, cfg, fingerprint, record, category, opErr)
				}
				err = opErr
			}
		}
	}
	if record != nil && record.CooldownUntil != nil && record.CooldownUntil.After(now) {
		return nil, ErrUpstreamAuthCooldown
	}
	// A compatibility seed may already have refreshed a configured token. It
	// must not immediately refresh a second time when the first API call fails.
	refreshAttempted := handle != nil && handle.Refreshed
	refreshSucceeded := refreshAttempted
	if handle != nil && !refreshAttempted && err != nil && isRecoverableAuthError(strategy.ClassifyAuthError(err)) {
		refreshAttempted = true
		if refreshed, refreshErr := strategy.Refresh(ctx, cfg, proxyURL, handle); refreshErr == nil && refreshed != nil {
			refreshSucceeded = true
			if saveErr := m.persistAuthentication(ctx, cfg, strategy, refreshed, true, false); saveErr != nil {
				return nil, saveErr
			}
			opErr := operation(ctx, refreshed)
			if opErr == nil {
				return m.persistOperationSuccess(ctx, cfg, strategy, refreshed, false, true, false)
			}
			category := strategy.ClassifyAuthError(opErr)
			if !durable && category != UpstreamAuthErrorConflict && !isRecoverableAuthError(category) {
				return nil, opErr
			}
			return nil, m.recordFailure(ctx, cfg, fingerprint, record, category, opErr)
		} else if refreshErr != nil {
			category := strategy.ClassifyAuthError(refreshErr)
			if category == UpstreamAuthErrorConflict {
				return nil, m.recordFailure(ctx, cfg, fingerprint, record, category, refreshErr)
			}
			if policy, ok := strategy.(interface{ CanLoginAfterRefreshError(error) bool }); ok && !policy.CanLoginAfterRefreshError(refreshErr) {
				return nil, m.recordFailure(ctx, cfg, fingerprint, record, category, refreshErr)
			}
			// Preserve the actual refresh failure. Manual-token strategies cannot
			// fall back to Login, while user-login strategies still may below.
			err = refreshErr
		} else {
			err = errUpstreamAuthHandleMissing
			if policy, ok := strategy.(interface{ CanLoginAfterRefreshError(error) bool }); ok && !policy.CanLoginAfterRefreshError(err) {
				return nil, m.recordFailure(ctx, cfg, fingerprint, record, strategy.ClassifyAuthError(err), err)
			}
		}
	}
	// A successful refresh followed by another auth failure is already the
	// single permitted recovery action; only a failed refresh may fall back to login.
	if strategy.CanLogin(cfg) && (!refreshAttempted || !refreshSucceeded) {
		handle, err = strategy.Login(ctx, cfg, proxyURL)
		if err == nil && handle != nil {
			if saveErr := m.persistAuthentication(ctx, cfg, strategy, handle, false, record != nil); saveErr != nil {
				return nil, saveErr
			}
			if opErr := operation(ctx, handle); opErr == nil {
				return m.persistOperationSuccess(ctx, cfg, strategy, handle, false, false, record != nil)
			} else {
				category := strategy.ClassifyAuthError(opErr)
				if category == UpstreamAuthErrorConflict {
					return nil, m.recordFailure(ctx, cfg, fingerprint, record, category, opErr)
				}
				err = opErr
			}
		}
	}
	if err == nil {
		err = errUpstreamAuthHandleMissing
	}
	return nil, m.recordFailure(ctx, cfg, fingerprint, record, strategy.ClassifyAuthError(err), err)
}

func (m *upstreamAuthSessionManager) persistAuthentication(ctx context.Context, cfg *UpstreamConfig, strategy UpstreamAuthStrategy, handle *UpstreamAuthHandle, refreshed, relogin bool) error {
	record, err := m.repo.Get(ctx, cfg.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if record == nil {
		record = &UpstreamAuthSessionRecord{UpstreamConfigID: cfg.ID}
	}
	// Checkpoint credentials independently of counters so a later metadata save
	// cannot discard a successfully rotated ticket.
	if err := m.saveSnapshot(ctx, cfg, strategy, handle, record); err != nil {
		return err
	}
	if !durableAuthLifecycle(strategy) {
		return nil
	}
	m.recordAuthenticationSuccess(cfg, record, refreshed, relogin, now)
	return m.repo.Save(ctx, record)
}

func (m *upstreamAuthSessionManager) recordAuthenticationSuccess(cfg *UpstreamConfig, record *UpstreamAuthSessionRecord, refreshed, relogin bool, now time.Time) {
	if refreshed {
		record.RefreshCount++
		record.LastRefreshedAt = &now
		slog.Info("auth_session_refresh", "upstream_config_id", cfg.ID, "provider", cfg.Provider)
	} else {
		record.LoginCount++
		record.LastAuthenticatedAt = &now
		if relogin {
			record.ReloginCount++
			slog.Info("auth_session_relogin", "upstream_config_id", cfg.ID, "provider", cfg.Provider)
		} else {
			slog.Info("auth_session_login", "upstream_config_id", cfg.ID, "provider", cfg.Provider)
		}
	}
}

func (m *upstreamAuthSessionManager) persistReuseAttempt(ctx context.Context, upstreamConfigID int64) error {
	record, err := m.repo.Get(ctx, upstreamConfigID)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("upstream auth session disappeared during reuse")
	}
	now := time.Now().UTC()
	record.LastUsedAt = &now
	record.ReuseCount++
	record.ConsecutiveAuthFailures, record.LastErrorCategory = 0, ""
	return m.repo.Save(ctx, record)
}

func (m *upstreamAuthSessionManager) persistOperationSuccess(ctx context.Context, cfg *UpstreamConfig, strategy UpstreamAuthStrategy, handle *UpstreamAuthHandle, reused, refreshed, relogin bool) (*UpstreamAuthHandle, error) {
	durable := durableAuthLifecycle(strategy)
	if !durable && reused && !refreshed {
		return handle, nil
	}
	record, err := m.repo.Get(ctx, cfg.ID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		record = &UpstreamAuthSessionRecord{UpstreamConfigID: cfg.ID}
	}
	now := time.Now().UTC()
	record.LastUsedAt = &now
	record.ConsecutiveAuthFailures, record.LastErrorCategory = 0, ""
	if durable {
		record.LastErrorAt = nil
	} else if refreshed || !reused && handle.Authenticated {
		m.recordAuthenticationSuccess(cfg, record, refreshed, relogin, now)
	}
	if reused && durable {
		record.ReuseCount++
	}
	if err := m.saveSnapshot(ctx, cfg, strategy, handle, record); err != nil {
		return nil, err
	}
	if reused && durable {
		slog.Info("auth_session_reused", "upstream_config_id", cfg.ID, "provider", cfg.Provider)
	}
	return handle, nil
}

// saveSnapshot captures provider state without changing authentication counters.
func (m *upstreamAuthSessionManager) saveSnapshot(ctx context.Context, cfg *UpstreamConfig, strategy UpstreamAuthStrategy, handle *UpstreamAuthHandle, record *UpstreamAuthSessionRecord) error {
	secret, err := strategy.Serialize(handle)
	if err != nil {
		return err
	}
	ciphertext, err := m.encrypt(secret)
	if err != nil {
		return err
	}
	record.Provider, record.AuthMode = cfg.Provider, cfg.AuthMode
	record.CredentialFingerprint, record.SecretCiphertext = strategy.Fingerprint(cfg), ciphertext
	record.ExpiresAt = handle.ExpiresAt
	return m.repo.Save(ctx, record)
}

func (m *upstreamAuthSessionManager) recordFailure(ctx context.Context, cfg *UpstreamConfig, fingerprint string, record *UpstreamAuthSessionRecord, category UpstreamAuthErrorCategory, cause error) error {
	latest, err := m.repo.Get(ctx, cfg.ID)
	if err != nil {
		return err
	}
	if latest != nil {
		record = latest
	}
	if category == "" {
		category = UpstreamAuthErrorUnknown
	}
	if record == nil {
		record = &UpstreamAuthSessionRecord{UpstreamConfigID: cfg.ID, Provider: cfg.Provider, AuthMode: cfg.AuthMode, CredentialFingerprint: fingerprint}
	}
	now := time.Now().UTC()
	record.LastErrorCategory, record.LastErrorAt = string(category), &now
	if category == UpstreamAuthErrorUnauthorized || category == UpstreamAuthErrorExpired || category == UpstreamAuthErrorPermanent || category == UpstreamAuthErrorConflict {
		record.ConsecutiveAuthFailures++
	}
	if category == UpstreamAuthErrorConflict || record.ConsecutiveAuthFailures >= UpstreamAuthSessionFailureThreshold {
		until := now.Add(UpstreamAuthSessionCooldown)
		record.CooldownUntil, record.CooldownCount = &until, record.CooldownCount+1
		slog.Warn("auth_session_cooldown", "upstream_config_id", cfg.ID, "provider", cfg.Provider, "category", category)
	}
	if err := m.repo.Save(ctx, record); err != nil {
		return err
	}
	if cause == nil {
		return errors.New(string(category))
	}
	return cause
}

func (m *upstreamAuthSessionManager) Clear(ctx context.Context, upstreamConfigID int64) error {
	if upstreamConfigID <= 0 {
		return errors.New("invalid upstream config id")
	}
	return m.repo.Delete(ctx, upstreamConfigID)
}

func (m *upstreamAuthSessionManager) ClearCooldown(ctx context.Context, upstreamConfigID int64) error {
	return m.repo.ClearCooldown(ctx, upstreamConfigID)
}

func (m *upstreamAuthSessionManager) ForceReauth(ctx context.Context, upstreamConfigID int64) error {
	return m.repo.Delete(ctx, upstreamConfigID)
}

func (m *upstreamAuthSessionManager) Status(ctx context.Context, upstreamConfigID int64, cfg *UpstreamConfig) (*UpstreamAuthSessionStatus, error) {
	if cfg == nil {
		return nil, errors.New("upstream config not found")
	}
	status := &UpstreamAuthSessionStatus{Provider: cfg.Provider, AuthMode: cfg.AuthMode}
	record, err := m.repo.Get(ctx, upstreamConfigID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return status, nil
	}
	status.LastAuthenticatedAt, status.LastRefreshedAt, status.LastUsedAt = record.LastAuthenticatedAt, record.LastRefreshedAt, record.LastUsedAt
	status.CooldownUntil, status.LastErrorCategory = record.CooldownUntil, record.LastErrorCategory
	status.LoginCount, status.ReuseCount, status.RefreshCount, status.ReloginCount, status.CooldownCount = record.LoginCount, record.ReuseCount, record.RefreshCount, record.ReloginCount, record.CooldownCount
	status.Reusable = record.CredentialFingerprint == UpstreamAuthCredentialFingerprint(cfg) && record.SecretCiphertext != "" && (record.ExpiresAt == nil || record.ExpiresAt.After(time.Now().UTC().Add(upstreamAuthSessionExpirySkew))) && (record.CooldownUntil == nil || record.CooldownUntil.Before(time.Now().UTC()))
	return status, nil
}

func (m *upstreamAuthSessionManager) encrypt(secret *UpstreamAuthSessionSecret) (string, error) {
	if m.encryptor == nil {
		return "", errors.New("upstream auth session encryptor is unavailable")
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	return m.encryptor.Encrypt(string(raw))
}

func (m *upstreamAuthSessionManager) decrypt(ciphertext string) (*UpstreamAuthSessionSecret, error) {
	if m.encryptor == nil {
		return nil, errors.New("upstream auth session encryptor is unavailable")
	}
	plain, err := m.encryptor.Decrypt(ciphertext)
	if err != nil {
		return nil, err
	}
	var secret UpstreamAuthSessionSecret
	if err := json.Unmarshal([]byte(plain), &secret); err != nil {
		return nil, err
	}
	return &secret, nil
}

func expiredHandle(handle *UpstreamAuthHandle, now time.Time) bool {
	return handle == nil || handle.ExpiresAt != nil && !handle.ExpiresAt.After(now.Add(upstreamAuthSessionExpirySkew))
}

func isRecoverableAuthError(category UpstreamAuthErrorCategory) bool {
	return category == UpstreamAuthErrorUnauthorized || category == UpstreamAuthErrorExpired
}

func UpstreamAuthCredentialFingerprint(cfg *UpstreamConfig) string {
	if cfg == nil {
		return ""
	}
	keys := []string{
		AccountCredentialSub2APILoginEmail, AccountCredentialSub2APILoginPassword,
		AccountCredentialSub2APIAccessToken, AccountCredentialSub2APIRefreshToken,
		AccountCredentialNewAPILoginUsername, AccountCredentialNewAPILoginPassword,
		AccountCredentialNewAPICookie, AccountCredentialNewAPIAccessToken,
		// Preserve the original material layout even when these retired values
		// are empty, so existing Sub2API/NewAPI sessions keep their fingerprints.
		AccountCredentialNewAPIUserID, AccountCredentialLCodexLoginIdentifier,
		AccountCredentialLCodexLoginPassword,
	}
	material := []string{strings.TrimSpace(cfg.Provider), strings.TrimSpace(cfg.AuthMode), strings.TrimSpace(cfg.SiteURL), cfg.EffectiveAPIURL()}
	for _, key := range keys {
		material = append(material, key+"="+strings.TrimSpace(stringCredential(cfg.Credentials, key)))
	}
	sum := sha256.Sum256([]byte(strings.Join(material, "\x00")))
	return hex.EncodeToString(sum[:])
}
