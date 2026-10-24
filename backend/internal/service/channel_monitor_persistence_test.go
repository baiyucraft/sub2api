//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func persistMonitorFailureForTest(ctx context.Context, repo *unknownMonitorRepo) {
	svc := NewChannelMonitorService(repo, failingMonitorDecryptor{})
	svc.persistCheckResultsIfAllowed(ctx, &ChannelMonitor{ID: 46, Name: "persistence-probe"}, []*CheckResult{
		{Model: "gpt-test", Status: MonitorStatusError, Message: "request timeout", CheckedAt: time.Now()},
	})
}

func waitForMonitorPersistenceSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for monitor persistence")
	}
}

func TestPersistCheckResults_ProbeDeadlineStillPersistsWithIndependentBudget(t *testing.T) {
	taskCtx, cancelTask := context.WithCancel(context.Background())
	t.Cleanup(cancelTask)
	probeCtx, cancelProbe := context.WithDeadline(
		withChannelMonitorRunnerTaskContext(taskCtx, taskCtx), time.Now().Add(-time.Second),
	)
	t.Cleanup(cancelProbe)
	require.ErrorIs(t, probeCtx.Err(), context.DeadlineExceeded)

	var insertCtxErr error
	var insertDeadline time.Time
	var hasDeadline bool
	repo := &unknownMonitorRepo{
		beforeInsert: func(ctx context.Context) error {
			insertCtxErr = ctx.Err()
			insertDeadline, hasDeadline = ctx.Deadline()
			return insertCtxErr
		},
	}
	started := time.Now()
	persistMonitorFailureForTest(probeCtx, repo)

	require.NoError(t, taskCtx.Err())
	require.NoError(t, insertCtxErr)
	require.True(t, hasDeadline)
	require.WithinDuration(t, started.Add(5*time.Second), insertDeadline, time.Second)
	require.Len(t, repo.rows, 1)
	require.Equal(t, MonitorStatusError, repo.rows[0].Status)
	require.Equal(t, "request timeout", repo.rows[0].Message)
	require.Equal(t, 1, repo.insertCalls)
	require.Equal(t, 1, repo.markCalls)
	require.True(t, repo.insertCtx == repo.markCtx, "history and checked time must share one persistence budget")
}

func TestPersistCheckResults_PreCancelledTaskSkipsBothWrites(t *testing.T) {
	taskCtx, cancelTask := context.WithCancel(context.Background())
	cancelTask()
	ctx := withChannelMonitorRunnerTaskContext(context.Background(), taskCtx)
	repo := &unknownMonitorRepo{}

	persistMonitorFailureForTest(ctx, repo)

	require.Empty(t, repo.rows)
	require.Zero(t, repo.insertCalls)
	require.Zero(t, repo.markCalls)
}

func TestPersistCheckResults_TaskCancellationInterruptsHistoryWrite(t *testing.T) {
	taskCtx, cancelTask := context.WithCancel(context.Background())
	t.Cleanup(cancelTask)
	started := make(chan struct{})
	finished := make(chan struct{})
	var insertErr error
	repo := &unknownMonitorRepo{
		beforeInsert: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			insertErr = ctx.Err()
			return insertErr
		},
	}
	go func() {
		defer close(finished)
		persistMonitorFailureForTest(withChannelMonitorRunnerTaskContext(taskCtx, taskCtx), repo)
	}()
	waitForMonitorPersistenceSignal(t, started)
	cancelTask()
	waitForMonitorPersistenceSignal(t, finished)

	require.ErrorIs(t, insertErr, context.Canceled)
	require.Empty(t, repo.rows)
	require.Equal(t, 1, repo.insertCalls)
	require.Zero(t, repo.markCalls)
}

func TestPersistCheckResults_TaskCancellationAfterHistorySkipsMarkChecked(t *testing.T) {
	taskCtx, cancelTask := context.WithCancel(context.Background())
	t.Cleanup(cancelTask)
	repo := &unknownMonitorRepo{afterInsert: cancelTask}

	persistMonitorFailureForTest(withChannelMonitorRunnerTaskContext(taskCtx, taskCtx), repo)

	require.Len(t, repo.rows, 1, "already committed history is retained")
	require.Equal(t, 1, repo.insertCalls)
	require.Zero(t, repo.markCalls)
}

func TestPersistCheckResults_TaskCancellationInterruptsMarkChecked(t *testing.T) {
	taskCtx, cancelTask := context.WithCancel(context.Background())
	t.Cleanup(cancelTask)
	started := make(chan struct{})
	finished := make(chan struct{})
	var markErr error
	repo := &unknownMonitorRepo{
		beforeMark: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			markErr = ctx.Err()
			return markErr
		},
	}
	go func() {
		defer close(finished)
		persistMonitorFailureForTest(withChannelMonitorRunnerTaskContext(taskCtx, taskCtx), repo)
	}()
	waitForMonitorPersistenceSignal(t, started)
	cancelTask()
	waitForMonitorPersistenceSignal(t, finished)

	require.ErrorIs(t, markErr, context.Canceled)
	require.Len(t, repo.rows, 1, "history committed before cancellation is retained")
	require.Equal(t, 1, repo.insertCalls)
	require.Equal(t, 1, repo.markCalls)
}

func TestPersistCheckResults_HistoryFailureDoesNotMarkChecked(t *testing.T) {
	repo := &unknownMonitorRepo{insertErr: errors.New("history unavailable")}
	ctx := withChannelMonitorRunnerTaskContext(context.Background(), context.Background())

	persistMonitorFailureForTest(ctx, repo)

	require.Empty(t, repo.rows)
	require.Equal(t, 1, repo.insertCalls)
	require.Zero(t, repo.markCalls)
}

func TestPersistCheckResults_MarkCheckedFailureRetainsHistory(t *testing.T) {
	repo := &unknownMonitorRepo{markErr: errors.New("checked time unavailable")}
	ctx := withChannelMonitorRunnerTaskContext(context.Background(), context.Background())

	persistMonitorFailureForTest(ctx, repo)

	require.Len(t, repo.rows, 1)
	require.Equal(t, MonitorStatusError, repo.rows[0].Status)
	require.Equal(t, 1, repo.insertCalls)
	require.Equal(t, 1, repo.markCalls)
}

func TestPersistCheckResults_CancelledManualProbeSkipsBothWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &unknownMonitorRepo{}

	persistMonitorFailureForTest(ctx, repo)

	require.Empty(t, repo.rows)
	require.Zero(t, repo.insertCalls)
	require.Zero(t, repo.markCalls)
}

func TestPersistCheckResults_ManualFailureKeepsOriginalContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	repo := &unknownMonitorRepo{}

	persistMonitorFailureForTest(ctx, repo)

	require.Len(t, repo.rows, 1)
	require.Equal(t, MonitorStatusError, repo.rows[0].Status)
	require.Equal(t, 1, repo.insertCalls)
	require.Equal(t, 1, repo.markCalls)
	require.True(t, ctx == repo.insertCtx)
	require.True(t, ctx == repo.markCtx)
	require.NoError(t, ctx.Err())
}
