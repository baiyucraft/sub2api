package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedulerTTFTPolicyResolverStub struct {
	invalidated    []int64
	broadcasted    []int64
	broadcastError error
}

func (s *schedulerTTFTPolicyResolverStub) Resolve(context.Context, int64) (GroupTTFTGuardResolvedPolicy, error) {
	return GroupTTFTGuardResolvedPolicy{}, nil
}

func (s *schedulerTTFTPolicyResolverStub) Invalidate(groupID int64) {
	s.invalidated = append(s.invalidated, groupID)
}

func (s *schedulerTTFTPolicyResolverStub) BroadcastInvalidation(_ context.Context, groupID int64) error {
	s.broadcasted = append(s.broadcasted, groupID)
	return s.broadcastError
}

func TestSchedulerSnapshotOrdinaryGroupChangedDoesNotInvalidateTTFTPolicy(t *testing.T) {
	resolver := &schedulerTTFTPolicyResolverStub{}
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	svc.groupTTFTGuardPolicies = resolver
	groupID := int64(31)

	err := svc.handleOutboxEvent(context.Background(), SchedulerOutboxEvent{
		EventType: SchedulerOutboxEventGroupChanged,
		GroupID:   &groupID,
	}, nil)
	require.NoError(t, err)
	require.Empty(t, resolver.invalidated)
	require.Empty(t, resolver.broadcasted)
}

func TestSchedulerSnapshotTTFTPolicyChangedInvalidatesAndBroadcastsInSimpleMode(t *testing.T) {
	resolver := &schedulerTTFTPolicyResolverStub{}
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	svc.groupTTFTGuardPolicies = resolver
	groupID := int64(31)

	err := svc.handleOutboxEvent(context.Background(), SchedulerOutboxEvent{
		EventType: SchedulerOutboxEventGroupChanged,
		GroupID:   &groupID,
		Payload:   map[string]any{SchedulerOutboxPayloadTTFTGuardPolicyChanged: true},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []int64{31}, resolver.invalidated)
	require.Equal(t, []int64{31}, resolver.broadcasted)
}

func TestSchedulerSnapshotTTFTPolicyBroadcastFailureBlocksEvent(t *testing.T) {
	broadcastErr := errors.New("redis publish failed")
	resolver := &schedulerTTFTPolicyResolverStub{broadcastError: broadcastErr}
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	svc.groupTTFTGuardPolicies = resolver
	groupID := int64(31)

	err := svc.handleOutboxEvent(context.Background(), SchedulerOutboxEvent{
		EventType: SchedulerOutboxEventGroupChanged,
		GroupID:   &groupID,
		Payload:   map[string]any{SchedulerOutboxPayloadTTFTGuardPolicyChanged: true},
	}, nil)
	require.ErrorIs(t, err, broadcastErr)
	require.Equal(t, []int64{31}, resolver.invalidated)
	require.Equal(t, []int64{31}, resolver.broadcasted)
}

func TestSchedulerSnapshotInvalidGroupEventDoesNotInvalidateTTFTPolicy(t *testing.T) {
	resolver := &schedulerTTFTPolicyResolverStub{}
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	svc.groupTTFTGuardPolicies = resolver
	groupID := int64(0)

	err := svc.handleGroupEvent(context.Background(), &groupID, map[string]any{
		SchedulerOutboxPayloadTTFTGuardPolicyChanged: true,
	}, nil)
	require.NoError(t, err)
	require.Empty(t, resolver.invalidated)
	require.Empty(t, resolver.broadcasted)
}
