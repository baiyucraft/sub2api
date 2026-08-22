package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// PlatformDescriptor is the single service-level catalog for concrete
// upstream platforms. Probe capability is intentionally independent from the
// model directory: catalog-only platforms remain visible but fail closed for
// active probing.
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

var registeredPlatformCatalog = []PlatformDescriptor{
	{ID: PlatformOpenAI, Label: "OpenAI", ProbeSupported: true},
	{ID: PlatformAnthropic, Label: "Anthropic", ProbeSupported: true},
	{ID: PlatformGemini, Label: "Gemini", ProbeSupported: true},
	{ID: PlatformAntigravity, Label: "Antigravity", ProbeReason: "该平台暂无主动探针协议"},
	{ID: PlatformGrok, Label: "Grok", ProbeReason: "该平台暂无主动探针协议"},
	{ID: PlatformKimi, Label: "Kimi", ProbeReason: "该平台暂无主动探针协议"},
	{ID: PlatformZhipu, Label: "Zhipu GLM", ProbeReason: "该平台暂无主动探针协议"},
	{ID: PlatformDeepseek, Label: "DeepSeek", ProbeReason: "该平台暂无主动探针协议"},
}

func RegisteredPlatformCatalog() []PlatformDescriptor {
	result := make([]PlatformDescriptor, 0, len(registeredPlatformCatalog))
	for _, entry := range registeredPlatformCatalog {
		copy := entry
		copy.DefaultModels = cloneStrings(defaultModelIDsForRegisteredPlatform(entry.ID))
		result = append(result, copy)
	}
	return result
}

func DefaultModelIDsForPlatform(platform string) []string {
	return cloneStrings(defaultModelIDsForRegisteredPlatform(strings.ToLower(strings.TrimSpace(platform))))
}

func IsConcreteRequestPlatform(platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	for _, entry := range registeredPlatformCatalog {
		if entry.ID == platform {
			return true
		}
	}
	return false
}
