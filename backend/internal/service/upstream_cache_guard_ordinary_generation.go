package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

// Ordinary recovery generations must survive restarts and agree across
// instances. Process-local snapshot revisions cannot identify a configuration
// changed away and back while a consumer was stopped.
func (s *SettingService) ordinaryRecoverySettingRevisions(ctx context.Context) (map[string]string, error) {
	revisions := make(map[string]string, 2)
	for _, key := range []string{SettingKeyUpstreamCacheGuard, SettingKeySchedulingProtectionPlatforms} {
		if s == nil || s.settingRepo == nil {
			revisions[key] = "missing"
			continue
		}
		setting, err := s.settingRepo.Get(ctx, key)
		if errors.Is(err, ErrSettingNotFound) {
			revisions[key] = "missing"
			continue
		}
		if err != nil {
			return nil, err
		}
		if setting == nil {
			return nil, upstreamcacheguard.ErrUnavailable
		}
		// Retain only a digest; neither setting contents nor credentials enter
		// the account's durable recovery metadata.
		revisions[key] = ordinaryRecoveryGenerationDigest(struct {
			Value     string
			UpdatedAt string
		}{setting.Value, setting.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return revisions, nil
}

func ordinaryRecoveryGenerationDigest(value any) string {
	encoded, _ := json.Marshal(value)
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:])
}

// Match the core's normalization without comparing map order or treating the
// legacy default spellings as a policy change. Only the small global settings
// value is inspected on each runner tick; group maps remain immutable.
func cacheGuardConfigurationIdentity(settings UpstreamCacheGuardSettings) string {
	if settings.Priority == "" {
		settings.Priority = "cache_first"
	}
	if settings.ProtectionEvidenceExpiryMinutes == 0 {
		settings.ProtectionEvidenceExpiryMinutes = 15
	}
	ids := append([]int64(nil), settings.GroupIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	settings.GroupIDs = nil
	for _, id := range ids {
		if len(settings.GroupIDs) == 0 || settings.GroupIDs[len(settings.GroupIDs)-1] != id {
			settings.GroupIDs = append(settings.GroupIDs, id)
		}
	}
	return ordinaryRecoveryGenerationDigest(settings)
}

// Equal strategies share one ordinary recovery generation, just as they share
// the released trial cohort. Group write timestamps are combined in a stable
// order, rather than allowing different groups to reset one another's progress.
func ordinaryRecoveryPolicyGenerations(global UpstreamCacheGuardSettings, policies map[int64]GroupSchedulingProtectionResolvedPolicy, records []GroupSchedulingProtectionPolicyRecord, revisions map[string]string) map[string]string {
	type member struct {
		GroupID  int64
		Updated  string
		Selected bool
		Mode     upstreamcacheguard.Mode
	}
	updated := make(map[int64]string, len(records))
	for _, record := range records {
		if record.UpdatedAt != nil {
			updated[record.TTFT.GroupID] = record.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	families := make(map[string][]member)
	families[upstreamcacheguard.PolicySignature(global)] = nil
	for id, policy := range policies {
		signature := upstreamcacheguard.PolicySignature(policy.Cache)
		families[signature] = append(families[signature], member{GroupID: id, Updated: updated[id], Selected: policy.PlatformSelected, Mode: policy.Cache.Mode})
	}
	out := make(map[string]string, len(families))
	for signature, members := range families {
		sort.Slice(members, func(i, j int) bool { return members[i].GroupID < members[j].GroupID })
		out[signature] = ordinaryRecoveryGenerationDigest(struct {
			Version   string
			Signature string
			Revisions map[string]string
			Members   []member
		}{"ordinary-recovery-config-v1", signature, revisions, members})
	}
	return out
}
