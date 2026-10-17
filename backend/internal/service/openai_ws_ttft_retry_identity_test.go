package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSCurrentTurnRetryPreservesLogicalRequestAndReliableSession(t *testing.T) {
	svc, groupID := &OpenAIGatewayService{}, int64(22)
	seed := WithUpstreamCacheGuardRequestSession(context.Background(), 7, nil, []byte(`{"prompt_cache_key":"conversation-a"}`))
	seed = WithOpenAITTFTGuardAttempt(seed, svc, 7, &groupID)
	turns := newUpstreamCacheGuardWSTurns(seed)
	first := turns.turnContext(1, []byte(`{"type":"response.create","model":"model-a"}`))
	later := turns.turnContext(2, []byte(`{"type":"response.create","model":"model-b","prompt_cache_key":"conversation-b"}`))
	firstAttempt := first.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	failedAttempt := later.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	require.NotEqual(t, firstAttempt.logicalRequestID, failedAttempt.logicalRequestID)
	require.NotEqual(t, firstAttempt.sessionID, failedAttempt.sessionID)

	err := newOpenAIWSCurrentTurnFailoverError(fmt.Errorf("capacity"), []byte(`{"model":"replacement-model"}`), later)
	restored := ContextWithOpenAIWSCurrentTurnRetryIdentity(seed, fmt.Errorf("wrapped: %w", err), 7)
	// A replacement bridge's first turn creates another physical attempt while
	// retaining the later turn's identity, including repeated replacements.
	restored = WithOpenAITTFTGuardAttempt(restored, svc, 7, &groupID)
	replacement := newUpstreamCacheGuardWSTurns(restored).turnContext(1, []byte(`{"type":"response.create","model":"replacement-model"}`))
	retried := replacement.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	require.Equal(t, failedAttempt.logicalRequestID, retried.logicalRequestID)
	require.Equal(t, failedAttempt.sessionID, retried.sessionID)
	require.NotEqual(t, failedAttempt.attemptID, retried.attemptID)
	require.Same(t, later.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget, replacement.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget)
	secondError := newOpenAIWSCurrentTurnFailoverError(fmt.Errorf("retry again"), nil, replacement)
	again := WithOpenAITTFTGuardAttempt(ContextWithOpenAIWSCurrentTurnRetryIdentity(seed, secondError, 7), svc, 7, &groupID)
	require.Equal(t, failedAttempt.logicalRequestID, again.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt).logicalRequestID)
	require.Equal(t, failedAttempt.sessionID, again.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt).sessionID)
}

func TestOpenAIWSCurrentTurnRetryCannotInventOrImportAnotherKeysSession(t *testing.T) {
	svc, groupID := &OpenAIGatewayService{}, int64(22)
	seed := WithOpenAITTFTGuardAttempt(WithUpstreamCacheGuardScheduling(context.Background(), 7), svc, 7, &groupID)
	later := newUpstreamCacheGuardWSTurns(seed).turnContext(2, []byte(`{"type":"response.create","model":"model-a"}`))
	failed := later.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	require.Empty(t, failed.sessionID)
	err := newOpenAIWSCurrentTurnFailoverError(fmt.Errorf("capacity"), nil, later)
	// Even if the outer context still has a prior marker, missing reliable
	// identity in the failed turn stays missing across account/model migration.
	destination := WithUpstreamCacheGuardLogicalSession(context.Background(), 7, "explicit:old-conversation")
	restored := WithOpenAITTFTGuardAttempt(ContextWithOpenAIWSCurrentTurnRetryIdentity(destination, err, 7), svc, 7, &groupID)
	require.Empty(t, restored.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt).sessionID)
	require.Equal(t, failed.logicalRequestID, restored.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt).logicalRequestID)
	otherKey := WithUpstreamCacheGuardLogicalSession(context.Background(), 8, "explicit:other-key")
	require.Same(t, otherKey, ContextWithOpenAIWSCurrentTurnRetryIdentity(otherKey, err, 8))
	require.Same(t, destination, ContextWithOpenAIWSCurrentTurnRetryIdentity(destination, newOpenAIWSCurrentTurnFailoverError(fmt.Errorf("legacy"), nil), 7))
}
