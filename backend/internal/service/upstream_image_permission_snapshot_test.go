//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamImagePermissionSnapshotRetainsDeniedUntilAllowed(t *testing.T) {
	for _, provider := range []string{UpstreamProviderSub2API, UpstreamProviderLCodex} {
		for _, incoming := range []string{"missing", "unavailable", "invalid", "denied", "allowed"} {
			t.Run(provider+"/"+incoming, func(t *testing.T) {
				remoteID := int64(77)
				observed := time.Now().UTC().Add(-48 * time.Hour)
				makeExtra := func(status string, allowed bool, at time.Time) map[string]any {
					if provider == UpstreamProviderLCodex {
						return map[string]any{LCodexImageCapabilitySnapshotExtraKey: lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{
							Version: lcodexImageCapabilitySnapshotVersion, Status: status, AllowImageGeneration: allowed, ObservedAt: &at,
						})}
					}
					return map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
						Version: sub2APIImagePricingSnapshotVersion, Status: status, AllowImageGeneration: allowed, ObservedAt: &at,
					})}
				}
				previous := makeExtra(UpstreamKeyImagePricingStatusDisabled, false, observed)
				repo := &upstreamConfigServiceRepo{keys: []UpstreamKey{{UpstreamConfigID: 9, RemoteKeyID: &remoteID, Extra: previous}}}
				svc := NewUpstreamConfigService(repo, nil, nil)
				extra := map[string]any{"unrelated": "retained"}
				if incoming != "missing" {
					status, allowed := UpstreamKeyImagePricingStatusDisabled, false
					switch incoming {
					case "unavailable":
						status = UpstreamKeyImagePricingStatusUnavailable
					case "invalid":
						status = "invalid"
					case "allowed":
						status, allowed = UpstreamKeyImagePricingStatusPartial, true
					}
					for key, value := range makeExtra(status, allowed, time.Now().UTC()) {
						extra[key] = value
					}
				}
				cfg := &UpstreamConfig{ID: 9, Provider: provider, RechargeRate: 1}
				snapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &remoteID, Extra: extra}}}
				if provider == UpstreamProviderLCodex {
					require.NoError(t, svc.mergeLCodexImageCapabilitySnapshots(context.Background(), cfg, snapshot))
				} else {
					require.NoError(t, svc.mergeSub2APIImagePricingSnapshots(context.Background(), cfg, snapshot))
				}
				pricing := deriveUpstreamKeyImagePricing(&snapshot.Keys[0], cfg)
				rank := OpenAIImagePermissionDenied
				if incoming == "allowed" {
					rank = OpenAIImagePermissionAllowed
				}
				require.Equal(t, rank, OpenAIImagePermissionRank(&Account{UpstreamImagePricing: pricing}, 86400))
				require.Equal(t, "retained", snapshot.Keys[0].Extra["unrelated"])
				if incoming == "missing" || incoming == "unavailable" || incoming == "invalid" {
					require.True(t, pricing.Stale)
					require.Equal(t, UpstreamKeyImagePricingStatusDisabled, pricing.Status)
					require.WithinDuration(t, observed, *pricing.ObservedAt, time.Second)
				}
				// Snapshot merging never mutates persisted history or manual account policy.
				require.Equal(t, previous, repo.keys[0].Extra)
			})
		}
	}
}

type imagePermissionHistoryFailureRepo struct {
	*upstreamConfigServiceRepo
	err error
}

func (r *imagePermissionHistoryFailureRepo) ListKeys(context.Context, int64) ([]UpstreamKey, error) {
	return nil, r.err
}

func (r *imagePermissionHistoryFailureRepo) ListKeysForMaskedFallback(context.Context, int64, []int64) ([]UpstreamKey, error) {
	return nil, r.err
}

func TestUpstreamImagePermissionSnapshotHistoryReadFailureDoesNotRewriteSnapshot(t *testing.T) {
	for _, provider := range []string{UpstreamProviderSub2API, UpstreamProviderLCodex} {
		t.Run(provider, func(t *testing.T) {
			repo := &imagePermissionHistoryFailureRepo{upstreamConfigServiceRepo: &upstreamConfigServiceRepo{}, err: errors.New("fixture history unavailable")}
			svc := NewUpstreamConfigService(repo, nil, nil)
			remoteID := int64(77)
			extra := map[string]any{"unrelated": "retained"}
			snapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &remoteID, Extra: extra}}}
			cfg := &UpstreamConfig{ID: 9, Provider: provider}
			var err error
			if provider == UpstreamProviderLCodex {
				err = svc.mergeLCodexImageCapabilitySnapshots(context.Background(), cfg, snapshot)
			} else {
				err = svc.mergeSub2APIImagePricingSnapshots(context.Background(), cfg, snapshot)
			}
			require.ErrorIs(t, err, repo.err)
			require.Equal(t, extra, snapshot.Keys[0].Extra)
			require.Len(t, snapshot.Keys[0].Extra, 1)
		})
	}
}
