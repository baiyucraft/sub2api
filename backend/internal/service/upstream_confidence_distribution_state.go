package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
)

const UpstreamConfidenceDistributionLease = 2 * time.Minute

var ErrConfidenceDistributionLeaseLost = errors.New("confidence distribution attempt lease lost")

// DistributionAttempt is a durable claim. A consumed sequence is never retried,
// even if the worker disappears before the response can be recorded.
type DistributionAttempt struct {
	SeriesID      string `json:"series_id"`
	LeaseToken    string `json:"lease_token"`
	Sequence      int64  `json:"sequence"`
	ProbeID       string `json:"probe_id"`
	ClientVersion string `json:"-"`
}

type UpstreamConfidenceDistributionRepository interface {
	ClaimConfidenceDistribution(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*DistributionAttempt, error)
	FinishConfidenceDistribution(context.Context, int64, *DistributionAttempt, DistributionSample, time.Time) (*UpstreamConfidenceDistribution, bool, error)
	LoadConfidenceDistribution(context.Context, int64, time.Time) (*UpstreamConfidenceDistribution, error)
	LoadConfidenceDistributionForSeries(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*UpstreamConfidenceDistribution, error)
}

// ConfidenceDistributionState is stored separately from key health metadata so
// health observation updates cannot overwrite an in-flight claim.
type ConfidenceDistributionState struct {
	IdentityVersion    int               `json:"identity_version,omitempty"`
	IdentityComponents map[string]string `json:"identity_components,omitempty"`
	// Public SemVer generator input only; credentials and header values are never persisted.
	ClientVersion   string                             `json:"client_version,omitempty"`
	SeriesReset     *ConfidenceDistributionSeriesReset `json:"series_reset,omitempty"`
	SeriesID        string                             `json:"series_id"`
	Fingerprint     string                             `json:"fingerprint"`
	Protocol        string                             `json:"protocol"`
	BaselineVersion string                             `json:"baseline_version"`
	ProbeOrder      []string                           `json:"probe_order"`
	NextSequence    int64                              `json:"next_sequence"`
	Samples         []DistributionSample               `json:"samples"`
	Pending         *DistributionAttempt               `json:"pending,omitempty"`
	LeaseExpiresAt  time.Time                          `json:"lease_expires_at,omitempty"`
	LastDecisive    string                             `json:"last_decisive,omitempty"`
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

// NewConfidenceDistributionStateForIdentity records only canonical request
// identity digests. The legacy constructor remains useful for decoding fixtures.
func NewConfidenceDistributionStateForIdentity(identity ConfidenceDistributionIdentity) (*ConfidenceDistributionState, error) {
	if identity.Version != ConfidenceDistributionIdentityVersion || identity.BaselineVersion != DistributionBaselineVersion(identity.Protocol) {
		return nil, errors.New("invalid confidence distribution request identity")
	}
	state, err := NewConfidenceDistributionState(identity.Fingerprint, identity.Protocol)
	if err != nil {
		return nil, err
	}
	state.UpgradeIdentity(identity)
	return state, nil
}

// IdentityChange describes a safe upgrade or the public reasons for starting a
// new series. It never exposes digests or request configuration values.
func (s *ConfidenceDistributionState) IdentityChange(identity ConfidenceDistributionIdentity) (reasons []string, upgrade bool) {
	if s.IdentityVersion == 0 {
		if identity.Version == ConfidenceDistributionIdentityVersion && identity.LegacyCompatible && s.Fingerprint == identity.LegacyFingerprint &&
			s.Protocol == distributionProtocol(identity.Protocol) && s.BaselineVersion == identity.BaselineVersion {
			return nil, true
		}
		return []string{"legacy_identity_unverifiable"}, false
	}
	if s.IdentityVersion == 2 && identity.Version == ConfidenceDistributionIdentityVersion &&
		identity.V2Fingerprint != "" && s.Fingerprint == identity.V2Fingerprint &&
		s.Protocol == distributionProtocol(identity.Protocol) && s.BaselineVersion == identity.BaselineVersion {
		return nil, true
	}
	if s.IdentityVersion != identity.Version || identity.Version != ConfidenceDistributionIdentityVersion {
		return []string{"legacy_identity_unverifiable"}, false
	}
	for _, component := range []string{"binding", "protocol", "endpoint", "credential", "model", "proxy", "headers", "contract", "baseline"} {
		if s.IdentityComponents[component] != identity.Components[component] {
			reasons = append(reasons, component+"_changed")
		}
	}
	// Explicit fields also protect against partially recorded identities.
	if s.Protocol != distributionProtocol(identity.Protocol) {
		reasons = append(reasons, "protocol_changed")
	}
	if s.BaselineVersion != identity.BaselineVersion {
		reasons = append(reasons, "baseline_changed")
	}
	if len(reasons) == 0 && s.Fingerprint != identity.Fingerprint {
		reasons = append(reasons, "legacy_identity_unverifiable")
	}
	sort.Strings(reasons)
	unique := reasons[:0]
	for _, reason := range reasons {
		if len(unique) == 0 || unique[len(unique)-1] != reason {
			unique = append(unique, reason)
		}
	}
	return unique, false
}

// UpgradeIdentity changes no sampling state, lease, or last decisive verdict.
func (s *ConfidenceDistributionState) UpgradeIdentity(identity ConfidenceDistributionIdentity) {
	s.IdentityVersion, s.Fingerprint = identity.Version, identity.Fingerprint
	s.ClientVersion = identity.ClientVersion
	s.IdentityComponents = make(map[string]string, len(identity.Components))
	for component, digest := range identity.Components {
		s.IdentityComponents[component] = digest
	}
}

func (s *ConfidenceDistributionState) PendingResetSummary(identity ConfidenceDistributionIdentity, reasons []string) (*UpstreamConfidenceDistribution, error) {
	summary, err := ScoreUpstreamConfidenceDistribution(identity.Protocol, nil)
	if summary != nil {
		summary.SeriesReset = &ConfidenceDistributionSeriesReset{Pending: true, Reasons: append([]string(nil), reasons...), PreviousAttempted: len(s.Samples)}
	}
	return summary, err
}

func confidenceDistributionToken() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate confidence distribution token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func (s *ConfidenceDistributionState) Validate() error {
	if s == nil || s.IdentityVersion < 0 || s.SeriesID == "" || s.Fingerprint == "" || s.NextSequence < 1 || len(s.ProbeOrder) != UpstreamConfidenceDistributionWindowSize {
		return errors.New("invalid persistent confidence distribution state")
	}
	if s.IdentityVersion == ConfidenceDistributionIdentityVersion && s.Protocol == "responses" &&
		(NormalizeCodexClientVersion(s.ClientVersion) != s.ClientVersion || s.ClientVersion == "" || CompareVersions(s.ClientVersion, codexUpstreamMinVersion) < 0) {
		return errors.New("invalid persistent confidence distribution client version")
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
		ProbeID:       s.ProbeOrder[(s.NextSequence-1)%UpstreamConfidenceDistributionWindowSize],
		ClientVersion: s.ClientVersion,
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
	if summary != nil {
		summary.SeriesReset = cloneConfidenceDistributionSeriesReset(s.SeriesReset)
	}
	return summary, err
}

func cloneConfidenceDistributionSeriesReset(reset *ConfidenceDistributionSeriesReset) *ConfidenceDistributionSeriesReset {
	if reset == nil {
		return nil
	}
	copy := *reset
	copy.Reasons = append([]string(nil), reset.Reasons...)
	if reset.At != nil {
		at := *reset.At
		copy.At = &at
	}
	return &copy
}
