package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const UpstreamConfidenceDistributionLease = 2 * time.Minute

var ErrConfidenceDistributionLeaseLost = errors.New("confidence distribution attempt lease lost")

// DistributionAttempt is a durable claim. A consumed sequence is never retried,
// even if the worker disappears before the response can be recorded.
type DistributionAttempt struct {
	SeriesID   string `json:"series_id"`
	LeaseToken string `json:"lease_token"`
	Sequence   int64  `json:"sequence"`
	ProbeID    string `json:"probe_id"`
}

type UpstreamConfidenceDistributionRepository interface {
	ClaimConfidenceDistribution(context.Context, int64, string, string, time.Time) (*DistributionAttempt, error)
	FinishConfidenceDistribution(context.Context, int64, *DistributionAttempt, DistributionSample, time.Time) (*UpstreamConfidenceDistribution, bool, error)
	LoadConfidenceDistribution(context.Context, int64, time.Time) (*UpstreamConfidenceDistribution, error)
	LoadConfidenceDistributionForSeries(context.Context, int64, string, time.Time) (*UpstreamConfidenceDistribution, error)
}

// ConfidenceDistributionState is stored separately from key health metadata so
// health observation updates cannot overwrite an in-flight claim.
type ConfidenceDistributionState struct {
	SeriesID        string               `json:"series_id"`
	Fingerprint     string               `json:"fingerprint"`
	Protocol        string               `json:"protocol"`
	BaselineVersion string               `json:"baseline_version"`
	ProbeOrder      []string             `json:"probe_order"`
	NextSequence    int64                `json:"next_sequence"`
	Samples         []DistributionSample `json:"samples"`
	Pending         *DistributionAttempt `json:"pending,omitempty"`
	LeaseExpiresAt  time.Time            `json:"lease_expires_at,omitempty"`
	LastDecisive    string               `json:"last_decisive,omitempty"`
}

func NewConfidenceDistributionState(fingerprint, protocol string) (*ConfidenceDistributionState, error) {
	if fingerprint == "" {
		return nil, errors.New("confidence distribution fingerprint is required")
	}
	protocol = distributionProtocol(protocol)
	if _, err := distributionLoadBaseline(protocol); err != nil {
		return nil, err
	}
	order, err := NewDistributionProbeOrder()
	if err != nil {
		return nil, err
	}
	seriesID, err := confidenceDistributionToken()
	if err != nil {
		return nil, err
	}
	return &ConfidenceDistributionState{
		SeriesID: seriesID, Fingerprint: fingerprint, Protocol: protocol,
		BaselineVersion: DistributionBaselineVersion(protocol), ProbeOrder: order,
		NextSequence: 1, Samples: []DistributionSample{},
	}, nil
}

func confidenceDistributionToken() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate confidence distribution token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func (s *ConfidenceDistributionState) Validate() error {
	if s == nil || s.SeriesID == "" || s.Fingerprint == "" || s.NextSequence < 1 || len(s.ProbeOrder) != UpstreamConfidenceDistributionWindowSize {
		return errors.New("invalid persistent confidence distribution state")
	}
	counts := make(map[string]int)
	for _, id := range s.ProbeOrder {
		counts[id]++
	}
	quotas := DistributionProbeQuotas()
	if len(counts) != len(quotas) {
		return errors.New("invalid confidence distribution cycle")
	}
	for id, count := range quotas {
		if counts[id] != count {
			return errors.New("invalid confidence distribution cycle quota")
		}
	}
	return nil
}

// Recover expires abandoned claims without issuing another request and applies
// the same retention boundary as health observations. It returns whether the
// durable state changed.
func (s *ConfidenceDistributionState) Recover(now time.Time) bool {
	changed := false
	if s.Pending != nil && !s.LeaseExpiresAt.After(now) {
		for i := range s.Samples {
			if s.Samples[i].Sequence == s.Pending.Sequence {
				s.Samples[i].Valid = false
				s.Samples[i].Answer = ""
				s.Samples[i].Reason = "lease_expired"
				break
			}
		}
		s.Pending, s.LeaseExpiresAt = nil, time.Time{}
		changed = true
	}
	cutoff := now.UTC().Add(-UpstreamHealthObservationRetention)
	kept := make([]DistributionSample, 0, len(s.Samples))
	for _, sample := range s.Samples {
		if sample.ObservedAt.Before(cutoff) {
			changed = true
			continue
		}
		kept = append(kept, sample)
	}
	if changed {
		s.Samples = kept
	}
	return changed
}

