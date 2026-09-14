//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPlatformFilteredForSelection_UsesRoutedTargetPlatform(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		UpstreamModelRoutes: []UpstreamKeyModelRoute{
			{
				PublicModel:    "kimi-k2",
				TargetPlatform: PlatformKimi,
				Status:         UpstreamKeyModelRouteStatusAvailable,
				Enabled:        true,
			},
		},
	}

	require.False(t, isPlatformFilteredForSelection(account, PlatformKimi, false),
		"a physical OpenAI account with a Kimi route must remain eligible for Kimi")
	require.True(t, isPlatformFilteredForSelection(account, PlatformDeepseek, false),
		"a routed account without a DeepSeek route must fail closed")
}

func TestIsPlatformFilteredForSelection_LegacyAccountKeepsPhysicalPlatformSemantics(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI}

	require.False(t, isPlatformFilteredForSelection(account, PlatformOpenAI, false))
	require.True(t, isPlatformFilteredForSelection(account, PlatformKimi, false))
}

func TestGatewayServiceIsModelSupportedByAccountWithContext_RoutedModelIsTargetSpecific(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		UpstreamModelRoutes: []UpstreamKeyModelRoute{
			{
				PublicModel:    "kimi-k2",
				TargetPlatform: PlatformKimi,
				Status:         UpstreamKeyModelRouteStatusAvailable,
				Enabled:        true,
			},
		},
	}
	svc := &GatewayService{}

	kimiCtx := WithResolvedTargetPlatform(WithRequestedPublicModel(context.Background(), "kimi-k2"), PlatformKimi)
	deepseekCtx := WithResolvedTargetPlatform(WithRequestedPublicModel(context.Background(), "kimi-k2"), PlatformDeepseek)

	require.True(t, svc.isModelSupportedByAccountWithContext(kimiCtx, account, "kimi-k2"))
	require.False(t, svc.isModelSupportedByAccountWithContext(deepseekCtx, account, "kimi-k2"),
		"a model route must not be reused for another target platform")
}
