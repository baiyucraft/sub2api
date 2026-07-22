package service

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	Sub2APIImagePricingSnapshotExtraKey   = "sub2api_image_pricing_snapshot"
	sub2APIImagePricingSnapshotVersion    = 1
	LCodexImageCapabilitySnapshotExtraKey = "lcodex_image_pricing_snapshot"
	lcodexImageCapabilitySnapshotVersion  = 1

	UpstreamKeyImagePricingStatusAvailable   = "available"
	UpstreamKeyImagePricingStatusPartial     = "partial"
	UpstreamKeyImagePricingStatusDisabled    = "disabled"
	UpstreamKeyImagePricingStatusUnavailable = "unavailable"
)

type UpstreamKeyImagePricing struct {
	Supported   bool
	Status      string
	Stale       bool
	Currency    string
	FinalCost1K *float64
	FinalCost2K *float64
	FinalCost4K *float64
	ObservedAt  *time.Time
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
		Supported:  snapshot.AllowImageGeneration,
		Status:     snapshot.Status,
		Stale:      snapshot.Stale,
		Currency:   "USD",
		ObservedAt: snapshot.ObservedAt,
	}
	if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
		out.Stale = true
	}
	if !snapshot.AllowImageGeneration || snapshot.Status == UpstreamKeyImagePricingStatusDisabled || snapshot.Status == UpstreamKeyImagePricingStatusUnavailable {
		return out
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
	out.FinalCost1K = multiplyImagePrice(snapshot.ImagePrice1K, normalizedRate)
	out.FinalCost2K = multiplyImagePrice(snapshot.ImagePrice2K, normalizedRate)
	out.FinalCost4K = multiplyImagePrice(snapshot.ImagePrice4K, normalizedRate)
	if out.FinalCost1K == nil || out.FinalCost2K == nil || out.FinalCost4K == nil {
		out.Status = UpstreamKeyImagePricingStatusPartial
	} else {
		out.Status = UpstreamKeyImagePricingStatusAvailable
	}
	return out
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
