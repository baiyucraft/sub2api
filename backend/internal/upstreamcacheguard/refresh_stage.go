package upstreamcacheguard

import (
	"context"
	"errors"
	"sync"
)

// refreshLock serializes refreshes without allowing lock contention to outlive
// their caller's budget. Its zero value is ready for use.
type refreshLock struct {
	once  sync.Once
	token chan struct{}
}

func (m *refreshLock) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.once.Do(func() {
		m.token = make(chan struct{}, 1)
		m.token <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

func (m *refreshLock) Unlock() { m.token <- struct{}{} }

// Configuration writers publish settings, policies and their epoch under mu.
// Capture them under one matching read lock; Settings and activePolicies only
// load immutable atomic pointers and must not acquire mu recursively here.
func (e *Engine) refreshConfiguration() (Settings, uint64, map[string]Settings) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Settings(), e.policyEpoch.Load(), e.activePolicies()
}

// RefreshStageError identifies a fixed refresh operation while retaining the
// original cause for errors.Is/errors.As. The stage never contains identities.
type RefreshStageError struct {
	Stage string
	Err   error
}

func (e *RefreshStageError) Error() string {
	return "cache guard refresh " + e.Stage + ": " + e.Err.Error()
}
func (e *RefreshStageError) Unwrap() error { return e.Err }

func runRefreshStage(ctx context.Context, stage string, run func() error) error {
	done := MeasureStage(ctx, stage)
	defer done()
	if err := ctx.Err(); err != nil {
		return &RefreshStageError{Stage: stage, Err: err}
	}
	err := run()
	// lib/pq may return SQLSTATE 57014 after the caller's context expires.
	// Retain that driver cause and the context cause sampled at stage return.
	err = errors.Join(err, ctx.Err())
	if err != nil {
		return &RefreshStageError{Stage: stage, Err: err}
	}
	return nil
}
