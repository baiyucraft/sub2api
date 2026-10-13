package upstreamcacheguard

import (
	"reflect"
	"testing"
	"time"
)

type testGroupPolicyResolver map[int64]Settings

func (r testGroupPolicyResolver) ResolveGroupPolicy(groupID int64) (GroupPolicy, bool) {
	s, ok := r[groupID]
	return GroupPolicy{GroupID: groupID, Settings: s}, ok
}

func cascadeTestSettings(threshold float64) Settings {
	s := DefaultSettings()
	s.Mode = Protect
	s.ThresholdPercent = threshold
	s.RecoveryThresholdPercent = threshold + 5
	return s
}

func cascadeTestSnapshot(now time.Time, groupID int64, phase Phase) Snapshot {
	return Snapshot{
		GroupID: groupID, Mode: Protect, Available: true, LoadedAt: now,
		Accounts: []AccountSnapshot{{AccountState: AccountState{
			AccountID: 7, UpstreamKeyID: 9, Phase: phase, ValidRequests: 24,
			CachePercent: 70, LastEvidenceAt: now.Add(-time.Minute), UpdatedAt: now,
		}, Version: 4}},
	}
}

func TestCacheGuardCascadeThresholdDirectionAndStableSource(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	policies := testGroupPolicyResolver{1: cascadeTestSettings(85), 2: cascadeTestSettings(75), 3: cascadeTestSettings(65), 4: cascadeTestSettings(75), 5: cascadeTestSettings(60)}
	snapshots := []Snapshot{cascadeTestSnapshot(now, 4, Cooling), cascadeTestSnapshot(now, 2, Trial), cascadeTestSnapshot(now, 5, Cooling)}
	got := DeriveAccountProtection(1, 7, 9, snapshots, policies, now)
	if !got.Protected || got.Reason != ProtectionReasonThresholdCascade || got.PrimarySource == nil || got.PrimarySource.GroupID != 5 || got.PrimarySource.ThresholdPercent != 60 {
		t.Fatalf("unexpected source selection: %+v", got)
	}
	if len(got.Sources) != 3 || got.Sources[1].GroupID != 2 || got.Sources[2].GroupID != 4 {
		t.Fatalf("source order must be threshold then group ID: %+v", got.Sources)
	}
	for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 {
		snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
	}
	if reversed := DeriveAccountProtection(1, 7, 9, snapshots, policies, now); !reflect.DeepEqual(got, reversed) {
		t.Fatal("snapshot order changed derived protection")
	}
	if equal := DeriveAccountProtection(4, 7, 9, []Snapshot{cascadeTestSnapshot(now, 2, Cooling)}, policies, now); !equal.Protected {
		t.Fatal("equal threshold did not inherit native protection")
	}
	if loose := DeriveAccountProtection(3, 7, 9, []Snapshot{cascadeTestSnapshot(now, 2, Cooling)}, policies, now); loose.Protected {
		t.Fatal("looser threshold inherited a stricter native source")
	}
}

func TestCacheGuardCascadeRequiresCurrentKeyEnabledFreshNativeSource(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	settings := cascadeTestSettings(75)
	resolver := UniformGroupPolicyResolver{Settings: settings}
	for _, tc := range []struct {
		name string
		edit func(*Snapshot)
	}{
		{"healthy", func(s *Snapshot) { s.Accounts[0].Phase = Healthy }},
		{"empty_phase", func(s *Snapshot) { s.Accounts[0].Phase = "" }},
		{"other_account", func(s *Snapshot) { s.Accounts[0].AccountID = 8 }},
		{"rotated_key", func(s *Snapshot) { s.Accounts[0].UpstreamKeyID = 10 }},
		{"unknown_key", func(s *Snapshot) { s.Accounts[0].UpstreamKeyID = 0 }},
		{"same_group", func(s *Snapshot) { s.GroupID = 1 }},
		{"observe_snapshot", func(s *Snapshot) { s.Mode = Observe }},
		{"unavailable", func(s *Snapshot) { s.Available = false }},
		{"sampling_gap", func(s *Snapshot) { s.SamplingGap = true }},
		{"stale_snapshot", func(s *Snapshot) { s.LoadedAt = now.Add(-RefreshLifetime - time.Nanosecond) }},
		{"no_snapshot_clock", func(s *Snapshot) { s.LoadedAt = time.Time{} }},
		{"future_snapshot", func(s *Snapshot) { s.LoadedAt = now.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := cascadeTestSnapshot(now, 2, Cooling)
			tc.edit(&snapshot)
			if got := DeriveAccountProtection(1, 7, 9, []Snapshot{snapshot}, resolver, now); got.Protected || len(got.Sources) != 0 {
				t.Fatalf("ineligible source produced protection: %+v", got)
			}
		})
	}
	settings.Mode = Disabled
	if got := DeriveAccountProtection(1, 7, 9, []Snapshot{cascadeTestSnapshot(now, 2, Cooling)}, UniformGroupPolicyResolver{Settings: settings}, now); got.Protected {
		t.Fatal("disabled policy produced a proposed protection")
	}
	settings.Mode = Protect
	settings.GroupIDs = []int64{1}
	if got := DeriveAccountProtection(1, 7, 9, []Snapshot{cascadeTestSnapshot(now, 2, Cooling)}, UniformGroupPolicyResolver{Settings: settings}, now); got.Protected {
		t.Fatal("source outside enabled groups produced protection")
	}
}

