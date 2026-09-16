package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const sessionSwitchStatePrefix = "session_switch_state:"
const sessionSwitchStickyPrefix = "session-switch:v1:"

// gatewayCacheWithForkExtensions keeps fork-owned optional stores outside the
// upstream cache implementation while preserving every optional interface
// implemented by the embedded gateway cache.
type gatewayCacheWithForkExtensions struct {
	*gatewayCache
	service.SessionSwitchStateStore
}

func newGatewayCacheWithForkExtensions(rdb *redis.Client) service.GatewayCache {
	return &gatewayCacheWithForkExtensions{
		gatewayCache:            &gatewayCache{rdb: rdb},
		SessionSwitchStateStore: NewSessionSwitchStateStore(rdb),
	}
}

func sessionSwitchStickyHash(ctx context.Context, sessionHash string) string {
	apiKeyID := service.SessionSwitchStickyNamespaceAPIKeyID(ctx)
	if apiKeyID <= 0 || strings.TrimSpace(sessionHash) == "" {
		return sessionHash
	}
	return fmt.Sprintf("%s%d:%s", sessionSwitchStickyPrefix, apiKeyID, sessionHash)
}

func (c *gatewayCacheWithForkExtensions) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	return c.gatewayCache.GetSessionAccountID(ctx, groupID, sessionSwitchStickyHash(ctx, sessionHash))
}

func (c *gatewayCacheWithForkExtensions) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	return c.gatewayCache.SetSessionAccountID(ctx, groupID, sessionSwitchStickyHash(ctx, sessionHash), accountID, ttl)
}

func (c *gatewayCacheWithForkExtensions) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return c.gatewayCache.RefreshSessionTTL(ctx, groupID, sessionSwitchStickyHash(ctx, sessionHash), ttl)
}

func (c *gatewayCacheWithForkExtensions) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	return c.gatewayCache.DeleteSessionAccountID(ctx, groupID, sessionSwitchStickyHash(ctx, sessionHash))
}

type sessionSwitchStateStore struct {
	rdb *redis.Client
}

// NewSessionSwitchStateStore exposes the fork-owned session failure and
// cooldown store without extending the upstream GatewayCache contract.
func NewSessionSwitchStateStore(rdb *redis.Client) service.SessionSwitchStateStore {
	return &sessionSwitchStateStore{rdb: rdb}
}

