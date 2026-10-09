package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// PlatformDescriptor is the single service-level catalog for concrete
// upstream platforms.
type PlatformDescriptor struct {
	ID             string
	Label          string
	ProbeSupported bool
	ProbeReason    string
	DefaultModels  []string
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func defaultModelIDsForRegisteredPlatform(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformAnthropic:
		return claudeDefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformComposite:
		return compositeDefaultModelsListCandidateIDs()
	case PlatformKimi:
		return cloneStrings(kimiOfficialModelIDs)
	case PlatformZhipu:
		return cloneStrings(zhipuOfficialModelIDs)
	case PlatformDeepseek:
		return cloneStrings(deepseekOfficialModelIDs)
	case PlatformMiniMax:
		return cloneStrings(minimaxOfficialModelIDs)
	case PlatformOpenCodeGo:
		return cloneStrings(DefaultOpenCodeGoModelIDs())
	case PlatformTypeSafe:
		return []string{"jev-latest"}
	case PlatformCommandCode:
		return []string{DefaultCommandCodeTestModel}
	case PlatformCline:
		return []string{DefaultClineTestModel}
	default:
		return nil
	}
}

func claudeDefaultModelIDs() []string {
	ids := make([]string, 0, len(claude.DefaultModels))
	for _, model := range claude.DefaultModels {
		ids = append(ids, model.ID)
	}
	return ids
}

// Active-probe capability is independent of platform identity. New platform
// registrations must not advertise a working probe until its transport exists.
var registeredPlatformProbeCapabilities = map[string]PlatformDescriptor{
	PlatformOpenAI:      {ProbeSupported: true},
	PlatformAnthropic:   {ProbeSupported: true},
	PlatformGemini:      {ProbeSupported: true},
	PlatformAntigravity: {ProbeSupported: true},
	PlatformGrok:        {ProbeSupported: true},
	// These providers expose the OpenAI Chat Completions contract. They use a
	// dedicated chat probe rather than the Responses probe used by OpenAI.
	PlatformKimi:       {ProbeSupported: true},
	PlatformZhipu:      {ProbeSupported: true},
	PlatformDeepseek:   {ProbeSupported: true},
	PlatformMiniMax:    {ProbeSupported: true},
	PlatformOpenCodeGo: {ProbeSupported: true},
	PlatformTypeSafe:   {ProbeReason: "System One requires a dedicated probe"},
}

func RegisteredPlatformCatalog() []PlatformDescriptor {
	platforms := domain.Platforms()
	result := make([]PlatformDescriptor, 0, len(platforms))
	for _, spec := range platforms {
		entry, registered := registeredPlatformProbeCapabilities[spec.ID]
		entry.ID, entry.Label = spec.ID, spec.DisplayName
		if !registered {
			entry.ProbeReason = "Active health probing is not implemented for this platform"
		}
		entry.DefaultModels = cloneStrings(defaultModelIDsForRegisteredPlatform(spec.ID))
		result = append(result, entry)
	}
	return result
}

func DefaultModelIDsForPlatform(platform string) []string {
	return cloneStrings(defaultModelIDsForRegisteredPlatform(strings.ToLower(strings.TrimSpace(platform))))
}

func IsConcreteRequestPlatform(platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	return domain.IsConcretePlatform(platform)
}
