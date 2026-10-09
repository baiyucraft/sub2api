package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

const ConfidenceDistributionIdentityVersion = 3

// An identity-contract marker, never sent upstream. It must not follow the
// compiled client version, or an application upgrade would reopen the window.
const distributionClientVersionMarker = "series-version"

// ConfidenceDistributionIdentity is internal request identity, never a public
// API value. Digests and a public client-version generator input are persisted;
// raw credentials and headers stay local.
type ConfidenceDistributionIdentity struct {
	Version           int               `json:"-"`
	Fingerprint       string            `json:"-"`
	Components        map[string]string `json:"-"`
	Protocol          string            `json:"-"`
	BaselineVersion   string            `json:"-"`
	LegacyFingerprint string            `json:"-"`
	LegacyCompatible  bool              `json:"-"`
	V2Fingerprint     string            `json:"-"`
	ClientVersion     string            `json:"-"`
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
	clientVersion, manualVersion := "", ""
	if protocol == "responses" {
		// Random window IDs are added after identity resolution, during dispatch.
		ensureCodexIdentityHeaders(headers)
		clientVersion = codexClientVersionFromUA(headers.Get("User-Agent"))
		_, uaOverridden := snapshot.HeaderOverrideValue("user-agent")
		_, versionOverridden := snapshot.HeaderOverrideValue("version")
		if (!uaOverridden || !versionOverridden) && s.settingService != nil && s.settingService.settingRepo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			value, readErr := s.settingService.settingRepo.GetValue(ctx, SettingKeyOpenAICodexClientVersion)
			cancel()
			if readErr != nil && !errors.Is(readErr, ErrSettingNotFound) {
				return nil, &distributionConfigurationError{"configuration_error", "probe_client_identity_unavailable", errors.New("probe client identity policy is unavailable")}
			}
			manualVersion = NormalizeCodexClientVersion(value)
			if manualVersion != "" && CompareVersions(manualVersion, codexUpstreamMinVersion) >= 0 {
				clientVersion = manualVersion
				headers.Set("User-Agent", openai.SetCodexUserAgentVersion(headers.Get("User-Agent"), clientVersion))
				headers.Set("version", clientVersion)
			} else {
				manualVersion = ""
				// A removed fixed policy must not seed a new series from the
				// canonical resolver's cached, formerly fixed version.
				ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
				synced, readErr := s.settingService.settingRepo.GetValue(ctx, SettingKeyOpenAICodexClientVersionSynced)
				cancel()
				if readErr != nil && !errors.Is(readErr, ErrSettingNotFound) {
					return nil, &distributionConfigurationError{"configuration_error", "probe_client_identity_unavailable", errors.New("probe client identity policy is unavailable")}
				}
				clientVersion = NormalizeCodexClientVersion(synced)
				if clientVersion == "" || CompareVersions(clientVersion, codexUpstreamMinVersion) < 0 {
					clientVersion = codexCLIVersion
				}
				headers.Set("User-Agent", openai.SetCodexUserAgentVersion(headers.Get("User-Agent"), clientVersion))
				headers.Set("version", clientVersion)
			}
		}
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
	// The v2 digest proves an existing window still describes this exact wire
	// request. Never infer the historical version from today's runtime default.
	v2Fingerprint := distributionIdentityDigest([]any{2, components})
	if protocol == "responses" {
		// Only generated version fields are normalized. Explicit effective
		// overrides remain byte-sensitive request identity, including Window-ID.
		_, uaOverridden := snapshot.HeaderOverrideValue("user-agent")
		_, versionOverridden := snapshot.HeaderOverrideValue("version")
		if !uaOverridden {
			stableHeaders["user-agent"] = []string{openai.SetCodexUserAgentVersion(distributionHeaderValue(headers, "user-agent"), distributionClientVersionMarker)}
		}
		if !versionOverridden {
			stableHeaders["version"] = []string{distributionClientVersionMarker}
		}
		components["headers"] = distributionIdentityDigest([]any{stableHeaders, manualVersion, uaOverridden, versionOverridden})
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
		V2Fingerprint: v2Fingerprint, ClientVersion: clientVersion,
	}
	return &distributionRequestSpec{identity: identity, account: snapshot, endpoint: endpoint, model: model, headers: headers}, nil
}

func distributionHeaderValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

// Apply only the automatic version fields. Account header overrides already
// applied to this snapshot must not be overwritten by the series generator.
func (spec *distributionRequestSpec) pinClientVersion(version string) error {
	if spec.identity.Protocol != "responses" {
		return nil
	}
	if NormalizeCodexClientVersion(version) != version || CompareVersions(version, codexUpstreamMinVersion) < 0 {
		return errors.New("invalid pinned distribution client version")
	}
	if _, overridden := spec.account.HeaderOverrideValue("user-agent"); !overridden {
		ua := openai.SetCodexUserAgentVersion(distributionHeaderValue(spec.headers, "user-agent"), version)
		if ua == "" {
			return errors.New("invalid distribution client template")
		}
		spec.headers.Set("User-Agent", ua)
	}
	if _, overridden := spec.account.HeaderOverrideValue("version"); !overridden {
		spec.headers.Set("version", version)
	}
	return nil
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