func TestCacheGuardCascadeObserveProducesOnlySameModeView(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	settings := cascadeTestSettings(75)
	settings.Mode = Observe
	source := cascadeTestSnapshot(now, 2, Cooling)
	source.Mode = Observe
	resolver := UniformGroupPolicyResolver{Settings: settings}
	if got := DeriveAccountProtection(1, 7, 9, []Snapshot{source}, resolver, now); !got.Protected || got.PrimarySource == nil || got.PrimarySource.GroupID != 2 {
		t.Fatalf("observe view lost proposed source: %+v", got)
	}
	// Observed degradation is never promoted into another group's active
	// protection, and active protection is not mislabeled as observed evidence.
	for _, tc := range []struct{ targetMode, sourcePolicyMode, sourceSnapshotMode Mode }{
		{Protect, Observe, Observe},
		{Observe, Protect, Protect},
		{Observe, Observe, Protect},
		{Observe, Protect, Observe},
	} {
		targetSettings, sourceSettings := settings, settings
		targetSettings.Mode, sourceSettings.Mode = tc.targetMode, tc.sourcePolicyMode
		source.Mode = tc.sourceSnapshotMode
		policies := testGroupPolicyResolver{1: targetSettings, 2: sourceSettings}
		if got := DeriveAccountProtection(1, 7, 9, []Snapshot{source}, policies, now); got.Protected {
			t.Fatalf("mixed modes produced protection: %+v", tc)
		}
	}
}

func TestCacheGuardCascadeKeepsNativeEvidenceAndRecoveryOwnership(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	resolver := UniformGroupPolicyResolver{Settings: cascadeTestSettings(75)}
	target := cascadeTestSnapshot(now, 1, Healthy)
	target.Accounts[0].ValidRequests = 1000
	target.Accounts[0].CachePercent = 99
	target.Accounts[0].TrialValidRequests = 99
	target.Accounts[0].TrialSessions = 20
	target.Accounts[0].TrialCachePercent = 99
	target.Accounts[0].CohortID = "target-old-cohort"
	source := cascadeTestSnapshot(now, 2, Trial)
	source.Accounts[0].CohortID = "native-source-cohort"
	source.Accounts[0].FirstTrialAt = now.Add(-2 * time.Minute)
	source.Accounts[0].TrialValidRequests = 4
	source.Accounts[0].TrialSessions = 2
	source.Accounts[0].TrialCachePercent = 76
	other := cascadeTestSnapshot(now, 3, Cooling)
	snapshots := []Snapshot{target, source, other}
	before := make([]Snapshot, len(snapshots))
	for i, snapshot := range snapshots {
		before[i] = snapshot
		before[i].Accounts = append([]AccountSnapshot(nil), snapshot.Accounts...)
	}
	got := DeriveAccountProtection(1, 7, 9, snapshots, resolver, now)
	if !got.Protected || got.PrimarySource == nil || got.PrimarySource.CohortID != "native-source-cohort" || got.PrimarySource.TrialValidRequests != 4 || got.PrimarySource.TrialCachePercent != 76 {
		t.Fatalf("target evidence changed source recovery ownership: %+v", got)
	}
	if !reflect.DeepEqual(snapshots, before) {
		t.Fatal("read-only derivation mutated native snapshots")
	}
	// Even a window with no remaining samples cannot clear native protection.
	snapshots[1].Accounts[0].ValidRequests = 0
	snapshots[1].Accounts[0].CachePercent = 0
	snapshots[1].Accounts[0].LastEvidenceAt = now.Add(-2 * time.Hour)
	if expired := DeriveAccountProtection(1, 7, 9, snapshots, resolver, now); !expired.Protected || len(expired.Sources) != 2 {
		t.Fatal("window expiry silently restored the source")
	}
	// Native recovery removes one cause, not unrelated protection.
	snapshots[1].Accounts[0].Phase = Healthy
	if partial := DeriveAccountProtection(1, 7, 9, snapshots, resolver, now); !partial.Protected || len(partial.Sources) != 1 || partial.PrimarySource.GroupID != 3 {
		t.Fatalf("one recovered source cleared another: %+v", partial)
	}
	snapshots[2].Accounts[0].Phase = Healthy
	if recovered := DeriveAccountProtection(1, 7, 9, snapshots, resolver, now); recovered.Protected || recovered.PrimarySource != nil || len(recovered.Sources) != 0 {
		t.Fatalf("all recovered sources still protect target: %+v", recovered)
	}
}

func TestCacheGuardCascadeNativeGroupsRetainRecoveryOwnership(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	resolver := UniformGroupPolicyResolver{Settings: cascadeTestSettings(75)}
	snapshots := []Snapshot{cascadeTestSnapshot(now, 1, Trial), cascadeTestSnapshot(now, 2, Trial)}
	for _, groupID := range []int64{1, 2} {
		derived := DeriveAccountProtection(groupID, 7, 9, snapshots, resolver, now)
		if !derived.Protected || len(derived.Sources) != 1 || derived.PrimarySource.GroupID == groupID {
			t.Fatalf("group %d has invalid external sources: %+v", groupID, derived)
		}
		if snapshots[groupID-1].Accounts[0].Phase != Trial {
			t.Fatal("derivation replaced a native trial phase")
		}
	}
	policy, ok := resolver.ResolveGroupPolicy(1)
	if !ok || policy.Settings.Mode != Protect || policy.GroupID != 1 {
		t.Fatal("uniform resolver changed native group policy")
	}
}
