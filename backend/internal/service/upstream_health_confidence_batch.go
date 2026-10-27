package service

import (
	"context"
	"errors"
	"sort"
	"time"
)

type upstreamAccountBindingsBatchLister interface {
	ListByUpstreamKeyIDs(context.Context, []int64) ([]Account, error)
}

type ConfidenceDistributionReadResult struct {
	Distribution *UpstreamConfidenceDistribution
	Err          error
}

type upstreamConfidenceDistributionBatchReader interface {
	LoadConfidenceDistributionsForSeries(context.Context, map[int64]ConfidenceDistributionIdentity, time.Time) (map[int64]ConfidenceDistributionReadResult, error)
}

type ConfidenceDistributionIdentityResult struct {
	Identity ConfidenceDistributionIdentity
	Err      error
}

type upstreamConfidenceIdentityBatchResolver interface {
	ResolveConfidenceDistributionIdentities(context.Context, []*Account) map[int64]ConfidenceDistributionIdentityResult
}

// Optional fixed-clock reader keeps one list response on a single evaluation
// instant while retaining compatibility with existing repositories.
type upstreamHealthConfidencesAtReader interface {
	GetUpstreamHealthConfidencesAt(context.Context, []int64, time.Time) (map[int64]UpstreamHealthConfidenceSummary, error)
}

func (s *UpstreamConfigService) readLegacyHealthConfidencesAt(ctx context.Context, keyIDs []int64, now time.Time) (map[int64]UpstreamHealthConfidenceSummary, error) {
	if len(keyIDs) == 0 {
		return map[int64]UpstreamHealthConfidenceSummary{}, nil
	}
	if batch, ok := s.repo.(upstreamHealthConfidencesAtReader); ok {
		return batch.GetUpstreamHealthConfidencesAt(ctx, keyIDs, now)
	}
	if batch, ok := s.repo.(UpstreamHealthConfidencesReader); ok {
		return batch.GetUpstreamHealthConfidences(ctx, keyIDs)
	}
	out := make(map[int64]UpstreamHealthConfidenceSummary, len(keyIDs))
	if reader, ok := s.repo.(UpstreamHealthConfidenceReader); ok {
		for _, id := range keyIDs {
			value, err := reader.GetUpstreamHealthConfidence(ctx, id)
			if err != nil {
				return nil, err
			}
			out[id] = value
		}
	}
	return out, nil
}

// GetUpstreamHealthConfidences takes one request-local settings snapshot and
// batches storage reads. No cross-request cache or truncated history is used.
func (s *UpstreamConfigService) GetUpstreamHealthConfidences(ctx context.Context, keyIDs []int64) (map[int64]UpstreamHealthConfidenceSummary, error) {
	return s.GetUpstreamHealthConfidencesAt(ctx, keyIDs, time.Now().UTC())
}

