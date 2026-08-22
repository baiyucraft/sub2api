package service

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	Sub2APIImagePricingSnapshotExtraKey   = "sub2api_image_pricing_snapshot"
	Sub2APIVideoPricingSnapshotExtraKey   = "sub2api_video_pricing_snapshot"
	Sub2APILongContextSnapshotExtraKey    = "sub2api_long_context_snapshot"
	sub2APIImagePricingSnapshotVersion    = 1
	sub2APIVideoPricingSnapshotVersion    = 1
	sub2APILongContextSnapshotVersion     = 1
	LCodexImageCapabilitySnapshotExtraKey = "lcodex_image_pricing_snapshot"
	lcodexImageCapabilitySnapshotVersion  = 1

	UpstreamKeyImagePricingStatusAvailable   = "available"
	UpstreamKeyImagePricingStatusPartial     = "partial"
	UpstreamKeyImagePricingStatusDisabled    = "disabled"
	UpstreamKeyImagePricingStatusUnavailable = "unavailable"
)

type UpstreamKeyImagePricing struct {
	Supported               bool
	Status                  string
	Stale                   bool
	Currency                string
	RateIndependent         bool
	EffectiveRateMultiplier *float64
	FinalCost1K             *float64
	FinalCost2K             *float64
	FinalCost4K             *float64
	ObservedAt              *time.Time
	PricingSource           string
	DefaultedTiers          []string
}

// UpstreamKeyVideoPricing is the redacted video capability and pricing
// snapshot exposed to the upstream-management account list.
type UpstreamKeyVideoPricing struct {
	Supported               bool
	Status                  string
	Stale                   bool
	RateIndependent         bool
	EffectiveRateMultiplier *float64
	FinalCost480p           *float64
	FinalCost720p           *float64
	FinalCost1080p          *float64
	ObservedAt              *time.Time
}

// UpstreamLongContextState records whether long-context billing was
// explicitly enabled by the bound upstream key. Status "unknown" preserves
// compatibility with keys created before this snapshot existed.
type UpstreamLongContextState struct {
	Enabled    bool
	Status     string
	Stale      bool
	Source     string
	ObservedAt *time.Time
}

type sub2APIImagePricingSnapshot struct {
	Version              int        `json:"version"`
	Status               string     `json:"status"`
	AllowImageGeneration bool       `json:"allow_image_generation"`
	ImageRateIndependent bool       `json:"image_rate_independent"`
	ImageRateMultiplier  *float64   `json:"image_rate_multiplier"`
	ImagePrice1K         *float64   `json:"image_price_1k"`
	ImagePrice2K         *float64   `json:"image_price_2k"`
	ImagePrice4K         *float64   `json:"image_price_4k"`
	ObservedAt           *time.Time `json:"observed_at"`
	Stale                bool       `json:"stale"`
	PricingSource        string     `json:"pricing_source,omitempty"`
	DefaultedTiers       []string   `json:"defaulted_tiers,omitempty"`
}

type sub2APIVideoPricingSnapshot struct {
	Version              int        `json:"version"`
	Status               string     `json:"status"`
	AllowVideoGeneration bool       `json:"allow_video_generation"`
	VideoRateIndependent bool       `json:"video_rate_independent"`
	VideoRateMultiplier  *float64   `json:"video_rate_multiplier"`
	VideoPrice480p       *float64   `json:"video_price_480p"`
	VideoPrice720p       *float64   `json:"video_price_720p"`
	VideoPrice1080p      *float64   `json:"video_price_1080p"`
	ObservedAt           *time.Time `json:"observed_at"`
	Stale                bool       `json:"stale"`
}

type sub2APILongContextSnapshot struct {
	Version    int        `json:"version"`
	Status     string     `json:"status"`
	Enabled    bool       `json:"enabled"`
	ObservedAt *time.Time `json:"observed_at"`
	Stale      bool       `json:"stale"`
}

type lcodexImageCapabilitySnapshot struct {
	Version              int        `json:"version"`
	Status               string     `json:"status"`
	AllowImageGeneration bool       `json:"allow_image_generation"`
	ObservedAt           *time.Time `json:"observed_at"`
	Stale                bool       `json:"stale"`
}