func hashSessionSwitchKeyComponents(components ...string) string {
	h := sha256.New()
	for _, component := range components {
		_, _ = fmt.Fprintf(h, "%d:", len(component))
		_, _ = h.Write([]byte(component))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeSessionSwitchScope(scope service.SessionSwitchScope) (service.SessionSwitchScope, error) {
	scope.SessionHash = strings.TrimSpace(scope.SessionHash)
	scope.RouteModel = strings.TrimSpace(scope.RouteModel)
	if scope.APIKeyID <= 0 || scope.GroupID <= 0 || scope.SessionHash == "" || scope.RouteModel == "" {
		return service.SessionSwitchScope{}, errors.New("invalid session switch scope")
	}
	return scope, nil
}

func sessionSwitchScopeDigest(scope service.SessionSwitchScope) string {
	return hashSessionSwitchKeyComponents(
		strconv.FormatInt(scope.APIKeyID, 10),
		strconv.FormatInt(scope.GroupID, 10),
		scope.SessionHash,
		scope.RouteModel,
	)
}

func sessionSwitchFailureKey(scope service.SessionSwitchAccountScope) (string, error) {
	normalized, err := normalizeSessionSwitchScope(scope.SessionSwitchScope)
	if err != nil || scope.AccountID <= 0 {
		return "", errors.New("invalid session switch account scope")
	}
	scopeDigest := sessionSwitchScopeDigest(normalized)
	accountDigest := hashSessionSwitchKeyComponents(strconv.FormatInt(scope.AccountID, 10))
	return fmt.Sprintf("%s{%s}:failures:%s", sessionSwitchStatePrefix, scopeDigest, accountDigest), nil
}

func sessionSwitchCooldownKey(scope service.SessionSwitchScope) (string, error) {
	normalized, err := normalizeSessionSwitchScope(scope)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s{%s}:cooldowns", sessionSwitchStatePrefix, sessionSwitchScopeDigest(normalized)), nil
}

var recordSessionSwitchFailureScript = redis.NewScript(`
local failure_key = KEYS[1]
local cooldown_key = KEYS[2]
local window_ms = tonumber(ARGV[1])
local threshold = tonumber(ARGV[2])
local cooldown_ms = tonumber(ARGV[3])
local account_id = ARGV[4]

local count = redis.call('INCR', failure_key)
if count == 1 then
  redis.call('PEXPIRE', failure_key, window_ms)
end
if count < threshold then
  return {count, 0, 0}
end

local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
local cooldown_until = now_ms + cooldown_ms
redis.call('DEL', failure_key)
redis.call('ZREMRANGEBYSCORE', cooldown_key, '-inf', now_ms)

local existing_until = redis.call('ZSCORE', cooldown_key, account_id)
if existing_until ~= false and tonumber(existing_until) > cooldown_until then
  cooldown_until = tonumber(existing_until)
else
  redis.call('ZADD', cooldown_key, cooldown_until, account_id)
end

local latest = redis.call('ZREVRANGE', cooldown_key, 0, 0, 'WITHSCORES')
if #latest == 2 then
  redis.call('PEXPIREAT', cooldown_key, math.floor(tonumber(latest[2])))
end
return {count, 1, cooldown_until}
`)

var listSessionSwitchCooldownAccountsScript = redis.NewScript(`
local cooldown_key = KEYS[1]
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', cooldown_key, '-inf', now_ms)
local accounts = redis.call('ZRANGE', cooldown_key, 0, -1)
if #accounts == 0 then
  redis.call('DEL', cooldown_key)
end
return accounts
`)

func (s *sessionSwitchStateStore) RecordSessionSwitchFailure(ctx context.Context, scope service.SessionSwitchAccountScope, window time.Duration, threshold int, cooldown time.Duration) (service.SessionSwitchFailureState, error) {
	if s == nil || s.rdb == nil {
		return service.SessionSwitchFailureState{}, errors.New("session switch state store unavailable")
	}
	if window < time.Millisecond || threshold <= 0 || cooldown < time.Millisecond {
		return service.SessionSwitchFailureState{}, errors.New("invalid session switch failure policy")
	}
	failureKey, err := sessionSwitchFailureKey(scope)
	if err != nil {
		return service.SessionSwitchFailureState{}, err
	}
	cooldownKey, err := sessionSwitchCooldownKey(scope.SessionSwitchScope)
	if err != nil {
		return service.SessionSwitchFailureState{}, err
	}
	result, err := recordSessionSwitchFailureScript.Run(
		ctx,
		s.rdb,
		[]string{failureKey, cooldownKey},
		window.Milliseconds(),
		threshold,
		cooldown.Milliseconds(),
		scope.AccountID,
	).Slice()
	if err != nil {
		return service.SessionSwitchFailureState{}, fmt.Errorf("record session switch failure: %w", err)
	}
	if len(result) != 3 {
		return service.SessionSwitchFailureState{}, fmt.Errorf("record session switch failure: unexpected result length %d", len(result))
	}
	count, err := sessionSwitchScriptInt64At(result, 0)
	if err != nil {
		return service.SessionSwitchFailureState{}, fmt.Errorf("record session switch failure count: %w", err)
	}
	trippedValue, err := sessionSwitchScriptInt64At(result, 1)
	if err != nil {
		return service.SessionSwitchFailureState{}, fmt.Errorf("record session switch failure trip state: %w", err)
	}
	cooldownUntilMillis, err := sessionSwitchScriptInt64At(result, 2)
	if err != nil {
		return service.SessionSwitchFailureState{}, fmt.Errorf("record session switch failure cooldown: %w", err)
	}
	state := service.SessionSwitchFailureState{FailureCount: count, Tripped: trippedValue == 1}
	if cooldownUntilMillis > 0 {
		state.CooldownUntil = time.UnixMilli(cooldownUntilMillis)
	}
	return state, nil
}

func (s *sessionSwitchStateStore) ClearSessionSwitchFailures(ctx context.Context, scope service.SessionSwitchAccountScope) error {
	if s == nil || s.rdb == nil {
		return errors.New("session switch state store unavailable")
	}
	key, err := sessionSwitchFailureKey(scope)
	if err != nil {
		return err
	}
	return s.rdb.Del(ctx, key).Err()
}

func (s *sessionSwitchStateStore) ListSessionSwitchCooldownAccountIDs(ctx context.Context, scope service.SessionSwitchScope) ([]int64, error) {
	if s == nil || s.rdb == nil {
		return nil, errors.New("session switch state store unavailable")
	}
	key, err := sessionSwitchCooldownKey(scope)
	if err != nil {
		return nil, err
	}
	values, err := listSessionSwitchCooldownAccountsScript.Run(ctx, s.rdb, []string{key}).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("list session switch cooldown accounts: %w", err)
	}
	accountIDs := make([]int64, 0, len(values))
	for _, value := range values {
		accountID, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || accountID <= 0 {
			return nil, fmt.Errorf("list session switch cooldown accounts: invalid account id %q", value)
		}
		accountIDs = append(accountIDs, accountID)
	}
	sort.Slice(accountIDs, func(i, j int) bool { return accountIDs[i] < accountIDs[j] })
	return accountIDs, nil
}

func sessionSwitchScriptInt64At(values []any, index int) (int64, error) {
	if index < 0 || index >= len(values) {
		return 0, fmt.Errorf("redis script array missing index %d", index)
	}
	switch value := values[index].(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case string:
		return strconv.ParseInt(value, 10, 64)
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis script value %T", value)
	}
}

var _ service.GatewayCache = (*gatewayCacheWithForkExtensions)(nil)
var _ service.SessionSwitchStateStore = (*gatewayCacheWithForkExtensions)(nil)
var _ service.SessionSwitchStateStore = (*sessionSwitchStateStore)(nil)
var _ service.LiveCallStore = (*gatewayCacheWithForkExtensions)(nil)
var _ service.CyberSessionBlockStore = (*gatewayCacheWithForkExtensions)(nil)
var _ service.OpenAIWSSessionPreemptionCache = (*gatewayCacheWithForkExtensions)(nil)
