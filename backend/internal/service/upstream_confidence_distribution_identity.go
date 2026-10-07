package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

const ConfidenceDistributionIdentityVersion = 2

// ConfidenceDistributionIdentity is internal request identity, never a public
// API value. Only digests are persisted; raw credentials and headers stay local.
type ConfidenceDistributionIdentity struct {
	Version           int               `json:"-"`
	Fingerprint       string            `json:"-"`
	Components        map[string]string `json:"-"`
	Protocol          string            `json:"-"`
	BaselineVersion   string            `json:"-"`
	LegacyFingerprint string            `json:"-"`
	LegacyCompatible  bool              `json:"-"`
}

type distributionRequestSpec struct {
	identity ConfidenceDistributionIdentity
	account  Account
	endpoint string
	model    string
	headers  http.Header
}

type distributionConfigurationError struct {
	result, reason string
	err            error
}

func (e *distributionConfigurationError) Error() string { return e.err.Error() }

func distributionAccountProtocol(account *Account) string {
	if !openai_compat.ShouldUseResponsesAPI(account.Extra) {
		return "chat_completions"
	}
	return "responses"
}

// Retain the exact pre-v2 algorithm solely to verify a legacy window upgrade.
func distributionAccountFingerprint(account *Account) string {
	protocol := distributionAccountProtocol(account)
	return distributionIdentityDigest(map[string]any{
		"account_id": account.ID, "key_id": account.UpstreamKeyID,
		"protocol": protocol, "credentials": account.Credentials,
		"effective_model": account.GetMappedModel(UpstreamConfidenceDistributionClaimedModel),
		"contract":        UpstreamConfidenceDistributionPromptVersion,
		"baseline":        DistributionBaselineVersion(protocol), "proxy_id": account.ProxyID,
	})
}

func distributionIdentityDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// ResolveConfidenceDistributionIdentity shares exactly the same checks and
// normalization as dispatch, but neither claims a slot nor generates a request.
func (s *AccountTestService) ResolveConfidenceDistributionIdentity(account *Account) (ConfidenceDistributionIdentity, error) {
	spec, err := s.resolveDistributionRequest(account)
	if err != nil {
		return ConfidenceDistributionIdentity{}, err
	}
	return spec.identity, nil
}

