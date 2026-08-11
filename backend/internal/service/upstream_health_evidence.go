package service

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const upstreamHealthTrafficPersistInterval = 30 * time.Second

// UpstreamHealthEvidenceRecorder is the narrow, provider-neutral seam used by
// request paths. It keeps the health module independent from the individual
// OpenAI, Anthropic and Gemini forwarding implementations.
type UpstreamHealthEvidenceRecorder interface {
	RecordUpstreamTrafficSuccess(ctx context.Context, account *Account, statusCode int)
	RecordUpstreamTrafficFailure(ctx context.Context, account *Account, statusCode int)
}

var globalUpstreamHealthEvidenceRecorder struct {
	sync.RWMutex
	value UpstreamHealthEvidenceRecorder
}

func SetGlobalUpstreamHealthEvidenceRecorder(recorder UpstreamHealthEvidenceRecorder) {
	globalUpstreamHealthEvidenceRecorder.Lock()
	globalUpstreamHealthEvidenceRecorder.value = recorder
	globalUpstreamHealthEvidenceRecorder.Unlock()
}

func upstreamHealthEvidenceRecorder() UpstreamHealthEvidenceRecorder {
	globalUpstreamHealthEvidenceRecorder.RLock()
	recorder := globalUpstreamHealthEvidenceRecorder.value
	globalUpstreamHealthEvidenceRecorder.RUnlock()
	return recorder
}

func ReportUpstreamTrafficSuccess(ctx context.Context, account *Account, statusCode int) {
	if recorder := upstreamHealthEvidenceRecorder(); recorder != nil {
		recorder.RecordUpstreamTrafficSuccess(ctx, account, statusCode)
	}
}

func ReportUpstreamTrafficFailure(ctx context.Context, account *Account, statusCode int) {
	if recorder := upstreamHealthEvidenceRecorder(); recorder != nil {
		recorder.RecordUpstreamTrafficFailure(ctx, account, statusCode)
	}
}

func (s *UpstreamConfigService) RecordUpstreamTrafficSuccess(ctx context.Context, account *Account, statusCode int) {
	if account == nil || account.UpstreamKeyID == nil || *account.UpstreamKeyID <= 0 {
		return
	}
	if statusCode <= 0 {
		statusCode = http.StatusOK
	}
	s.recordUpstreamTrafficEvidence(ctx, *account.UpstreamKeyID, true, strconv.Itoa(statusCode), "traffic_succeeded")
}

func (s *UpstreamConfigService) RecordUpstreamTrafficFailure(ctx context.Context, account *Account, statusCode int) {
	if account == nil || account.UpstreamKeyID == nil || *account.UpstreamKeyID <= 0 {
		return
	}
	reason := ""
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		reason = "authentication_failed"
	case statusCode == http.StatusTooManyRequests || statusCode == 529:
		reason = "capacity_limited"
	case statusCode >= http.StatusInternalServerError:
		reason = "upstream_server_error"
	default:
		// Request-scoped 4xx errors do not prove a Key health problem.
		return
	}
	s.recordUpstreamTrafficEvidence(ctx, *account.UpstreamKeyID, false, strconv.Itoa(statusCode), reason)
}

func (s *UpstreamConfigService) recordUpstreamTrafficEvidence(ctx context.Context, keyID int64, success bool, status, reason string) {
	if s == nil || keyID <= 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = s.withHealthKeyLock(keyID, func() error {
		now := time.Now().UTC()
		registry := GlobalUpstreamHealthRegistry()
		var transition UpstreamHealthTransition
		if success {
			transition = registry.RecordTrafficSuccessTransition(keyID, status, reason, now)
		} else {
			transition = registry.RecordTrafficFailureTransition(keyID, status, reason, now)
		}

		lastPersistedValue, hasLastPersisted := s.healthPersistedAt.Load(keyID)
		lastPersisted, _ := lastPersistedValue.(time.Time)
		persist := !success || transition.StateChanged() || !hasLastPersisted ||
			now.Sub(lastPersisted) >= upstreamHealthTrafficPersistInterval
		if !persist {
			return nil
		}

		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		observation := &UpstreamHealthObservation{
			ObservedAt: transition.Current.UpdatedAt,
			State:      transition.Current.Status,
			Source:     "traffic",
			Result:     status,
			Reason:     reason,
		}
		if err := s.saveHealthTransitionWithObservation(persistCtx, keyID, transition, observation); err != nil {
			// Health evidence is fail-open. The persistence helper restores the
			// previous in-memory snapshot on failure.
			return err
		}
		s.healthPersistedAt.Store(keyID, now)
		return nil
	})
}
