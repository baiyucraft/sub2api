package service

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	UpstreamSchedulerConcurrencyOverrideKey = "scheduler_concurrency_override"

	ConcurrencyTargetAccount  = "account"
	ConcurrencyTargetUpstream = "upstream"

	UpstreamConcurrencySourceOverride  = "override"
	UpstreamConcurrencySourceProvider  = "provider"
	UpstreamConcurrencySourceUnlimited = "unlimited"
	UpstreamConcurrencySourceDefault   = "default"

	DefaultUpstreamSchedulerConcurrency = 100
	MaxUpstreamSchedulerConcurrency     = 1_000_000
)

// UpstreamSchedulerConcurrency is the normalized scheduling capacity derived
// from one upstream config. Limit == 0 means explicitly unlimited.
type UpstreamSchedulerConcurrency struct {
	Limit       int    `json:"limit"`
	Source      string `json:"source"`
	UsesDefault bool   `json:"uses_default"`
	Unlimited   bool   `json:"unlimited"`
	Override    *int   `json:"override,omitempty"`
}

// ResolveUpstreamSchedulerConcurrency is the single authority for resolving
// the shared concurrency limit of an upstream config.
func ResolveUpstreamSchedulerConcurrency(extra map[string]any) UpstreamSchedulerConcurrency {
	if override, ok := positiveBoundedInt(extraValue(extra, UpstreamSchedulerConcurrencyOverrideKey)); ok {
		return UpstreamSchedulerConcurrency{
			Limit:    override,
			Source:   UpstreamConcurrencySourceOverride,
			Override: intPointer(override),
		}
	}

	snapshot, _ := extraValue(extra, upstreamConcurrencySnapshotKey).(map[string]any)
	if upstreamString(snapshot["status"]) == upstreamConcurrencyStatusCurrent {
		switch upstreamString(snapshot["semantics"]) {
		case upstreamConcurrencySemanticsLimited:
			if limit, ok := positiveBoundedInt(snapshot["limit"]); ok {
				return UpstreamSchedulerConcurrency{Limit: limit, Source: UpstreamConcurrencySourceProvider}
			}
		case upstreamConcurrencySemanticsProviderDefined:
			if limit, ok := positiveBoundedInt(snapshot["raw_value"]); ok {
				return UpstreamSchedulerConcurrency{Limit: limit, Source: UpstreamConcurrencySourceProvider}
			}
		case upstreamConcurrencySemanticsUnlimited:
			if raw, ok := nonNegativeInt(snapshot["raw_value"]); ok && raw == 0 {
				return UpstreamSchedulerConcurrency{Source: UpstreamConcurrencySourceUnlimited, Unlimited: true}
			}
		}
	}

	return UpstreamSchedulerConcurrency{
		Limit:       DefaultUpstreamSchedulerConcurrency,
		Source:      UpstreamConcurrencySourceDefault,
		UsesDefault: true,
	}
}

func extraValue(extra map[string]any, key string) any {
	if extra == nil {
		return nil
	}
	return extra[key]
}

func positiveBoundedInt(value any) (int, bool) {
	parsed, ok := nonNegativeInt(value)
	if !ok || parsed < 1 || parsed > MaxUpstreamSchedulerConcurrency {
		return 0, false
	}
	return parsed, true
}

func nonNegativeInt(value any) (int, bool) {
	var parsed int64
	switch v := value.(type) {
	case int:
		parsed = int64(v)
	case int8:
		parsed = int64(v)
	case int16:
		parsed = int64(v)
	case int32:
		parsed = int64(v)
	case int64:
		parsed = v
	case uint:
		if uint64(v) > uint64(^uint(0)>>1) {
			return 0, false
		}
		parsed = int64(v)
	case uint64:
		if v > uint64(^uint(0)>>1) {
			return 0, false
		}
		parsed = int64(v)
	case float64:
		if v < 0 || v != float64(int64(v)) {
			return 0, false
		}
		parsed = int64(v)
	case json.Number:
		var err error
		parsed, err = v.Int64()
		if err != nil {
			return 0, false
		}
	case string:
		text := strings.TrimSpace(v)
		if text == "" || !isDecimalInteger(text) {
			return 0, false
		}
		var err error
		parsed, err = strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if parsed < 0 || parsed > int64(MaxUpstreamSchedulerConcurrency) {
		return 0, false
	}
	return int(parsed), true
}

func intPointer(value int) *int { return &value }

type ConcurrencyTarget struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Limit int    `json:"limit"`
}

func (t ConcurrencyTarget) normalized() ConcurrencyTarget {
	if t.Kind != ConcurrencyTargetUpstream || t.ID <= 0 {
		t.Kind = ConcurrencyTargetAccount
	}
	return t
}

func (t ConcurrencyTarget) Key() string {
	t = t.normalized()
	return t.Kind + ":" + strconv.FormatInt(t.ID, 10)
}

func (a *Account) SchedulingConcurrencyTarget() ConcurrencyTarget {
	if a == nil {
		return ConcurrencyTarget{Kind: ConcurrencyTargetAccount}
	}
	if a.UpstreamConfigID != nil && *a.UpstreamConfigID > 0 {
		limit := a.UpstreamConcurrencyLimit
		if a.UpstreamConcurrencyUnlimited {
			limit = 0
		} else if limit <= 0 {
			limit = DefaultUpstreamSchedulerConcurrency
		}
		return ConcurrencyTarget{Kind: ConcurrencyTargetUpstream, ID: *a.UpstreamConfigID, Limit: limit}
	}
	limit := a.Concurrency
	if limit < 1 {
		limit = 1
	}
	return ConcurrencyTarget{Kind: ConcurrencyTargetAccount, ID: a.ID, Limit: limit}
}

func AccountConcurrencyLoadDescriptor(account *Account) AccountWithConcurrency {
	if account == nil {
		return AccountWithConcurrency{}
	}
	target := account.SchedulingConcurrencyTarget()
	return AccountWithConcurrency{
		ID:             account.ID,
		MaxConcurrency: target.Limit,
		TargetKind:     target.Kind,
		TargetID:       target.ID,
	}
}

// AccountSchedulingLoadDescriptor keeps LoadFactor as a scheduling-only
// virtual capacity for ordinary accounts. Upstream-bound accounts always use
// the shared upstream concurrency limit for both capacity and load ranking.
func AccountSchedulingLoadDescriptor(account *Account) AccountWithConcurrency {
	if account == nil {
		return AccountWithConcurrency{}
	}
	target := account.SchedulingConcurrencyTarget()
	maxConcurrency := target.Limit
	if target.Kind == ConcurrencyTargetAccount {
		maxConcurrency = account.EffectiveLoadFactor()
	}
	return AccountWithConcurrency{
		ID:             account.ID,
		MaxConcurrency: maxConcurrency,
		TargetKind:     target.Kind,
		TargetID:       target.ID,
	}
}
