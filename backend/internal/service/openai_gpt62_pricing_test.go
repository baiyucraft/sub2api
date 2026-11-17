package service

import (
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGPT62PricingFallback(t *testing.T) {
	other := &LiteLLMModelPricing{InputCostPerToken: 99e-6}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.1-codex": other,
	}}
	for _, tt := range []struct {
		model                      string
		input, output, read, write float64
	}{
		{"gpt-6.2-sol", 2e-6, 10e-6, .1e-6, 2.5e-6},
		{"gpt-6.2-astra", 8e-6, 40e-6, .4e-6, 10e-6},
	} {
		t.Run(tt.model, func(t *testing.T) {
			for _, suffix := range []string{"", "-xhigh", "-20261117"} {
				price := svc.GetModelPricing(tt.model + suffix)
				require.NotNil(t, price)
				require.Equal(t, tt.input, price.InputCostPerToken)
				require.Equal(t, tt.output, price.OutputCostPerToken)
				require.Equal(t, tt.read, price.CacheReadInputTokenCost)
				require.Equal(t, tt.write, price.CacheCreationInputTokenCost)
				require.Equal(t, tt.input*2, price.InputCostPerTokenPriority)
				require.Equal(t, tt.output*2, price.OutputCostPerTokenPriority)
				require.Equal(t, 272_000, price.LongContextInputTokenThreshold)
				require.Equal(t, float64(2), price.LongContextInputCostMultiplier)
				require.Equal(t, 1.5, price.LongContextOutputCostMultiplier)
			}
		})
	}
	require.Nil(t, svc.GetModelPricing("gpt-6.2"))
	require.Nil(t, svc.GetModelPricing("gpt-6.2-unknown"))
}

func TestOpenAIGPT62PricingUsesExactCatalogBeforeFallback(t *testing.T) {
	sol := &LiteLLMModelPricing{InputCostPerToken: 7e-6}
	xhigh := &LiteLLMModelPricing{InputCostPerToken: 9e-6}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6.2-sol":       sol,
		"gpt-6.2-sol-xhigh": xhigh,
	}}
	require.Same(t, sol, svc.GetModelPricing("gpt-6.2-sol"))
	require.Same(t, sol, svc.GetModelPricing("gpt-6.2-sol-max"))
	require.Same(t, xhigh, svc.GetModelPricing("gpt-6.2-sol-xhigh"))
}

func TestOpenAIGPT62BundledPricesMatchFallback(t *testing.T) {
	body, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	svc := &PricingService{}
	prices, err := svc.parsePricingData(body)
	require.NoError(t, err)
	for id, fallback := range map[string]*LiteLLMModelPricing{
		"gpt-6.2-sol":   openAIGPT62SolFallbackPricing,
		"gpt-6.2-astra": openAIGPT62AstraFallbackPricing,
	} {
		price := prices[id]
		require.NotNil(t, price, id)
		expected := *fallback
		expected.CacheCreationInputTokenCostExplicit = true
		require.Equal(t, expected, *price, id)
	}
}

func TestOpenAIGPT62PricingChargesCacheTTL(t *testing.T) {
	body, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	parser := &PricingService{}
	prices, err := parser.parsePricingData(body)
	require.NoError(t, err)
	tokens := UsageTokens{CacheCreationTokens: 100, CacheCreation30mTokens: 40, CacheCreation12hTokens: 60}
	for name, source := range map[string]*PricingService{
		"bundled":         {pricingData: prices},
		"missing catalog": {},
		"no service":      nil,
		"explicit zero": {pricingData: map[string]*LiteLLMModelPricing{
			"gpt-6.2-sol": {InputCostPerToken: 2e-6, CacheCreationInputTokenCost: 2.5e-6, CacheCreationInputTokenCost12h: new(0.0)},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			billing := NewBillingService(&config.Config{}, source)
			sol, err := billing.CalculateCostWithServiceTier("gpt-6.2-sol", tokens, 1, "")
			require.NoError(t, err)
			if name == "explicit zero" {
				require.InDelta(t, 0.0001, sol.CacheCreationCost, 1e-12)
				return
			}
			require.InDelta(t, 0.0004, sol.CacheCreationCost, 1e-12)
			astra, err := billing.CalculateCostWithServiceTier("gpt-6.2-astra", tokens, 1, "priority")
			require.NoError(t, err)
			require.InDelta(t, 0.0032, astra.CacheCreationCost, 1e-12)
		})
	}
}
