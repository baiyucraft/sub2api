package legacy

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

func ResolveUpstreamSchedulerConcurrency(extra map[string]any) forkscheduling.UpstreamSchedulerConcurrency {
	if override, ok := positiveBoundedInt(extraValue(extra, forkscheduling.UpstreamSchedulerConcurrencyOverrideKey)); ok {
		return forkscheduling.UpstreamSchedulerConcurrency{Limit: override, Source: forkscheduling.UpstreamConcurrencySourceOverride, Override: intPointer(override)}
	}

	snapshot, _ := extraValue(extra, "upstream_concurrency_snapshot").(map[string]any)
	if stringValue(snapshot["status"]) == "current" {
		switch stringValue(snapshot["semantics"]) {
		case "limited":
			if limit, ok := positiveBoundedInt(snapshot["limit"]); ok {
				return forkscheduling.UpstreamSchedulerConcurrency{Limit: limit, Source: forkscheduling.UpstreamConcurrencySourceProvider}
			}
		case "provider_defined":
			if limit, ok := positiveBoundedInt(snapshot["raw_value"]); ok {
				return forkscheduling.UpstreamSchedulerConcurrency{Limit: limit, Source: forkscheduling.UpstreamConcurrencySourceProvider}
			}
		case "unlimited":
			if raw, ok := nonNegativeInt(snapshot["raw_value"]); ok && raw == 0 {
				return forkscheduling.UpstreamSchedulerConcurrency{Source: forkscheduling.UpstreamConcurrencySourceUnlimited, Unlimited: true}
			}
		}
	}
	return forkscheduling.UpstreamSchedulerConcurrency{Limit: forkscheduling.DefaultUpstreamSchedulerConcurrency, Source: forkscheduling.UpstreamConcurrencySourceDefault, UsesDefault: true}
}

func ResolveConcurrencyTarget(view forkscheduling.ConcurrencyAccountView) forkscheduling.ConcurrencyTarget {
	if view.UpstreamConfigID > 0 {
		limit := view.UpstreamLimit
		if view.UpstreamUnlimited {
			limit = 0
		} else if limit <= 0 {
			limit = forkscheduling.DefaultUpstreamSchedulerConcurrency
		}
		return forkscheduling.ConcurrencyTarget{Kind: forkscheduling.ConcurrencyTargetUpstream, ID: view.UpstreamConfigID, Limit: limit}
	}
	limit := view.AccountLimit
	if limit < 1 {
		limit = 1
	}
	return forkscheduling.ConcurrencyTarget{Kind: forkscheduling.ConcurrencyTargetAccount, ID: view.AccountID, Limit: limit}
}

type TargetPolicy struct{}

func (TargetPolicy) Resolve(view forkscheduling.ConcurrencyAccountView) forkscheduling.ConcurrencyTarget {
	return ResolveConcurrencyTarget(view)
}

func extraValue(extra map[string]any, key string) any {
	if extra == nil {
		return nil
	}
	return extra[key]
}

func positiveBoundedInt(value any) (int, bool) {
	parsed, ok := nonNegativeInt(value)
	if !ok || parsed < 1 || parsed > forkscheduling.MaxUpstreamSchedulerConcurrency {
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
	if parsed < 0 || parsed > int64(forkscheduling.MaxUpstreamSchedulerConcurrency) {
		return 0, false
	}
	return int(parsed), true
}

func isDecimalInteger(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if i == 0 && r == '-' {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "-"
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intPointer(value int) *int { return &value }
