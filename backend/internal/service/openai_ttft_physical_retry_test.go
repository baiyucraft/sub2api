package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A retry performed inside the same forwarding call must create a new
// physical clock while retaining the logical request/session identity.
func TestOpenAITTFTPhysicalRetryRotatesAttemptAndRejectsLateCallback(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	keyID := int64(11)
	account.UpstreamKeyID = &keyID
	ctx := ttftAttemptContext(svc, group, "explicit:retry")

	firstCtx := prepareOpenAITTFTPhysicalAttempt(ctx)
	markOpenAITTFTGuardSend(firstCtx, account, "gpt-test", "openai")
	first := currentOpenAITTFTAttempt(firstCtx)
	first.mu.Lock()
	first.sentAt = time.Now().Add(-2 * time.Second)
	first.firstTokenAt = first.sentAt.Add(700 * time.Millisecond)
	first.mu.Unlock()

	secondCtx := prepareOpenAITTFTPhysicalAttempt(ctx)
	second := currentOpenAITTFTAttempt(secondCtx)
	require.NotEqual(t, first.attemptID, second.attemptID)
	require.Equal(t, first.logicalRequestID, second.logicalRequestID)
	require.Equal(t, first.sessionID, second.sessionID)
	require.Equal(t, openAITTFTGuardAlgorithmVersion, first.algorithmVersion)
	require.Equal(t, openAITTFTGuardAlgorithmVersion, second.algorithmVersion)

	markOpenAITTFTGuardSend(secondCtx, account, "gpt-test", "openai")
	second.mu.Lock()
	second.sentAt = time.Now().Add(-time.Second)
	second.firstTokenAt = second.sentAt.Add(120 * time.Millisecond)
	second.mu.Unlock()
	finishOpenAITTFTGuardAttempt(ctx, &OpenAIForwardResult{}, true)
	// The old transport callback arrives after the retry has completed. Its
	// physical context is already terminal and must not mutate the new attempt.
	finishOpenAITTFTGuardAttempt(firstCtx, &OpenAIForwardResult{FirstTokenMs: func() *int { v := 225000; return &v }()}, true)

	g := svc.getOpenAITTFTGuard()
	g.mu.Lock()
	key, ok := g.sampleKeyLocked(account.ID, keyID, "gpt-test")
	raw := g.observations[key]
	g.mu.Unlock()
	require.True(t, ok)
	require.NotNil(t, raw)
	ids := map[string]bool{}
	for _, fact := range raw.facts {
		ids[fact.AttemptID] = true
	}
	require.Len(t, ids, 2)
	view := svc.TTFTProtectionSnapshots(account.ID, keyID, group, cfg)
	require.Len(t, view, 1)
	require.Equal(t, int64(120), view[0].LastTTFTMs)
}
