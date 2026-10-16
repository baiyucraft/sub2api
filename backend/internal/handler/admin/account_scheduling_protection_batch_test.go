package admin

import (
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type pagePolicyCounter struct {
	policies accountProtectionPolicyStub
	reads    int
}

func (p *pagePolicyCounter) ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy {
	p.reads++
	return p.policies
}

type pageTTFTCounter struct {
	accountProtectionTTFTStub
	batches, singles int
}

func (r *pageTTFTCounter) TTFTProtectionSnapshots(accountID, keyID, groupID int64, config service.OpenAITTFTGuardConfigSnapshot) []service.TTFTProtectionModelView {
	r.singles++
	return r.accountProtectionTTFTStub.TTFTProtectionSnapshots(accountID, keyID, groupID, config)
}
func (r *pageTTFTCounter) TTFTProtectionSnapshotsBatch(requests []service.TTFTProtectionSnapshotRequest) [][]service.TTFTProtectionModelView {
	r.batches++
	result := make([][]service.TTFTProtectionModelView, len(requests))
	for i, request := range requests {
		result[i] = r.accountProtectionTTFTStub.TTFTProtectionSnapshots(request.AccountID, request.KeyID, request.GroupID, request.Config)
	}
	return result
}

func TestAccountProtectionPageBatchMatchesSingleViews(t *testing.T) {
	policy := &pagePolicyCounter{policies: accountProtectionPolicyStub{1: {GroupID: 1, TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true, Threshold: 2 * time.Second, MinSamples: 5}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect"}}}}
	reader := &pageTTFTCounter{accountProtectionTTFTStub: accountProtectionTTFTStub{states: map[int64][]service.TTFTProtectionModelView{0: {{Model: "model", State: "observed", SampleCount: 9}}, 1: {{Model: "model", State: "draining", SampleCount: 9}}}}}
	h := &AccountHandler{ttftGuardReader: reader, protectionPolicyReader: policy}
	upstream, key := int64(4), int64(7)
	accounts := []service.Account{{ID: 1, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstream, UpstreamKeyID: &key, GroupIDs: []int64{1, 1, 2}}, {ID: 2, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstream, UpstreamKeyID: &key, GroupIDs: []int64{1}}}
	caches := map[int64]service.UpstreamCacheGuardAccountView{1: {Summary: &service.UpstreamCacheGuardSummary{State: "protected"}}}
	want := map[int64]*AccountUpstreamProtection{}
	for i := range accounts {
		want[accounts[i].ID] = h.upstreamProtection(&accounts[i], caches[accounts[i].ID])
	}
	policy.reads, reader.singles = 0, 0
	got := h.upstreamProtectionsBatch(accounts, caches)
	require.Equal(t, want, got)
	require.Equal(t, 1, policy.reads)
	require.Equal(t, 1, reader.batches)
	require.Zero(t, reader.singles)
}

type pageCopyingPolicy accountProtectionPolicyStub

func (p pageCopyingPolicy) ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy {
	return maps.Clone(p)
}

func BenchmarkAccountProtectionPagePolicies(b *testing.B) {
	for _, size := range []int{20, 100} {
		accounts := make([]service.Account, size)
		upstream, key := int64(4), int64(7)
		policies := accountProtectionPolicyStub{}
		for group := int64(1); group <= 100; group++ {
			policies[group] = service.GroupSchedulingProtectionResolvedPolicy{GroupID: group, Cache: service.UpstreamCacheGuardSettings{Mode: "disabled"}}
		}
		for i := range accounts {
			accounts[i] = service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, UpstreamConfigID: &upstream, UpstreamKeyID: &key, GroupIDs: []int64{1, 2}}
		}
		h := &AccountHandler{protectionPolicyReader: pageCopyingPolicy(policies), ttftGuardReader: &pageTTFTCounter{}}
		b.Run(fmt.Sprintf("page%d_legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				for i := range accounts {
					_ = h.upstreamProtection(&accounts[i], service.UpstreamCacheGuardAccountView{})
				}
			}
		})
		b.Run(fmt.Sprintf("page%d_batch", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = h.upstreamProtectionsBatch(accounts, nil)
			}
		})
	}
}
