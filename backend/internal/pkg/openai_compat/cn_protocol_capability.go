package openai_compat

// CNProtocolCapability describes the last known capability of a protocol on a
// 国产 OpenAI-compatible upstream account.
type CNProtocolCapability string

const (
	CNProtocolCapabilitySupported     CNProtocolCapability = "supported"
	CNProtocolCapabilityUnsupported   CNProtocolCapability = "unsupported"
	CNProtocolCapabilityUnknown       CNProtocolCapability = "unknown"
	CNProtocolCapabilityNotConfigured CNProtocolCapability = "not_configured"

	ExtraKeyCNProtocolCapabilities = "cn_protocol_capabilities"
	CNProtocolChatCompletions      = "chat_completions"
	CNProtocolResponses            = "responses"
	CNProtocolAnthropic            = "anthropic"
)

func NormalizeCNProtocolCapability(value string) CNProtocolCapability {
	switch CNProtocolCapability(value) {
	case CNProtocolCapabilitySupported,
		CNProtocolCapabilityUnsupported,
		CNProtocolCapabilityUnknown,
		CNProtocolCapabilityNotConfigured:
		return CNProtocolCapability(value)
	default:
		return CNProtocolCapabilityUnknown
	}
}

func ResolveCNProtocolCapability(extra map[string]any, protocol string) CNProtocolCapability {
	if extra == nil {
		return CNProtocolCapabilityUnknown
	}
	raw, ok := extra[ExtraKeyCNProtocolCapabilities].(map[string]any)
	if !ok {
		return CNProtocolCapabilityUnknown
	}
	value, ok := raw[protocol].(string)
	if !ok || value == "" {
		return CNProtocolCapabilityUnknown
	}
	return NormalizeCNProtocolCapability(value)
}

// MergeCNProtocolCapability returns a copy of extra with one protocol status
// updated. The returned map is suitable for AccountRepository.UpdateExtra.
func MergeCNProtocolCapability(extra map[string]any, protocol string, capability CNProtocolCapability) map[string]any {
	merged := make(map[string]any, len(extra)+1)
	for key, value := range extra {
		merged[key] = value
	}
	capabilities := make(map[string]any, 3)
	if current, ok := extra[ExtraKeyCNProtocolCapabilities].(map[string]any); ok {
		for key, value := range current {
			capabilities[key] = value
		}
	}
	capabilities[protocol] = string(NormalizeCNProtocolCapability(string(capability)))
	merged[ExtraKeyCNProtocolCapabilities] = capabilities
	return merged
}
