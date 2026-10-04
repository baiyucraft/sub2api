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

func TestEstimateGatewayInputTokensSystemOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{name: "string state", body: `{"state":"abcdefghijkl"}`, want: 4},
		{name: "arbitrary nested state", body: `{"state":[{"arbitrary":"abcdefghijkl"}]}`, want: 6},
		{name: "nested instructions", body: `{"questions":{"q":{"type":"noul","instructions":{"hint":"abcdefghijkl"}}}}`, want: 5},
		{name: "choice criteria", body: `{"questions":{"q":{"type":"choice","criteria":{"A":"abcdefghijkl"}}}}`, want: 5},
		{name: "score criteria", body: `{"questions":{"q":{"type":"score","criteria":[{"level":"abcdefghijkl"}]}}}`, want: 6},
		{name: "data keys", body: `{"state":{"abcdefghijkl":null}}`, want: 4},
		{name: "question ids", body: `{"questions":{"abcdefghijkl":{"type":"noul"}}}`, want: 4},
		{name: "extension input", body: `{"custom":{"value":"abcdefghijkl"}}`, want: 7},
		{name: "numeric and boolean state", body: `{"state":[123456789012,true,false,null]}`, want: 6},
		{name: "empty input", body: `{"model":"jev-latest","stream":false,"state":" ","questions":{}}`, want: 0},
		{name: "invalid body", body: `{"state":`, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, EstimateGatewayInputTokens([]byte(tc.body), "typesafe_systemone"))
		})
	}
	withMetadata := `{"model":"ignored-model-name","stream":true,"state":"abcdefghijkl","questions":{"q":{"type":"ignored-type","instructions":"mnop"}}}`
	withoutMetadata := `{"state":"abcdefghijkl","questions":{"q":{"instructions":"mnop"}}}`
	require.Equal(t, EstimateGatewayInputTokens([]byte(withoutMetadata), "typesafe_systemone"), EstimateGatewayInputTokens([]byte(withMetadata), "typesafe_systemone"))
	require.Zero(t, EstimateGatewayInputTokens([]byte(`{"state":"abcdefghijkl","criteria":{"A":"mnop"}}`), "responses"), "other protocols retain their extraction contract")
}
