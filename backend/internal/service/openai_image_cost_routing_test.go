package service

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func imageCostTestCandidate(id int64, cost *float64, status string, stale bool) openAIAccountCandidateScore {
	return openAIAccountCandidateScore{account: &Account{ID: id, UpstreamImagePricing: &UpstreamKeyImagePricing{
		Supported: true, Status: status, Stale: stale, FinalCost2K: cost,
	}}}
}

func stableIDOrder(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	out := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].account.ID < out[j].account.ID })
	return out
}

func TestBuildImageCostSelectionOrderZeroAndUnknown(t *testing.T) {
	zero, cheap := 0.0, 0.02
	ordered := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "prefer_lowest", ImageCostTolerancePercent: 5}, []openAIAccountCandidateScore{
		imageCostTestCandidate(3, nil, UpstreamKeyImagePricingStatusUnavailable, false),
		imageCostTestCandidate(2, &cheap, UpstreamKeyImagePricingStatusAvailable, false),
		imageCostTestCandidate(1, &zero, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, []int64{1, 2, 3}, []int64{ordered[0].account.ID, ordered[1].account.ID, ordered[2].account.ID})
}

func TestBuildImageCostSelectionOrderFreshBeforePartialAndStale(t *testing.T) {
	low, high := 0.01, 0.02
	observed := time.Now().Add(-2 * time.Hour)
	staleCandidate := imageCostTestCandidate(1, &low, UpstreamKeyImagePricingStatusAvailable, false)
	staleCandidate.account.UpstreamImagePricing.ObservedAt = &observed
	ordered := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "prefer_lowest", ImageCostTolerancePercent: 5, ImageCostStaleAfterSeconds: 300}, []openAIAccountCandidateScore{
		staleCandidate,
		imageCostTestCandidate(2, &low, UpstreamKeyImagePricingStatusPartial, false),
		imageCostTestCandidate(3, &high, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, []int64{3, 2, 1}, []int64{ordered[0].account.ID, ordered[1].account.ID, ordered[2].account.ID})
}

func TestBuildImageCostSelectionOrderToleranceKeepsExistingOrder(t *testing.T) {
	a, b := 0.0200, 0.0208
	ordered := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "prefer_lowest", ImageCostTolerancePercent: 5}, []openAIAccountCandidateScore{
		imageCostTestCandidate(2, &a, UpstreamKeyImagePricingStatusAvailable, false),
		imageCostTestCandidate(1, &b, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, []int64{1, 2}, []int64{ordered[0].account.ID, ordered[1].account.ID})

	strict := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "strict_lowest", ImageCostTolerancePercent: 5}, []openAIAccountCandidateScore{
		imageCostTestCandidate(2, &a, UpstreamKeyImagePricingStatusAvailable, false),
		imageCostTestCandidate(1, &b, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, []int64{2, 1}, []int64{strict[0].account.ID, strict[1].account.ID})
}

func TestBuildImageCostSelectionOrderUsesLowestValueAsToleranceAnchor(t *testing.T) {
	a, b, c := 0.0200, 0.0210, 0.0220
	ordered := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "prefer_lowest", ImageCostTolerancePercent: 5}, []openAIAccountCandidateScore{
		imageCostTestCandidate(3, &c, UpstreamKeyImagePricingStatusAvailable, false),
		imageCostTestCandidate(2, &b, UpstreamKeyImagePricingStatusAvailable, false),
		imageCostTestCandidate(1, &a, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, []int64{1, 2, 3}, []int64{ordered[0].account.ID, ordered[1].account.ID, ordered[2].account.ID})
	require.Equal(t, 1, ordered[0].imageCostRank)
	require.Equal(t, 1, ordered[1].imageCostRank)
	require.Equal(t, 2, ordered[2].imageCostRank)
}

func TestImageCostCandidateMetadataUsesRequestedTier(t *testing.T) {
	one, two, four := 0.01, 0.02, 0.04
	account := &Account{UpstreamImagePricing: &UpstreamKeyImagePricing{
		Supported:   true,
		Status:      UpstreamKeyImagePricingStatusAvailable,
		FinalCost1K: &one,
		FinalCost2K: &two,
		FinalCost4K: &four,
	}}
	for _, tc := range []struct {
		tier string
		want float64
	}{{"1K", one}, {"2K", two}, {"4K", four}} {
		known, value, _, _, status := imageCostCandidateMetadata(OpenAIAccountScheduleRequest{ImageSizeTier: tc.tier}, account)
		require.True(t, known)
		require.Equal(t, tc.want, value)
		require.Equal(t, "available", status)
	}
}

func TestBuildImageCostSelectionOrderUnknownFallsBackWithoutTreatingAsFree(t *testing.T) {
	free := 0.0
	ordered := buildImageCostSelectionOrder(OpenAIAccountScheduleRequest{ImageSizeTier: "2K", ImageCostRoutingMode: "prefer_lowest", ImageCostTolerancePercent: 5}, []openAIAccountCandidateScore{
		imageCostTestCandidate(1, nil, UpstreamKeyImagePricingStatusUnavailable, false),
		imageCostTestCandidate(2, &free, UpstreamKeyImagePricingStatusAvailable, false),
	}, stableIDOrder)
	require.Equal(t, int64(2), ordered[0].account.ID)
	require.True(t, ordered[0].imageCostKnown)
	require.False(t, ordered[1].imageCostKnown)
	require.Equal(t, "unknown", ordered[1].imageCostStatus)
}

func TestImageCostRoutingRejectsExplicitlyUnsupportedKeySnapshot(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	account := &Account{
		ID:       10,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		UpstreamImagePricing: &UpstreamKeyImagePricing{
			Supported: false,
			Status:    UpstreamKeyImagePricingStatusDisabled,
		},
	}
	compatible, reason := scheduler.isAccountRequestCompatibleReason(context.Background(), account, OpenAIAccountScheduleRequest{
		RequiredImageCapability: OpenAIImagesCapabilityBasic,
		ImageCostRoutingMode:    "prefer_lowest",
	})
	require.False(t, compatible)
	require.Equal(t, "image_capability_snapshot", reason)

	account.UpstreamImagePricing = nil
	compatible, _ = scheduler.isAccountRequestCompatibleReason(context.Background(), account, OpenAIAccountScheduleRequest{
		RequiredImageCapability: OpenAIImagesCapabilityBasic,
		ImageCostRoutingMode:    "prefer_lowest",
	})
	require.True(t, compatible, "unknown snapshots stay available as final fallback")

	account.UpstreamImagePricing = &UpstreamKeyImagePricing{
		Supported: false,
		Status:    UpstreamKeyImagePricingStatusUnavailable,
	}
	compatible, _ = scheduler.isAccountRequestCompatibleReason(context.Background(), account, OpenAIAccountScheduleRequest{
		RequiredImageCapability: OpenAIImagesCapabilityBasic,
		ImageCostRoutingMode:    "prefer_lowest",
	})
	require.True(t, compatible, "unavailable pricing is unknown capability, not an explicit disable")
}
