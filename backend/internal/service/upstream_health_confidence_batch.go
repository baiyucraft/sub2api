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

// GetUpstreamHealthConfidences takes one request-local settings snapshot and
// batches storage reads. No cross-request cache or truncated history is used.
func (s *UpstreamConfigService) GetUpstreamHealthConfidences(ctx context.Context, keyIDs []int64) (map[int64]UpstreamHealthConfidenceSummary, error) {
	keyIDs = confidenceSummaryKeyIDs(keyIDs)
	out := make(map[int64]UpstreamHealthConfidenceSummary, len(keyIDs))
	if len(keyIDs) == 0 {
		return out, nil
	}
	reader, ok := s.repo.(UpstreamHealthConfidenceReader)
	if !ok {
		return out, nil
	}
	readErrors := map[int64]error{}
	if batch, ok := s.repo.(UpstreamHealthConfidencesReader); ok {
		var err error
		out, err = batch.GetUpstreamHealthConfidences(ctx, keyIDs)
		if out == nil {
			out = make(map[int64]UpstreamHealthConfidenceSummary, len(keyIDs))
		}
		if err != nil {
			for _, id := range keyIDs {
				readErrors[id] = err
			}
		}
	} else {
		for _, id := range keyIDs {
			var err error
			out[id], err = reader.GetUpstreamHealthConfidence(ctx, id)
			if err != nil {
				readErrors[id] = err
			}
		}
	}
	if !s.confidenceProbeIndependent(ctx) {
		for _, id := range keyIDs {
			if err := readErrors[id]; err != nil {
				return nil, err
			}
		}
		return out, nil
	}

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
		now := time.Now().UTC()
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
	return out, nil
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
