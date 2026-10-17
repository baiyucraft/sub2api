package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTFTProtectionSnapshotsExpiredSamplesAreInsufficient(t *testing.T) {
	for _, initial := range []struct {
		state string
		ms    int
	}{{"normal", 1000}, {"protected", 60000}} {
		t.Run(initial.state, func(t *testing.T) {
			now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
			g := newOpenAITTFTGuardWithOptions(16, openAITTFTGuardEntryTTL, func() time.Time { return now })
			svc := &OpenAIGatewayService{openaiTTFTGuard: g}
			cfg := OpenAITTFTGuardConfigSnapshot{Enabled: true, Threshold: 20 * time.Second, MinSamples: 2, Source: "group"}
			for i := 0; i < 2; i++ {
				g.reportForKey(100, 1, 10, "gpt-test", true, &initial.ms, cfg)
			}
			requests := []TTFTProtectionSnapshotRequest{
				{AccountID: 1, KeyID: 10, GroupID: 100, Config: cfg},
				{AccountID: 1, KeyID: 10},
			}
			before := svc.TTFTProtectionSnapshotsBatch(requests)
			require.Len(t, before[0], 1)
			require.Equal(t, initial.state, before[0][0].State)
			require.Equal(t, "observed", before[1][0].State)

			now = now.Add(openAITTFTGuardEntryTTL - time.Nanosecond)
			require.Equal(t, before, svc.TTFTProtectionSnapshotsBatch(requests), "reads cannot extend sample validity")
			now = now.Add(time.Nanosecond)
			for _, models := range svc.TTFTProtectionSnapshotsBatch(requests) {
				require.Len(t, models, 1)
				require.Equal(t, "insufficient", models[0].State, "expired policy and shared observations have no valid samples")
				require.Zero(t, models[0].SampleCount, "expired latency is not a valid sample")
				require.True(t, models[0].LastSampleAt.IsZero())
				require.True(t, models[0].ExpiresAt.IsZero())
			}

			fast := 1000
			g.reportForKey(100, 1, 10, "gpt-test", true, &fast, cfg)
			fresh := svc.TTFTProtectionSnapshotsBatch(requests)[0][0]
			require.Equal(t, "insufficient", fresh.State)
			require.EqualValues(t, 1, fresh.SampleCount, "expired samples cannot satisfy the minimum")
			g.reportForKey(100, 1, 10, "gpt-test", true, &fast, cfg)
			want := "normal"
			if initial.state == "protected" {
				want = "insufficient"
			}
			require.Equal(t, want, svc.TTFTProtectionSnapshotsBatch(requests)[0][0].State)
		})
	}
}