func lcodexImageCapabilitySnapshotMap(snapshot lcodexImageCapabilitySnapshot) map[string]any {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func parseLCodexImageCapabilitySnapshot(extra map[string]any) (lcodexImageCapabilitySnapshot, bool) {
	if extra == nil {
		return lcodexImageCapabilitySnapshot{}, false
	}
	raw, ok := extra[LCodexImageCapabilitySnapshotExtraKey]
	if !ok || raw == nil {
		return lcodexImageCapabilitySnapshot{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return lcodexImageCapabilitySnapshot{}, false
	}
	var snapshot lcodexImageCapabilitySnapshot
	if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Version != lcodexImageCapabilitySnapshotVersion {
		return lcodexImageCapabilitySnapshot{}, false
	}
	if snapshot.Status != UpstreamKeyImagePricingStatusPartial && snapshot.Status != UpstreamKeyImagePricingStatusDisabled && snapshot.Status != UpstreamKeyImagePricingStatusUnavailable {
		return lcodexImageCapabilitySnapshot{}, false
	}
	return snapshot, true
}

func newUnavailableSub2APIImagePricingSnapshot(stale bool) sub2APIImagePricingSnapshot {
	return sub2APIImagePricingSnapshot{
		Version: sub2APIImagePricingSnapshotVersion,
		Status:  UpstreamKeyImagePricingStatusUnavailable,
		Stale:   stale,
	}
}

func sub2APIImagePricingSnapshotMap(snapshot sub2APIImagePricingSnapshot) map[string]any {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func sub2APIVideoPricingSnapshotMap(snapshot sub2APIVideoPricingSnapshot) map[string]any {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func sub2APILongContextSnapshotMap(snapshot sub2APILongContextSnapshot) map[string]any {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func parseSub2APIImagePricingSnapshot(extra map[string]any) (sub2APIImagePricingSnapshot, bool) {
	if extra == nil {
		return sub2APIImagePricingSnapshot{}, false
	}
	raw, ok := extra[Sub2APIImagePricingSnapshotExtraKey]
	if !ok || raw == nil {
		return sub2APIImagePricingSnapshot{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sub2APIImagePricingSnapshot{}, false
	}
	var snapshot sub2APIImagePricingSnapshot
	if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Version != sub2APIImagePricingSnapshotVersion {
		return sub2APIImagePricingSnapshot{}, false
	}
	switch snapshot.Status {
	case UpstreamKeyImagePricingStatusAvailable,
		UpstreamKeyImagePricingStatusPartial,
		UpstreamKeyImagePricingStatusDisabled,
		UpstreamKeyImagePricingStatusUnavailable:
	default:
		return sub2APIImagePricingSnapshot{}, false
	}
	return snapshot, true
}

func parseSub2APIVideoPricingSnapshot(extra map[string]any) (sub2APIVideoPricingSnapshot, bool) {
	if extra == nil {
		return sub2APIVideoPricingSnapshot{}, false
	}
	raw, ok := extra[Sub2APIVideoPricingSnapshotExtraKey]
	if !ok || raw == nil {
		return sub2APIVideoPricingSnapshot{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sub2APIVideoPricingSnapshot{}, false
	}
	var snapshot sub2APIVideoPricingSnapshot
	if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Version != sub2APIVideoPricingSnapshotVersion {
		return sub2APIVideoPricingSnapshot{}, false
	}
	switch snapshot.Status {
	case UpstreamKeyImagePricingStatusAvailable, UpstreamKeyImagePricingStatusPartial, UpstreamKeyImagePricingStatusDisabled, UpstreamKeyImagePricingStatusUnavailable:
	default:
		return sub2APIVideoPricingSnapshot{}, false
	}
	return snapshot, true
}

func parseSub2APILongContextSnapshot(extra map[string]any) (sub2APILongContextSnapshot, bool) {
	if extra == nil {
		return sub2APILongContextSnapshot{}, false
	}
	raw, ok := extra[Sub2APILongContextSnapshotExtraKey]
	if !ok || raw == nil {
		return sub2APILongContextSnapshot{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sub2APILongContextSnapshot{}, false
	}
	var snapshot sub2APILongContextSnapshot
	if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Version != sub2APILongContextSnapshotVersion {
		return sub2APILongContextSnapshot{}, false
	}
	if snapshot.Status != "known" && snapshot.Status != "unknown" {
		return sub2APILongContextSnapshot{}, false
	}
	return snapshot, true
}

func deriveUpstreamKeyImagePricing(key *UpstreamKey, config *UpstreamConfig) *UpstreamKeyImagePricing {
	if key == nil || config == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(config.Provider), UpstreamProviderLCodex) {
		snapshot, ok := parseLCodexImageCapabilitySnapshot(key.Extra)
		if !ok {
			return &UpstreamKeyImagePricing{Status: UpstreamKeyImagePricingStatusUnavailable, Currency: "USD"}
		}
		out := &UpstreamKeyImagePricing{
			Supported:  snapshot.AllowImageGeneration,
			Status:     snapshot.Status,
			Stale:      snapshot.Stale,
			Currency:   "USD",
			ObservedAt: snapshot.ObservedAt,
		}
		if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
			out.Stale = true
		}
		return out
	}
	snapshot, ok := parseSub2APIImagePricingSnapshot(key.Extra)
	if !ok {
		return &UpstreamKeyImagePricing{
			Status:   UpstreamKeyImagePricingStatusUnavailable,
			Currency: "USD",
		}
	}
	out := &UpstreamKeyImagePricing{
		Supported:       snapshot.AllowImageGeneration,
		Status:          snapshot.Status,
		Stale:           snapshot.Stale,
		Currency:        "USD",
		RateIndependent: snapshot.ImageRateIndependent,
		ObservedAt:      snapshot.ObservedAt,
		PricingSource:   snapshot.PricingSource,
		DefaultedTiers:  append([]string(nil), snapshot.DefaultedTiers...),
	}
	if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
		out.Stale = true
	}
	if !snapshot.AllowImageGeneration || snapshot.Status == UpstreamKeyImagePricingStatusDisabled || snapshot.Status == UpstreamKeyImagePricingStatusUnavailable {
		return out
	}
	if snapshot.Status == UpstreamKeyImagePricingStatusPartial && snapshot.PricingSource == "" {
		platform := ""
		if key.Platform != nil {
			platform = *key.Platform
		}
		defaults := defaultImagePricesForPlatform(platform)
		for _, tier := range []struct {
			name     string
			value    **float64
			fallback *float64
		}{
			{"1K", &snapshot.ImagePrice1K, defaults[0]},
			{"2K", &snapshot.ImagePrice2K, defaults[1]},
			{"4K", &snapshot.ImagePrice4K, defaults[2]},
		} {
			if *tier.value == nil {
				fallback := *tier.fallback
				*tier.value = &fallback
				snapshot.DefaultedTiers = append(snapshot.DefaultedTiers, tier.name)
			}
		}
		if len(snapshot.DefaultedTiers) > 0 {
			if len(snapshot.DefaultedTiers) == 3 {
				snapshot.PricingSource = "builtin_default"
			} else {
				snapshot.PricingSource = "mixed"
			}
			out.PricingSource = snapshot.PricingSource
			out.DefaultedTiers = append([]string(nil), snapshot.DefaultedTiers...)
		}
	}

	effectiveRate := key.SourceRateMultiplier
	if snapshot.ImageRateIndependent {
		effectiveRate = snapshot.ImageRateMultiplier
	}
	if effectiveRate == nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
		return out
	}
	normalizedRate, err := NormalizeUpstreamActualRate(*effectiveRate, config.RechargeRate)
	if err != nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
		return out
	}
	value := normalizedRate
	out.EffectiveRateMultiplier = &value
	out.FinalCost1K = multiplyImagePrice(snapshot.ImagePrice1K, normalizedRate)
	out.FinalCost2K = multiplyImagePrice(snapshot.ImagePrice2K, normalizedRate)
	out.FinalCost4K = multiplyImagePrice(snapshot.ImagePrice4K, normalizedRate)
	if out.FinalCost1K == nil || out.FinalCost2K == nil || out.FinalCost4K == nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
	} else if snapshot.PricingSource != "" && snapshot.PricingSource != "upstream" {
		out.Status = UpstreamKeyImagePricingStatusPartial
	} else {
		out.Status = UpstreamKeyImagePricingStatusAvailable
	}
	return out
}

func deriveUpstreamKeyVideoPricing(key *UpstreamKey, config *UpstreamConfig) *UpstreamKeyVideoPricing {
	if key == nil || config == nil {
		return nil
	}
	snapshot, ok := parseSub2APIVideoPricingSnapshot(key.Extra)
	if !ok {
		return &UpstreamKeyVideoPricing{Status: UpstreamKeyImagePricingStatusUnavailable}
	}
	out := &UpstreamKeyVideoPricing{Supported: snapshot.AllowVideoGeneration, Status: snapshot.Status, Stale: snapshot.Stale, RateIndependent: snapshot.VideoRateIndependent, ObservedAt: snapshot.ObservedAt}
	if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
		out.Stale = true
	}
	if !snapshot.AllowVideoGeneration || snapshot.Status == UpstreamKeyImagePricingStatusDisabled || snapshot.Status == UpstreamKeyImagePricingStatusUnavailable {
		return out
	}
	effectiveRate := key.SourceRateMultiplier
	if snapshot.VideoRateIndependent {
		effectiveRate = snapshot.VideoRateMultiplier
	}
	if effectiveRate == nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
		return out
	}
	normalizedRate, err := NormalizeUpstreamActualRate(*effectiveRate, config.RechargeRate)
	if err != nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
		return out
	}
	out.EffectiveRateMultiplier = &normalizedRate
	out.FinalCost480p = multiplyImagePrice(snapshot.VideoPrice480p, normalizedRate)
	out.FinalCost720p = multiplyImagePrice(snapshot.VideoPrice720p, normalizedRate)
	out.FinalCost1080p = multiplyImagePrice(snapshot.VideoPrice1080p, normalizedRate)
	if out.FinalCost480p == nil && out.FinalCost720p == nil && out.FinalCost1080p == nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
	}
	return out
}

