package upstreamcacheguard

import (
	"testing"
	"time"
)

func TestAccountWindowApplyStableTimestampAndKnownZero(t *testing.T) {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	a := AccountState{AccountID: 2, UpstreamKeyID: 5, Phase: Cooling, UpdatedAt: start,
		GroupIDs: []int64{9}, TrialGroupIDs: []int64{9}, LastEvidenceAt: start}
	w := AccountWindow{AccountID: 2, UpstreamKeyID: 5, ValidRequests: 2,
		TotalInputTokens: 100, CacheReadTokens: 0, GroupIDs: []int64{9, 10},
		TrialGroupIDs: []int64{10}, LastEvidenceAt: start.Add(time.Second)}
	w.Apply(&a, start.Add(time.Minute))
	if a.CachePercent != 0 || a.ValidRequests != 2 || a.Phase != Cooling || !a.UpdatedAt.Equal(start.Add(time.Minute)) ||
		len(a.GroupIDs) != 2 || len(a.TrialGroupIDs) != 2 {
		t.Fatalf("incorrect projected window: %+v", a)
	}
	w.Apply(&a, start.Add(2*time.Minute))
	if !a.UpdatedAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("unchanged window should not invalidate ETag: %v", a.UpdatedAt)
	}
	(AccountWindow{}).Apply(&a, start.Add(3*time.Minute))
	if a.ValidRequests != 0 || a.TotalInputTokens != 0 || a.CachePercent != 0 ||
		a.LastEvidenceAt != w.LastEvidenceAt || a.Phase != Cooling || len(a.GroupIDs) != 2 {
		t.Fatalf("expiry must clear metrics without restoring protection: %+v", a)
	}
}