func (s *ConfidenceDistributionState) Claim(now time.Time) (*DistributionAttempt, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s.Recover(now)
	if s.Pending != nil {
		return nil, nil
	}
	token, err := confidenceDistributionToken()
	if err != nil {
		return nil, err
	}
	attempt := &DistributionAttempt{
		SeriesID: s.SeriesID, LeaseToken: token, Sequence: s.NextSequence,
		ProbeID: s.ProbeOrder[(s.NextSequence-1)%UpstreamConfidenceDistributionWindowSize],
	}
	s.NextSequence++
	s.Pending = attempt
	s.LeaseExpiresAt = now.UTC().Add(UpstreamConfidenceDistributionLease)
	s.Samples = append(s.Samples, DistributionSample{
		Sequence: attempt.Sequence, ProbeID: attempt.ProbeID, Reason: "pending", ObservedAt: now.UTC(),
	})
	if len(s.Samples) > UpstreamConfidenceDistributionWindowSize {
		s.Samples = s.Samples[len(s.Samples)-UpstreamConfidenceDistributionWindowSize:]
	}
	return attempt, nil
}

func (s *ConfidenceDistributionState) Finish(attempt *DistributionAttempt, sample DistributionSample, now time.Time) (*UpstreamConfidenceDistribution, bool, error) {
	if err := s.Validate(); err != nil {
		return nil, false, err
	}
	if attempt == nil || s.Pending == nil || !s.LeaseExpiresAt.After(now) ||
		attempt.SeriesID != s.SeriesID || attempt.LeaseToken != s.Pending.LeaseToken ||
		attempt.Sequence != s.Pending.Sequence || attempt.ProbeID != s.Pending.ProbeID {
		return nil, false, ErrConfidenceDistributionLeaseLost
	}
	index := -1
	for i := range s.Samples {
		if s.Samples[i].Sequence == attempt.Sequence {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, false, ErrConfidenceDistributionLeaseLost
	}
	sample.Sequence, sample.ProbeID = attempt.Sequence, attempt.ProbeID
	// Window times identify issued requests, independent of their duration.
	sample.ObservedAt = s.Samples[index].ObservedAt
	s.Samples[index] = sample
	s.Pending, s.LeaseExpiresAt = nil, time.Time{}
	s.Recover(now)
	summary, err := s.Summary()
	if err != nil {
		return summary, false, err
	}
	alert := s.RecordDecisive(summary.Status)
	return summary, alert, nil
}

// RecordDecisive preserves the last sufficient verdict across collecting or
// insufficient windows so an inconclusive response cannot trigger a false
// recovery, or cause another copy of an existing mismatch alert.
func (s *ConfidenceDistributionState) RecordDecisive(status string) bool {
	if status != "match" && status != "mismatch" {
		return false
	}
	alert := (s.LastDecisive == "" && status == "mismatch") ||
		(s.LastDecisive != "" && s.LastDecisive != status)
	s.LastDecisive = status
	return alert
}

func (s *ConfidenceDistributionState) Summary() (*UpstreamConfidenceDistribution, error) {
	summary, err := ScoreUpstreamConfidenceDistribution(s.Protocol, s.Samples)
	if err == nil && summary != nil && s.Pending != nil {
		// A claim consumes a position but is not a completed observation. Never
		// publish a fresh numerical verdict while its response is still pending,
		// including the first full window with 127 successful observations.
		summary.Status = "collecting"
		summary.Reasons = []string{"probe_pending"}
		summary.Matches, summary.Scores = map[string]float64{}, map[string]float64{}
		summary.ClosestModel = ""
	}
	return summary, err
}