func deriveUpstreamLongContextState(key *UpstreamKey, config *UpstreamConfig) *UpstreamLongContextState {
	if key == nil || config == nil {
		return nil
	}
	snapshot, ok := parseSub2APILongContextSnapshot(key.Extra)
	if !ok {
		return &UpstreamLongContextState{Status: "unknown", Source: "legacy"}
	}
	out := &UpstreamLongContextState{Enabled: snapshot.Enabled, Status: snapshot.Status, Stale: snapshot.Stale, Source: "upstream_key", ObservedAt: snapshot.ObservedAt}
	if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
		out.Stale = true
	}
	return out
}

// DeriveUpstreamKeyImagePricingForAccount exposes the redacted pricing snapshot
// calculation to repository hydration without exposing raw key material.
func DeriveUpstreamKeyImagePricingForAccount(key *UpstreamKey, config *UpstreamConfig) *UpstreamKeyImagePricing {
	return deriveUpstreamKeyImagePricing(key, config)
}

func DeriveUpstreamKeyVideoPricingForAccount(key *UpstreamKey, config *UpstreamConfig) *UpstreamKeyVideoPricing {
	return deriveUpstreamKeyVideoPricing(key, config)
}

func DeriveUpstreamLongContextForAccount(key *UpstreamKey, config *UpstreamConfig) *UpstreamLongContextState {
	return deriveUpstreamLongContextState(key, config)
}

func multiplyImagePrice(price *float64, rate float64) *float64 {
	if price == nil || *price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0) {
		return nil
	}
	value := *price * rate
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func hydrateUpstreamConfigImagePricing(config *UpstreamConfig) {
	if config == nil || (!strings.EqualFold(strings.TrimSpace(config.Provider), UpstreamProviderSub2API) && !strings.EqualFold(strings.TrimSpace(config.Provider), UpstreamProviderLCodex)) {
		return
	}
	for _, key := range config.Keys {
		if key != nil {
			key.ImagePricing = deriveUpstreamKeyImagePricing(key, config)
		}
	}
}

func hydrateUpstreamKeysImagePricing(keys []UpstreamKey, config *UpstreamConfig) {
	if config == nil || (!strings.EqualFold(strings.TrimSpace(config.Provider), UpstreamProviderSub2API) && !strings.EqualFold(strings.TrimSpace(config.Provider), UpstreamProviderLCodex)) {
		return
	}
	for i := range keys {
		keys[i].ImagePricing = deriveUpstreamKeyImagePricing(&keys[i], config)
	}
}
