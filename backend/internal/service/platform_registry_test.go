package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegisteredPlatformCatalogFeedsProbeCatalog(t *testing.T) {
	registered := RegisteredPlatformCatalog()
	probe := DefaultUpstreamProbePlatformCatalog()
	require.Len(t, probe, len(registered))
	for index, descriptor := range registered {
		require.Equal(t, descriptor.ID, probe[index].ID)
		require.Equal(t, descriptor.Label, probe[index].Label)
		require.Equal(t, descriptor.ProbeSupported, probe[index].ProbeSupported)
		require.Equal(t, descriptor.DefaultModels, probe[index].Models)
	}
}

func TestCatalogOnlyPlatformsRemainConcreteButFailProbeSupport(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		require.True(t, IsConcreteRequestPlatform(platform))
		require.False(t, UpstreamProbePlatformSupported(platform))
	}
}

func TestRegisteredPlatformsUsePlatformSpecificModelCatalogs(t *testing.T) {
	tests := []struct {
		platform string
		mustHave []string
		mustNot  []string
	}{
		{PlatformAnthropic, []string{"claude-sonnet-4-6"}, []string{"gpt-5.6", "glm-5.2"}},
		{PlatformKimi, []string{"kimi-k2.5", "moonshot-v1-128k"}, []string{"claude-sonnet-4-6", "glm-5.2"}},
		{PlatformZhipu, []string{"glm-4.6", "glm-5.2", "cogview-3"}, []string{"claude-sonnet-4-6", "deepseek-chat"}},
		{PlatformDeepseek, []string{"deepseek-chat", "deepseek-v4-pro"}, []string{"claude-sonnet-4-6", "glm-5.2"}},
	}
	for _, tc := range tests {
		models := DefaultModelIDsForPlatform(tc.platform)
		for _, model := range tc.mustHave {
			require.Contains(t, models, model, "platform=%s", tc.platform)
		}
		for _, model := range tc.mustNot {
			require.NotContains(t, models, model, "platform=%s", tc.platform)
		}
	}
	require.Empty(t, DefaultModelIDsForPlatform("unknown-platform"))
}