func (s *AccountTestService) resolveDistributionRequest(account *Account) (*distributionRequestSpec, error) {
	if account == nil {
		return nil, errors.New("confidence distribution account is missing")
	}
	// The request and proxy dispatch must use the configuration observed before
	// claiming a durable slot, even if live configuration changes during Claim.
	snapshot := *account
	snapshot.Credentials = cloneDistributionMap(account.Credentials)
	snapshot.Extra = cloneDistributionMap(account.Extra)
	snapshot.ProxyID = cloneDistributionID(account.ProxyID)
	snapshot.ProxyIPGroupID = cloneDistributionID(account.ProxyIPGroupID)
	snapshot.UpstreamKeyID = cloneDistributionID(account.UpstreamKeyID)
	snapshot.UpstreamConfigID = cloneDistributionID(account.UpstreamConfigID)
	if account.Proxy != nil {
		proxy := *account.Proxy
		snapshot.Proxy = &proxy
	}
	protocol := distributionAccountProtocol(&snapshot)
	model := snapshot.GetMappedModel(UpstreamConfidenceDistributionClaimedModel)
	if strings.TrimSpace(model) == "" || !snapshot.IsModelSupported(UpstreamConfidenceDistributionClaimedModel) || isTextProbeUnsupportedModel(model) {
		return nil, &distributionConfigurationError{"unsupported_model", "probe_model_unsupported", errors.New("account does not support the Sol distribution probe model")}
	}
	apiKey, baseURL := snapshot.GetOpenAIApiKey(), snapshot.GetOpenAIBaseURL()
	if protocol == "chat_completions" {
		apiKey, baseURL = snapshot.GetOpenAIProtocolAPIKey(), snapshot.GetOpenAIFormatBaseURL()
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, &distributionConfigurationError{"configuration_error", "probe_credentials_missing", errors.New("OpenAI probe credentials are missing")}
	}
	validated, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, &distributionConfigurationError{"configuration_error", "probe_base_url_invalid", err}
	}
	endpoint := buildOpenAIResponsesURL(validated)
	if protocol == "chat_completions" {
		endpoint = buildOpenAIChatCompletionsURL(validated)
	}
	endpoint, err = normalizeDistributionEndpoint(endpoint)
	if err != nil {
		return nil, &distributionConfigurationError{"request_error", "probe_request_invalid", err}
	}
	headers := make(http.Header)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("Authorization", "Bearer "+apiKey)
	if protocol == "responses" {
		// Random window IDs are added after identity resolution, during dispatch.
		ensureCodexIdentityHeaders(headers)
	}
	snapshot.ApplyHeaderOverrides(headers)
	stableHeaders := make(map[string][]string, len(headers))
	for name, values := range headers {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		stableHeaders[strings.ToLower(name)] = append([]string(nil), values...)
	}
	proxyURL := ""
	if snapshot.ProxyID != nil && snapshot.Proxy != nil {
		proxyURL = snapshot.Proxy.URL()
	}
	components := map[string]string{
		"binding":    distributionIdentityDigest([]any{snapshot.ID, snapshot.UpstreamKeyID}),
		"protocol":   distributionIdentityDigest(protocol),
		"endpoint":   distributionIdentityDigest(endpoint),
		"credential": distributionIdentityDigest(apiKey),
		"model":      distributionIdentityDigest(model),
		"proxy":      distributionIdentityDigest([]any{snapshot.ProxyID, snapshot.ProxyIPGroupID, proxyURL}),
		"headers":    distributionIdentityDigest(stableHeaders),
		"contract":   distributionIdentityDigest(UpstreamConfidenceDistributionPromptVersion),
		"baseline":   distributionIdentityDigest(DistributionBaselineVersion(protocol)),
	}
	for _, digest := range components {
		if digest == "" {
			return nil, errors.New("invalid confidence distribution request identity")
		}
	}
	// Header overrides were part of legacy Credentials, so an exact legacy
	// fingerprint proves their continuity. Proxy contents and runtime defaults
	// were not; only accept those defaults when they cannot change independently.
	overrides := snapshot.GetHeaderOverrides()
	legacyCompatible := snapshot.ProxyID == nil && snapshot.ProxyIPGroupID == nil
	if protocol == "responses" {
		codexCanonicalUAMu.RLock()
		staticDefaults := codexCanonicalUAResolver == nil
		codexCanonicalUAMu.RUnlock()
		legacyCompatible = legacyCompatible && (staticDefaults || (overrides["user-agent"] != "" && overrides["originator"] != "" && overrides["version"] != ""))
	}
	identity := ConfidenceDistributionIdentity{
		Version:     ConfidenceDistributionIdentityVersion,
		Fingerprint: distributionIdentityDigest([]any{ConfidenceDistributionIdentityVersion, components}),
		Components:  components, Protocol: protocol, BaselineVersion: DistributionBaselineVersion(protocol),
		LegacyFingerprint: distributionAccountFingerprint(&snapshot), LegacyCompatible: legacyCompatible,
	}
	return &distributionRequestSpec{identity: identity, account: snapshot, endpoint: endpoint, model: model, headers: headers}, nil
}

func normalizeDistributionEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid confidence distribution endpoint")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host, port := strings.ToLower(parsed.Hostname()), parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func cloneDistributionID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneDistributionMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = cloneDistributionValue(value)
	}
	return copy
}

func cloneDistributionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneDistributionMap(typed)
	case map[string]string:
		copy := make(map[string]string, len(typed))
		for key, value := range typed {
			copy[key] = value
		}
		return copy
	case []any:
		copy := make([]any, len(typed))
		for i, value := range typed {
			copy[i] = cloneDistributionValue(value)
		}
		return copy
	default:
		return value
	}
}
