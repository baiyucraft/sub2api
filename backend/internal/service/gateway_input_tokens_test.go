package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateGatewayInputTokensExtractsSupportedRequestShapes(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantAtLeast int
	}{
		{name: "chat messages", body: `{"messages":[{"role":"user","content":"abcdefghijkl"}]}`, wantAtLeast: 3},
		{name: "responses input", body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"abcdefghijkl"}]}],"instructions":"system instructions"}`, wantAtLeast: 6},
		{name: "anthropic system", body: `{"system":"abcdefghijkl","messages":[{"role":"user","content":"mnop"}]}`, wantAtLeast: 4},
		{name: "gemini contents", body: `{"systemInstruction":{"parts":[{"text":"abcdefghijkl"}]},"contents":[{"parts":[{"text":"mnop"}]}]}`, wantAtLeast: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.GreaterOrEqual(t, EstimateGatewayInputTokens([]byte(tt.body), tt.name), tt.wantAtLeast)
		})
	}
}

func TestEstimateGatewayInputTokensHandlesEmptyAndMalformedBodies(t *testing.T) {
	require.Equal(t, 0, EstimateGatewayInputTokens(nil, "chat_completions"))
	require.Equal(t, 0, EstimateGatewayInputTokens([]byte(`{"model":"gpt-5.5"}`), "responses"))
	require.Equal(t, 0, EstimateGatewayInputTokens([]byte(`{"messages":`), "chat_completions"))
	require.Equal(t, 1, EstimateGatewayInputTokens([]byte(`{"prompt":"a"}`), "chat_completions"))
}

func TestAccountInputLengthEligibility(t *testing.T) {
	account := &Account{ProbeMinInputTokens: 10}

	require.True(t, IsAccountInputLengthEligible(context.Background(), account), "missing estimates remain compatible")
	require.True(t, IsAccountInputLengthEligible(WithGatewayInputTokenEstimate(context.Background(), 10), account))
	require.False(t, IsAccountInputLengthEligible(WithGatewayInputTokenEstimate(context.Background(), 9), account))
	require.Equal(t, "input_too_short", AccountInputLengthFailureReason(WithGatewayInputTokenEstimate(context.Background(), 9), account))
	require.True(t, IsAccountInputLengthEligible(WithGatewayInputTokenEstimate(context.Background(), 1), &Account{}))
}