func (s *UpstreamConfigService) GetUpstreamHealthConfidencesAt(ctx context.Context, keyIDs []int64, now time.Time) (map[int64]UpstreamHealthConfidenceSummary, error) {
	now = now.UTC()
	keyIDs = confidenceSummaryKeyIDs(keyIDs)
	out := make(map[int64]UpstreamHealthConfidenceSummary, len(keyIDs))
	if len(keyIDs) == 0 {
		return out, nil
	}
	if !s.confidenceProbeIndependent(ctx) {
		return s.readLegacyHealthConfidencesAt(ctx, keyIDs, now)
	}

	readErrors := map[int64]error{}
	lister, bound := s.accountRepo.(upstreamAccountBindingLister)
	accountsByKey := map[int64][]Account{}
	if batch, ok := s.accountRepo.(upstreamAccountBindingsBatchLister); ok && bound {
		accounts, err := batch.ListByUpstreamKeyIDs(ctx, keyIDs)
		for _, id := range keyIDs {
			if err != nil {
				readErrors[id] = err
			}
		}
		for _, account := range accounts {
			if account.UpstreamKeyID != nil {
				accountsByKey[*account.UpstreamKeyID] = append(accountsByKey[*account.UpstreamKeyID], account)
			}
		}
	} else if bound {
		for _, id := range keyIDs {
			if readErrors[id] != nil {
				continue
			}
			var err error
			accountsByKey[id], err = lister.ListByUpstreamKeyID(ctx, id)
			if err != nil {
				readErrors[id] = err
			}
		}
	}
	collector, supported := s.repo.(UpstreamConfidenceDistributionRepository)
	resolver, available := s.accountProber.(interface {
		ResolveConfidenceDistributionIdentity(*Account) (ConfidenceDistributionIdentity, error)
	})
	eligible := make([]*Account, 0, len(keyIDs))
	eligibleKeys := map[int64]int64{}
	protocols := map[int64]string{}
	legacyIDs := make([]int64, 0, len(keyIDs))
	for _, id := range keyIDs {
		if readErrors[id] != nil {
			out[id] = unavailableConfidenceDistributionSummary("responses", "confidence_read_unavailable")
			continue
		}
		if !bound {
			out[id] = unavailableConfidenceDistributionSummary("responses", "request_configuration_unavailable")
			continue
		}
		accounts := accountsByKey[id]
		if len(accounts) != 1 {
			out[id] = unavailableConfidenceDistributionSummary("responses", "account_binding_unavailable")
			continue
		}
		account := &accounts[0]
		if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
			legacyIDs = append(legacyIDs, id)
			continue
		}
		protocols[id] = distributionAccountProtocol(account)
		if !supported || !available {
			out[id] = unavailableConfidenceDistributionSummary(protocols[id], "request_configuration_unavailable")
			continue
		}
		eligible = append(eligible, account)
		eligibleKeys[account.ID] = id
	}
	legacy, legacyErr := s.readLegacyHealthConfidencesAt(ctx, legacyIDs, now)
	for _, id := range legacyIDs {
		if legacyErr != nil {
			out[id] = unavailableConfidenceDistributionSummary("responses", "confidence_read_unavailable")
		} else {
			out[id] = legacy[id]
		}
	}
	resolved := map[int64]ConfidenceDistributionIdentityResult{}
	if batch, ok := s.accountProber.(upstreamConfidenceIdentityBatchResolver); ok {
		resolved = batch.ResolveConfidenceDistributionIdentities(ctx, eligible)
	} else {
		for _, account := range eligible {
			identity, err := resolver.ResolveConfidenceDistributionIdentity(account)
			resolved[account.ID] = ConfidenceDistributionIdentityResult{Identity: identity, Err: err}
		}
	}
	identities := map[int64]ConfidenceDistributionIdentity{}
	for _, account := range eligible {
		id := eligibleKeys[account.ID]
		result, present := resolved[account.ID]
		if !present {
			result.Err = errors.New("confidence distribution identity unavailable")
		}
		if result.Err != nil {
			reason := "request_configuration_unavailable"
			var configErr *distributionConfigurationError
			if errors.As(result.Err, &configErr) {
				switch configErr.reason {
				case "probe_credentials_missing", "probe_base_url_invalid", "probe_model_unsupported", "probe_request_invalid":
					reason = configErr.reason
				}
			}
			out[id] = unavailableConfidenceDistributionSummary(protocols[id], reason)
			continue
		}
		identities[id] = result.Identity
	}
	reads := map[int64]ConfidenceDistributionReadResult{}
	if len(identities) > 0 {
		if batch, ok := s.repo.(upstreamConfidenceDistributionBatchReader); ok {
			var err error
			reads, err = batch.LoadConfidenceDistributionsForSeries(ctx, identities, now)
			if reads == nil {
				reads = map[int64]ConfidenceDistributionReadResult{}
			}
			if err != nil {
				for id := range identities {
					reads[id] = ConfidenceDistributionReadResult{Err: err}
				}
			}
		} else {
			for id, identity := range identities {
				distribution, err := collector.LoadConfidenceDistributionForSeries(ctx, id, identity, now)
				reads[id] = ConfidenceDistributionReadResult{Distribution: distribution, Err: err}
			}
		}
	}
	for id := range identities {
		read := reads[id]
		if read.Err != nil {
			out[id] = unavailableConfidenceDistributionSummary(protocols[id], "confidence_read_unavailable")
			continue
		}
		if read.Distribution == nil {
			var err error
			read.Distribution, err = ScoreUpstreamConfidenceDistribution(protocols[id], nil)
			if err != nil {
				out[id] = unavailableConfidenceDistributionSummary(protocols[id], "baseline_unavailable")
				continue
			}
		}
		out[id] = UpstreamHealthConfidenceSummary{Distribution: read.Distribution, Status: read.Distribution.Status,
			RequestedEffort: "low", PromptVersion: UpstreamConfidenceDistributionPromptVersion}
	}
	if err := s.attachConfidenceFingerprintAttributions(ctx, keyIDs, accountsByKey, out, now); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UpstreamConfigService) attachConfidenceFingerprintAttributions(ctx context.Context, keyIDs []int64, accountsByKey map[int64][]Account, out map[int64]UpstreamHealthConfidenceSummary, now time.Time) error {
	collector, ok := s.repo.(ConfidenceFingerprintRepository)
	if !ok {
		for _, id := range keyIDs {
			if len(accountsByKey[id]) == 1 && accountsByKey[id][0].Type == AccountTypeAPIKey {
				attribution := ConfidenceFingerprintAttribution{Status: "unknown", Reason: "fingerprint_storage_unavailable", Platform: accountsByKey[id][0].Platform, Methods: map[string]ConfidenceFingerprintMethodSummary{}}
				out[id] = withConfidenceAttribution(out[id], MergeConfidenceFingerprintAttribution(out[id].Distribution, attribution))
			}
		}
		return nil
	}
	models := DefaultUpstreamProbeModels()
	if s.settingService != nil {
		if configured, err := s.settingService.GetUpstreamProbeModels(ctx); err == nil {
			models = configured
		}
	}
	identities := make(map[int64]ConfidenceFingerprintIdentity, len(keyIDs))
	for _, id := range keyIDs {
		accounts := accountsByKey[id]
		if len(accounts) != 1 || accounts[0].Type != AccountTypeAPIKey || !UpstreamProbePlatformSupported(accounts[0].Platform) {
			continue
		}
		model := models.ModelFor(accounts[0].Platform)
		if accounts[0].Platform == PlatformOpenAI {
			model = UpstreamConfidenceDistributionClaimedModel
		}
		identity, ok := ConfidenceFingerprintIdentityForAccount(&accounts[0], model)
		if ok {
			identities[id] = identity
		}
	}
	reads, err := collector.LoadConfidenceFingerprints(ctx, identities)
	if reads == nil {
		reads = map[int64]ConfidenceFingerprintReadResult{}
	}
	if err != nil {
		for id := range identities {
			reads[id] = ConfidenceFingerprintReadResult{Err: err}
		}
	}
	for id, identity := range identities {
		read := reads[id]
		var attribution ConfidenceFingerprintAttribution
		if read.Err != nil {
			attribution = ScoreConfidenceFingerprintAt(identity, nil, now)
			attribution.Reason = "fingerprint_read_unavailable"
			for key, method := range attribution.Methods {
				method.Reason = "fingerprint_read_unavailable"
				attribution.Methods[key] = method
			}
		} else {
			attribution = ScoreConfidenceFingerprintAt(identity, read.State, now)
		}
		attribution = MergeConfidenceFingerprintAttribution(out[id].Distribution, attribution)
		out[id] = withConfidenceAttribution(out[id], attribution)
	}
	return nil
}

func withConfidenceAttribution(summary UpstreamHealthConfidenceSummary, attribution ConfidenceFingerprintAttribution) UpstreamHealthConfidenceSummary {
	summary.Attribution = &attribution
	return summary
}

func confidenceSummaryKeyIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
