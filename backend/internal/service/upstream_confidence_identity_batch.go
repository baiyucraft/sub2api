package service

import (
	"context"
	"errors"
	"time"
)

// distributionClientPolicy lives only for one call. It gives every eligible
// account the same settings evidence without touching dispatch's shared state.
type distributionClientPolicy struct {
	ctx                    context.Context
	loaded                 bool
	manualVersion, version string
	err                    error
}

type distributionProbeModelContextKey struct{}

func (p *distributionClientPolicy) probeModel() string {
	if model, ok := p.ctx.Value(distributionProbeModelContextKey{}).(string); ok && model != "" {
		return model
	}
	return UpstreamConfidenceDistributionClaimedModel
}

func (p *distributionClientPolicy) load(settings *SettingService) (string, string, error) {
	if p.loaded {
		return p.manualVersion, p.version, p.err
	}
	p.loaded = true
	read := func(key string) (string, error) {
		ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
		defer cancel()
		value, err := settings.settingRepo.GetValue(ctx, key)
		if errors.Is(err, ErrSettingNotFound) {
			err = nil
		}
		return value, err
	}
	value, err := read(SettingKeyOpenAICodexClientVersion)
	if err != nil {
		p.err = err
		return "", "", err
	}
	p.manualVersion = NormalizeCodexClientVersion(value)
	if p.manualVersion != "" && CompareVersions(p.manualVersion, codexUpstreamMinVersion) >= 0 {
		p.version = p.manualVersion
		return p.manualVersion, p.version, nil
	}
	p.manualVersion = ""
	value, err = read(SettingKeyOpenAICodexClientVersionSynced)
	if err != nil {
		p.err = err
		return "", "", err
	}
	p.version = NormalizeCodexClientVersion(value)
	if p.version == "" || CompareVersions(p.version, codexUpstreamMinVersion) < 0 {
		p.version = codexCLIVersion
	}
	return p.manualVersion, p.version, nil
}

func (s *AccountTestService) ResolveConfidenceDistributionIdentities(ctx context.Context, accounts []*Account) map[int64]ConfidenceDistributionIdentityResult {
	policy := &distributionClientPolicy{ctx: ctx}
	results := make(map[int64]ConfidenceDistributionIdentityResult, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		spec, err := s.resolveDistributionRequestWithPolicy(account, policy)
		result := ConfidenceDistributionIdentityResult{Err: err}
		if err == nil {
			result.Identity = spec.identity
		}
		results[account.ID] = result
	}
	return results
}
