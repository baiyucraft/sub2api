package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeySchedulingProtectionPlatforms = "upstream_scheduling_protection_platforms"

type schedulingProtectionPlatformsSnapshot struct{ platforms []string }

func DefaultSchedulingProtectionPlatforms() []string { return []string{PlatformOpenAI} }

func normalizeSchedulingProtectionPlatforms(platforms []string) ([]string, error) {
	out := make([]string, 0, len(platforms))
	seen := make(map[string]bool, len(platforms))
	for _, platform := range platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if !domain.IsConcretePlatform(platform) {
			return nil, infraerrors.BadRequest("INVALID_SCHEDULING_PROTECTION_PLATFORMS", "scheduling_protection_platforms must contain concrete platform IDs")
		}
		if !seen[platform] {
			seen[platform] = true
			out = append(out, platform)
		}
	}
	sort.Strings(out)
	return out, nil
}

func schedulingProtectionContainsPlatform(platforms []string, platform string) bool {
	if !domain.IsConcretePlatform(platform) {
		return false
	}
	for _, selected := range platforms {
		if selected == platform {
			return true
		}
	}
	return false
}

func (s *SettingService) SchedulingProtectionPlatformsSnapshot() []string {
	if s != nil {
		if cached, ok := s.schedulingProtectionPlatformsCache.Load().(*schedulingProtectionPlatformsSnapshot); ok && cached != nil {
			return append([]string{}, cached.platforms...)
		}
	}
	return DefaultSchedulingProtectionPlatforms()
}

func (s *SettingService) GetSchedulingProtectionPlatforms(ctx context.Context) ([]string, error) {
	platforms := DefaultSchedulingProtectionPlatforms()
	if s != nil && s.settingRepo != nil {
		s.schedulingProtectionPlatformsMu.Lock()
		defer s.schedulingProtectionPlatformsMu.Unlock()
		raw, err := s.settingRepo.GetValue(ctx, SettingKeySchedulingProtectionPlatforms)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return nil, err
		}
		if err == nil {
			if json.Unmarshal([]byte(raw), &platforms) != nil || platforms == nil {
				platforms = []string{}
			}
			var normalizeErr error
			platforms, normalizeErr = normalizeSchedulingProtectionPlatforms(platforms)
			if normalizeErr != nil {
				platforms = []string{}
			}
		}
		s.schedulingProtectionPlatformsCache.Store(&schedulingProtectionPlatformsSnapshot{platforms: append([]string{}, platforms...)})
	}
	return platforms, nil
}

func (s *SettingService) SetSchedulingProtectionPlatforms(ctx context.Context, platforms []string) error {
	platforms, err := normalizeSchedulingProtectionPlatforms(platforms)
	if err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return infraerrors.ServiceUnavailable("SCHEDULING_PROTECTION_UNAVAILABLE", "scheduling protection settings are unavailable")
	}
	raw, err := json.Marshal(platforms)
	if err != nil {
		return err
	}
	s.schedulingProtectionPlatformsMu.Lock()
	if err := s.settingRepo.Set(ctx, SettingKeySchedulingProtectionPlatforms, string(raw)); err != nil {
		s.schedulingProtectionPlatformsMu.Unlock()
		return err
	}
	s.schedulingProtectionPlatformsCache.Store(&schedulingProtectionPlatformsSnapshot{platforms: append([]string{}, platforms...)})
	s.schedulingProtectionPlatformsMu.Unlock()
	return s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
}

type SchedulingProtectionPlatformReader interface{ PlatformSelected(string) bool }

func SchedulingProtectionPlatformSelected(reader SchedulingProtectionPlatformReader, platform string) bool {
	if !domain.IsConcretePlatform(platform) {
		return false
	}
	if reader == nil {
		return platform == PlatformOpenAI
	}
	return reader.PlatformSelected(platform)
}

func groupProtectionPlatformNotSelected() error {
	return infraerrors.BadRequest("GROUP_PROTECTION_PLATFORM_NOT_SELECTED", "group platform is not selected for scheduling protection")
}
