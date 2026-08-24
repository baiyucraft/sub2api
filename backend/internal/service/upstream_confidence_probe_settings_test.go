package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetUpstreamConfidenceProbeSettingsStateDistinguishesConfiguration(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		repoErr    error
		wantStored bool
		wantEnable bool
		wantErr    bool
	}{
		{name: "missing"},
		{name: "empty", raw: "   "},
		{name: "enabled", raw: "{\"enabled\":true}", wantStored: true, wantEnable: true},
		{name: "disabled", raw: "{\"enabled\":false}", wantStored: true},
		{name: "invalid json", raw: "{", wantErr: true},
		{name: "read error", repoErr: errors.New("db unavailable"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
			if tc.raw != "" {
				repo.values[SettingKeyUpstreamConfidenceProbe] = tc.raw
			}
			repo.getValueErr = tc.repoErr
			settings, stored, err := NewSettingService(repo, nil).GetUpstreamConfidenceProbeSettingsState(context.Background())
			require.Equal(t, tc.wantStored, stored)
			require.Equal(t, tc.wantEnable, settings.Enabled)
			require.Equal(t, tc.wantErr, err != nil)
		})
	}
}

func TestNormalizeUpstreamConfidenceProbeSettingsForcesJuiceHigh(t *testing.T) {
	value, err := normalizeUpstreamConfidenceProbeSettings(UpstreamConfidenceProbeSettings{
		Enabled: true, ReasoningEffort: "low", PromptVersion: "legacy-v1",
		LongContextEnabled: true, LongContextMaxTokens: 8192,
	})
	require.NoError(t, err)
	require.Equal(t, UpstreamConfidenceDefaultEffort, value.ReasoningEffort)
	require.Equal(t, UpstreamConfidencePromptVersion, value.PromptVersion)
	require.False(t, value.LongContextEnabled)
	require.Equal(t, DefaultUpstreamConfidenceProbeSettings().LongContextMaxTokens, value.LongContextMaxTokens)
}
