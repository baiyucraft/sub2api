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
