package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const (
	upstreamConcurrencySnapshotKey = "upstream_concurrency_snapshot"

	upstreamConcurrencyStatusCurrent     = "current"
	upstreamConcurrencyStatusStale       = "stale"
	upstreamConcurrencyStatusUnsupported = "unsupported"

	upstreamConcurrencySemanticsLimited         = "limited"
	upstreamConcurrencySemanticsUnlimited       = "unlimited"
	upstreamConcurrencySemanticsProviderDefined = "provider_defined"
	upstreamConcurrencySemanticsUnknown         = "unknown"
)

func upstreamConcurrencySnapshotUpdates(cfg *UpstreamConfig, provider string, raw json.RawMessage, profileErr error) (map[string]any, string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if profileErr != nil {
		return map[string]any{upstreamConcurrencySnapshotKey: staleUpstreamConcurrencySnapshot(cfg, provider, now)}, ""
	}

	value, rawValue, present, err := parseOptionalUpstreamConcurrency(raw)
	snapshot := map[string]any{
		"version":         1,
		"provider":        provider,
		"last_checked_at": now,
	}
	if !present {
		snapshot["status"] = upstreamConcurrencyStatusUnsupported
		snapshot["semantics"] = upstreamConcurrencySemanticsUnknown
		return map[string]any{upstreamConcurrencySnapshotKey: snapshot}, ""
	}
	if err != nil {
		snapshot["status"] = upstreamConcurrencyStatusUnsupported
		snapshot["semantics"] = upstreamConcurrencySemanticsUnknown
		return map[string]any{upstreamConcurrencySnapshotKey: snapshot}, fmt.Sprintf("%s concurrency value is invalid and was ignored", provider)
	}
	snapshot["status"] = upstreamConcurrencyStatusCurrent
	snapshot["observed_at"] = now

	switch provider {
	case UpstreamProviderSub2API:
		persistedValue := strconv.FormatInt(value, 10)
		snapshot["raw_value"] = persistedValue
		if value == 0 {
			snapshot["semantics"] = upstreamConcurrencySemanticsUnlimited
		} else {
			snapshot["semantics"] = upstreamConcurrencySemanticsLimited
			snapshot["limit"] = persistedValue
		}
	case UpstreamProviderNewAPI:
		snapshot["semantics"] = upstreamConcurrencySemanticsProviderDefined
		snapshot["raw_value"] = persistedUpstreamConcurrencyRawValue(value, rawValue)
	default:
		snapshot["status"] = upstreamConcurrencyStatusUnsupported
		snapshot["semantics"] = upstreamConcurrencySemanticsUnknown
		delete(snapshot, "observed_at")
		delete(snapshot, "raw_value")
	}
	return map[string]any{upstreamConcurrencySnapshotKey: snapshot}, ""
}

func parseOptionalUpstreamConcurrency(raw json.RawMessage) (int64, any, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, nil, false, nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, nil, true, fmt.Errorf("invalid concurrency string")
		}
		if !isDecimalInteger(value) {
			return 0, nil, true, fmt.Errorf("concurrency string must contain only decimal digits")
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, nil, true, fmt.Errorf("concurrency value overflows int64")
		}
		return parsed, value, true, nil
	}

	text := string(raw)
	if !isDecimalInteger(text) {
		return 0, nil, true, fmt.Errorf("concurrency number must be a JSON integer")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, nil, true, fmt.Errorf("concurrency value overflows int64")
	}
	return parsed, parsed, true, nil
}

func isDecimalInteger(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func persistedUpstreamConcurrencyRawValue(value int64, rawValue any) string {
	if text, ok := rawValue.(string); ok {
		return text
	}
	return strconv.FormatInt(value, 10)
}

func staleUpstreamConcurrencySnapshot(cfg *UpstreamConfig, provider, now string) map[string]any {
	snapshot := map[string]any{
		"version":         1,
		"provider":        provider,
		"status":          upstreamConcurrencyStatusStale,
		"semantics":       upstreamConcurrencySemanticsUnknown,
		"last_checked_at": now,
	}
	if cfg == nil || cfg.Extra == nil {
		return snapshot
	}
	previous, ok := cfg.Extra[upstreamConcurrencySnapshotKey].(map[string]any)
	if !ok || previous == nil {
		return snapshot
	}
	if upstreamString(previous["provider"]) != provider {
		return snapshot
	}
	semantics := upstreamString(previous["semantics"])
	if semantics != upstreamConcurrencySemanticsLimited && semantics != upstreamConcurrencySemanticsUnlimited && semantics != upstreamConcurrencySemanticsProviderDefined {
		return snapshot
	}
	if previousStatus := upstreamString(previous["status"]); previousStatus != upstreamConcurrencyStatusCurrent && previousStatus != upstreamConcurrencyStatusStale {
		return snapshot
	}
	rawValue, rawValueOK := validPersistedUpstreamConcurrencyValue(previous["raw_value"])
	if !rawValueOK {
		return snapshot
	}
	if semantics == upstreamConcurrencySemanticsLimited {
		limit, limitOK := validPersistedUpstreamConcurrencyValue(previous["limit"])
		rawParsed, _ := strconv.ParseInt(rawValue, 10, 64)
		limitParsed, _ := strconv.ParseInt(limit, 10, 64)
		if !limitOK || rawParsed <= 0 || limitParsed != rawParsed {
			return snapshot
		}
		snapshot["limit"] = limit
		snapshot["raw_value"] = rawValue
	} else if semantics == upstreamConcurrencySemanticsUnlimited {
		rawParsed, _ := strconv.ParseInt(rawValue, 10, 64)
		if rawParsed != 0 {
			return snapshot
		}
		snapshot["raw_value"] = rawValue
	} else {
		snapshot["raw_value"] = rawValue
	}
	snapshot["semantics"] = semantics
	if observedAt, ok := previous["observed_at"]; ok {
		snapshot["observed_at"] = observedAt
	}
	return snapshot
}

func validPersistedUpstreamConcurrencyValue(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || !isDecimalInteger(text) {
		return "", false
	}
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		return "", false
	}
	return text, true
}
