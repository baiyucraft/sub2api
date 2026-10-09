package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
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

func TestRegisteredPlatformIdentitySharesDomainCatalogWithoutInheritingProbeSupport(t *testing.T) {
	registered := RegisteredPlatformCatalog()
	platforms := domain.Platforms()
	require.Len(t, registered, len(platforms))
	for i, spec := range platforms {
		require.Equal(t, spec.ID, registered[i].ID)
		require.Equal(t, spec.DisplayName, registered[i].Label)
		require.True(t, IsConcreteRequestPlatform(spec.ID))
	}
	for _, platform := range []string{PlatformCommandCode, PlatformCline} {
		require.False(t, UpstreamProbePlatformSupported(platform))
		for _, entry := range registered {
			if entry.ID == platform {
				require.NotEmpty(t, entry.ProbeReason)
				require.NotEmpty(t, entry.DefaultModels)
			}
		}
	}
}

func TestRegisteredPlatformsExposeActiveProbeSupport(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformGrok} {
		require.True(t, IsConcreteRequestPlatform(platform))
		require.True(t, UpstreamProbePlatformSupported(platform))
	}
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		require.True(t, IsConcreteRequestPlatform(platform))
		require.True(t, UpstreamProbePlatformSupported(platform))
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
		{PlatformMiniMax, []string{"MiniMax-M3", "MiniMax-M2.7"}, []string{"claude-sonnet-4-6", "deepseek-chat"}},
		{PlatformTypeSafe, []string{"jev-latest"}, []string{"claude-sonnet-4-6", "gpt-5.6"}},
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

func TestTypeSafeRegistryDoesNotAdvertiseLLMProbeSupport(t *testing.T) {
	require.True(t, IsConcreteRequestPlatform(PlatformTypeSafe))
	require.False(t, UpstreamProbePlatformSupported(PlatformTypeSafe))
	require.NotContains(t, DefaultModelIDsForPlatform(PlatformComposite), "jev-latest")
}
