package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/service/modeltrace_numeric"
)

const (
	ConfidenceFingerprintIdentityVersion       = 1
	ConfidenceFingerprintPolicyVersion         = 1
	ConfidenceFingerprintSamplingPolicyVersion = 3
	ConfidenceFingerprintSampleLimit           = 64
	ConfidenceFingerprintMinimumSamples        = 12
	confidenceFingerprintLease                 = 2 * time.Minute
)

type confidenceFingerprintAttemptContextKey struct{}
type confidenceFingerprintUnavailableContextKey struct{}

type ConfidenceFingerprintEvidence struct {
	ProtocolStatus string    `json:"protocol_status"`
	ProtocolReason string    `json:"protocol_reason,omitempty"`
	Route          string    `json:"route,omitempty"`
	Shape          string    `json:"shape,omitempty"`
	FinishClass    string    `json:"finish_class,omitempty"`
	OutputClass    string    `json:"output_class,omitempty"`
	Reasoning      bool      `json:"reasoning,omitempty"`
	ChallengeIndex int       `json:"challenge_index,omitempty"`
	MethodSequence int64     `json:"method_sequence,omitempty"`
	Feature        []float64 `json:"reference_feature,omitempty"`
	Numbers        []int     `json:"reference_numbers,omitempty"`
}

type ConfidenceFingerprintIdentity struct {
	Version     int               `json:"version"`
	Fingerprint string            `json:"fingerprint"`
	Components  map[string]string `json:"components,omitempty"`
	Platform    string            `json:"platform"`
	Model       string            `json:"model"`
	TargetModel string            `json:"target_model,omitempty"`
	Protocol    string            `json:"protocol"`
	Endpoint    string            `json:"endpoint"`
	Baseline    string            `json:"baseline_version"`
	Challenge   string            `json:"challenge_version"`
	Normalize   string            `json:"normalization_version"`
}

type ConfidenceFingerprintSample struct {
	ObservedAt     time.Time                     `json:"observed_at"`
	Method         string                        `json:"method"`
	MethodSequence int64                         `json:"method_sequence,omitempty"`
	Evidence       ConfidenceFingerprintEvidence `json:"evidence"`
}

type ConfidenceFingerprintAttempt struct {
	SeriesID       string    `json:"series_id"`
	Token          string    `json:"token"`
	Method         string    `json:"method"`
	ObservedAt     time.Time `json:"observed_at"`
	Sequence       int64     `json:"sequence"`
	MethodSequence int64     `json:"method_sequence,omitempty"`
}

type ConfidenceFingerprintState struct {
	PolicyVersion         int                           `json:"policy_version"`
	SamplingPolicyVersion int                           `json:"sampling_policy_version,omitempty"`
	SeriesID              string                        `json:"series_id"`
	Fingerprint           string                        `json:"fingerprint"`
	Components            map[string]string             `json:"components,omitempty"`
	NextSequence          int64                         `json:"next_sequence"`
	MethodSequences       map[string]int64              `json:"method_sequences,omitempty"`
	Pending               *ConfidenceFingerprintAttempt `json:"pending,omitempty"`
	LeaseExpires          time.Time                     `json:"lease_expires,omitempty"`
	Samples               []ConfidenceFingerprintSample `json:"samples"`
	LastProtocolEvidence  *ConfidenceFingerprintSample  `json:"last_protocol_evidence,omitempty"`
}

type ConfidenceFingerprintMethodSummary struct {
	Status             string                           `json:"status"`
	SampleCount        int                              `json:"sample_count"`
	MinimumSamples     int                              `json:"minimum_samples"`
	BaselineVersion    string                           `json:"baseline_version,omitempty"`
	Reason             string                           `json:"reason,omitempty"`
	Candidates         []ConfidenceFingerprintCandidate `json:"candidates,omitempty"`
	Weight             int                              `json:"weight,omitempty"`
	EffectiveWeight    int                              `json:"effective_weight,omitempty"`
	Participating      bool                             `json:"participating"`
	CandidateModel     string                           `json:"candidate_model,omitempty"`
	ConfirmedRounds    int                              `json:"confirmed_rounds,omitempty"`
	CurrentSampleCount int                              `json:"current_sample_count,omitempty"`
}

type ConfidenceFingerprintCandidate struct {
	ModelID          string  `json:"model_id"`
	DisplayName      string  `json:"display_name"`
	Score            float64 `json:"score,omitempty"`
	RelativeFit      float64 `json:"relative_fit,omitempty"`
	ClosedSetSupport float64 `json:"closed_set_support,omitempty"`
	SampleSize       int     `json:"sample_size,omitempty"`
}

type ConfidenceFingerprintAttribution struct {
	Status           string                                        `json:"status"`
	Reason           string                                        `json:"reason,omitempty"`
	Platform         string                                        `json:"platform,omitempty"`
	Model            string                                        `json:"model,omitempty"`
	TargetModel      string                                        `json:"target_model,omitempty"`
	Protocol         string                                        `json:"protocol,omitempty"`
	ObservedProtocol string                                        `json:"observed_protocol,omitempty"`
	ProtocolStatus   string                                        `json:"protocol_status,omitempty"`
	ProtocolReason   string                                        `json:"protocol_reason,omitempty"`
	Methods          map[string]ConfidenceFingerprintMethodSummary `json:"methods,omitempty"`
}

type ConfidenceFingerprintRepository interface {
	ClaimConfidenceFingerprint(context.Context, int64, ConfidenceFingerprintIdentity, time.Time) (*ConfidenceFingerprintAttempt, error)
	FinishConfidenceFingerprint(context.Context, int64, *ConfidenceFingerprintAttempt, ConfidenceFingerprintIdentity, ConfidenceFingerprintEvidence, time.Time) error
	RecordConfidenceFingerprintSample(context.Context, int64, ConfidenceFingerprintIdentity, string, ConfidenceFingerprintEvidence, time.Time) error
	LoadConfidenceFingerprints(context.Context, map[int64]ConfidenceFingerprintIdentity) (map[int64]ConfidenceFingerprintReadResult, error)
}

type ConfidenceFingerprintReadResult struct {
	State *ConfidenceFingerprintState
	Err   error
}

// ConfidenceFingerprintBaselineVersion identifies the pinned reference
// baselines applicable to an exact platform, model, and protocol. Their
// rankings remain reference evidence, not calibrated identity decisions.
func ConfidenceFingerprintBaselineVersion(platform, model, protocol string) string {
	baselines := confidenceFingerprintBaselines(platform, model, protocol)
	parts := make([]string, 0, 2)
	if baselines["chanshui_behavioral"] != "" {
		parts = append(parts, baselines["chanshui_behavioral"])
	}
	if baselines["modeltrace_numeric"] != "" {
		parts = append(parts, baselines["modeltrace_numeric"])
	}
	return strings.Join(parts, "+")
}

func confidenceFingerprintBaselines(platform, model, protocol string) map[string]string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	model = strings.ToLower(strings.TrimSpace(model))
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if !referenceFingerprintProtocolSupported(platform, protocol) {
		return map[string]string{}
	}
	result := make(map[string]string, 2)
	chanshuiPlatform := map[string]string{
		PlatformOpenAI: "openai", PlatformAnthropic: "anthropic", PlatformDeepseek: "deepseek",
		PlatformGemini: "google", PlatformGrok: "x-ai", PlatformZhipu: "z-ai",
	}[platform]
	if chanshuiPlatform != "" {
		chanshuiModelID := model
		if prefix, _, ok := strings.Cut(model, "/"); ok {
			if prefix != chanshuiPlatform {
				chanshuiModelID = ""
			}
		} else {
			chanshuiModelID = chanshuiPlatform + "/" + model
		}
		if chanshuiModelID != "" && HasChanshuiBehavioralBaseline(chanshuiModelID) {
			result["chanshui_behavioral"] = ChanshuiBehavioralBaselineVersion
		}
	}
	if modeltracePlatformMatches(platform, model) && modeltrace_numeric.HasTarget(model) {
		result["modeltrace_numeric"] = "modeltrace-numeric-" + modeltrace_numeric.SourceCommit[:7]
	}
	return result
}

func modeltracePlatformMatches(platform, model string) bool {
	switch {
	case strings.HasPrefix(model, "gpt-"):
		return platform == PlatformOpenAI
	case strings.HasPrefix(model, "claude-"):
		return platform == PlatformAnthropic
	default:
		return false
	}
}

func referenceFingerprintProtocolSupported(platform, protocol string) bool {
	switch platform {
	case PlatformOpenAI:
		return protocol == upstreamHealthProbeProtocolOpenAI || protocol == upstreamHealthProbeProtocolOpenAIChat
	case PlatformAnthropic:
		return protocol == upstreamHealthProbeProtocolAnthropic
	case PlatformGemini:
		return protocol == upstreamHealthProbeProtocolGemini
	case PlatformGrok:
		return protocol == upstreamHealthProbeProtocolGrok
	case PlatformDeepseek, PlatformZhipu, PlatformKimi, PlatformMiniMax, PlatformOpenCodeGo:
		return protocol == upstreamHealthProbeProtocolOpenAI || protocol == upstreamHealthProbeProtocolOpenAIChat || protocol == upstreamHealthProbeProtocolAnthropic
	default:
		return false
	}
}

func NewConfidenceFingerprintIdentity(platform, model, protocol, endpoint string) ConfidenceFingerprintIdentity {
	platform = strings.ToLower(strings.TrimSpace(platform))
	model = strings.TrimSpace(model)
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	endpoint = confidenceFingerprintEndpointIdentity(endpoint)
	components := map[string]string{
		"platform": platform, "model": model, "protocol": protocol,
		"endpoint": endpoint, "challenge": "health-structure-v1", "normalization": "response-shape-v1",
	}
	baselines := confidenceFingerprintBaselines(platform, model, protocol)
	for method, version := range baselines {
		components["baseline_"+method] = version
	}
	baseline := ConfidenceFingerprintBaselineVersion(platform, model, protocol)
	components["baseline"] = baseline
	challenge, normalization := "health-structure-v1", "response-shape-v1"
	if len(baselines) > 0 {
		challenge, normalization = "reference-probe-suite-v1", "reference-feature-v1"
		components["challenge"], components["normalization"] = challenge, normalization
	}
	raw, _ := json.Marshal(components)
	digest := sha256.Sum256(raw)
	return ConfidenceFingerprintIdentity{
		Version: ConfidenceFingerprintIdentityVersion, Fingerprint: hex.EncodeToString(digest[:]), Components: components,
		Platform: platform, Model: model, Protocol: protocol, Endpoint: endpoint, Baseline: baseline,
		Challenge: challenge, Normalize: normalization,
	}
}

func ConfidenceFingerprintIdentityForAccount(account *Account, requestedModel string) (ConfidenceFingerprintIdentity, bool) {
	return ConfidenceFingerprintIdentityForProtocol(account, requestedModel, "")
}

func ConfidenceFingerprintIdentityForProtocol(account *Account, requestedModel, protocolOverride string) (ConfidenceFingerprintIdentity, bool) {
	if account == nil {
		return ConfidenceFingerprintIdentity{}, false
	}
	identity, ok := ConfidenceFingerprintIdentityForEffectiveModel(account, account.GetMappedModel(requestedModel), protocolOverride)
	if !ok {
		return identity, false
	}
	return bindConfidenceFingerprintTarget(identity, requestedModel), true
}

func bindConfidenceFingerprintTarget(identity ConfidenceFingerprintIdentity, requestedModel string) ConfidenceFingerprintIdentity {
	identity.TargetModel = strings.TrimSpace(requestedModel)
	// Preserve compatible series hashes. A genuinely different requested target
	// must not reuse a mapped model's observations as if that target was tested.
	if canonicalConfidenceModel(identity.TargetModel) != canonicalConfidenceModel(identity.Model) {
		identity.Components["requested_model"] = identity.TargetModel
		identity = finalizeConfidenceFingerprintIdentity(identity)
	}
	return identity
}

func ConfidenceFingerprintIdentityForEffectiveModel(account *Account, effectiveModel, protocolOverride string) (ConfidenceFingerprintIdentity, bool) {
	if account == nil || account.Type != AccountTypeAPIKey {
		return ConfidenceFingerprintIdentity{}, false
	}
	platform := strings.ToLower(strings.TrimSpace(account.Platform))
	model := strings.TrimSpace(effectiveModel)
	if model == "" || !UpstreamProbePlatformSupported(platform) {
		return ConfidenceFingerprintIdentity{}, false
	}
	if account.IsCNProvider() && account.IsAdaptiveAPIProtocol() &&
		(protocolOverride == "" || protocolOverride == upstreamHealthProbeProtocolAdaptive) {
		identity := NewConfidenceFingerprintIdentity(platform, model, "adaptive_multi_protocol", "https://confidence.invalid")
		identity.Endpoint = confidenceFingerprintAdaptiveEndpointIdentity(account)
		identity.Components["endpoint"] = identity.Endpoint
		return bindConfidenceFingerprintAccount(identity, account)
	}
	protocol, endpoint := "", ""
	switch platform {
	case PlatformOpenAI:
		if distributionAccountProtocol(account) == "chat_completions" {
			protocol, endpoint = upstreamHealthProbeProtocolOpenAIChat, account.GetOpenAIFormatBaseURL()
		} else {
			protocol, endpoint = upstreamHealthProbeProtocolOpenAI, account.GetOpenAIBaseURL()
		}
	case PlatformAnthropic:
		protocol, endpoint = "anthropic_messages", account.GetAnthropicProtocolBaseURL()
	case PlatformGemini:
		protocol, endpoint = upstreamHealthProbeProtocolGemini, account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	case PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax:
		apiProtocol := account.GetAPIProtocol()
		switch apiProtocol {
		case APIProtocolAnthropic:
			protocol, endpoint = upstreamHealthProbeProtocolAnthropic, account.GetAnthropicProtocolBaseURL()
		case APIProtocolResponses:
			protocol, endpoint = upstreamHealthProbeProtocolOpenAI, account.GetOpenAIBaseURL()
		default:
			protocol, endpoint = upstreamHealthProbeProtocolOpenAIChat, account.GetOpenAIFormatBaseURL()
		}
	case PlatformOpenCodeGo:
		apiProtocol := account.GetAPIProtocol()
		if apiProtocol != APIProtocolChatCompletions && apiProtocol != APIProtocolAnthropic && apiProtocol != APIProtocolResponses {
			apiProtocol = account.resolveModelRoutedProtocol(model)
		}
		switch apiProtocol {
		case APIProtocolAnthropic:
			protocol, endpoint = upstreamHealthProbeProtocolAnthropic, account.GetBaseURL()
		case APIProtocolResponses:
			protocol, endpoint = upstreamHealthProbeProtocolOpenAI, account.GetOpenAIBaseURL()
		default:
			protocol, endpoint = upstreamHealthProbeProtocolOpenAIChat, account.GetOpenAIFormatBaseURL()
		}
	case PlatformGrok:
		protocol, endpoint = upstreamHealthProbeProtocolGrok, account.GetGrokBaseURL()
	case PlatformAntigravity:
		protocol, endpoint = upstreamHealthProbeProtocolAntigravity, account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	default:
		return ConfidenceFingerprintIdentity{}, false
	}
	if protocolOverride != "" && protocolOverride != upstreamHealthProbeProtocolAdaptive {
		protocol = protocolOverride
		switch protocol {
		case upstreamHealthProbeProtocolOpenAI:
			if account.IsAdaptiveAPIProtocol() {
				endpoint = account.GetCNProtocolBaseURL(APIProtocolResponses)
			} else {
				endpoint = account.GetOpenAIBaseURL()
			}
		case upstreamHealthProbeProtocolOpenAIChat:
			if account.IsAdaptiveAPIProtocol() {
				endpoint = account.GetCNProtocolBaseURL(APIProtocolChatCompletions)
			} else {
				endpoint = account.GetOpenAIFormatBaseURL()
			}
		case upstreamHealthProbeProtocolAnthropic:
			endpoint = account.GetAnthropicProtocolBaseURL()
		case upstreamHealthProbeProtocolGemini:
			endpoint = account.GetCredential("base_url")
			if endpoint == "" {
				endpoint = "https://generativelanguage.googleapis.com"
			}
		case upstreamHealthProbeProtocolGrok:
			endpoint = account.GetGrokBaseURL()
		case upstreamHealthProbeProtocolAntigravity:
			endpoint = account.GetCredential("base_url")
		}
	}
	identity := NewConfidenceFingerprintIdentity(platform, model, protocol, endpoint)
	return bindConfidenceFingerprintAccount(identity, account)
}

func bindConfidenceFingerprintAccount(identity ConfidenceFingerprintIdentity, account *Account) (ConfidenceFingerprintIdentity, bool) {
	identity.Components = cloneFingerprintComponents(identity.Components)
	if identity.Components == nil {
		identity.Components = make(map[string]string)
	}
	identity.Components["account_id"] = fmt.Sprintf("%d", account.ID)
	if account.UpstreamKeyID != nil {
		identity.Components["upstream_key_id"] = fmt.Sprintf("%d", *account.UpstreamKeyID)
	}
	credential := account.GetOpenAIProtocolAPIKey()
	if credential == "" {
		credential = account.GetCredential("api_key")
	}
	if credential == "" {
		return ConfidenceFingerprintIdentity{}, false
	}
	credentialDigest := sha256.Sum256([]byte(credential))
	identity.Components["credential"] = hex.EncodeToString(credentialDigest[:])
	return finalizeConfidenceFingerprintIdentity(identity), true
}

func finalizeConfidenceFingerprintIdentity(identity ConfidenceFingerprintIdentity) ConfidenceFingerprintIdentity {
	raw, _ := json.Marshal(identity.Components)
	digest := sha256.Sum256(raw)
	identity.Fingerprint = hex.EncodeToString(digest[:])
	return identity
}

func confidenceFingerprintAdaptiveEndpointIdentity(account *Account) string {
	endpoints := map[string]string{
		APIProtocolChatCompletions: confidenceFingerprintEndpointIdentity(account.GetCNProtocolBaseURL(APIProtocolChatCompletions)),
	}
	if account.Platform != PlatformZhipu {
		endpoints[APIProtocolResponses] = confidenceFingerprintEndpointIdentity(account.GetCNProtocolBaseURL(APIProtocolResponses))
	}
	if account.Platform != PlatformZhipu && account.HasExplicitCNProtocolBaseURL(APIProtocolAnthropic) {
		endpoints[APIProtocolAnthropic] = confidenceFingerprintEndpointIdentity(account.GetCNProtocolBaseURL(APIProtocolAnthropic))
	}
	raw, _ := json.Marshal(endpoints)
	digest := sha256.Sum256(append([]byte("cn-adaptive-endpoints-v1:"), raw...))
	return hex.EncodeToString(digest[:])
}

func confidenceFingerprintEndpointIdentity(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	canonical := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func (s *ConfidenceFingerprintState) Validate() error {
	if s == nil || s.PolicyVersion != ConfidenceFingerprintPolicyVersion || strings.TrimSpace(s.SeriesID) == "" || strings.TrimSpace(s.Fingerprint) == "" || s.NextSequence < 1 {
		return errors.New("invalid confidence fingerprint state")
	}
	if len(s.Samples) > ConfidenceFingerprintSampleLimit {
		return errors.New("confidence fingerprint sample limit exceeded")
	}
	return nil
}

func (s *ConfidenceFingerprintState) Claim(now time.Time) (*ConfidenceFingerprintAttempt, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Pending != nil && s.LeaseExpires.After(now) {
		return nil, nil
	}
	// Older JSON has no sampling version. Keep its sequence, samples, and any
	// in-flight attempt intact while moving future claims to the current policy.
	s.SamplingPolicyVersion = ConfidenceFingerprintSamplingPolicyVersion
	s.trimExpiredSamples(now)
	method := s.samplingMethod(s.NextSequence)
	if s.MethodSequences == nil {
		s.MethodSequences = make(map[string]int64)
	}
	methodSequence := int64(0)
	if method == "chanshui_behavioral" || method == "modeltrace_numeric" {
		s.MethodSequences[method]++
		methodSequence = s.MethodSequences[method]
	}
	var token [24]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		return nil, fmt.Errorf("create fingerprint probe lease: %w", err)
	}
	attempt := &ConfidenceFingerprintAttempt{
		SeriesID: s.SeriesID, Token: hex.EncodeToString(token[:]), Method: method,
		ObservedAt: now.UTC(), Sequence: s.NextSequence, MethodSequence: methodSequence,
	}
	s.NextSequence++
	s.Pending = attempt
	s.LeaseExpires = now.UTC().Add(confidenceFingerprintLease)
	return attempt, nil
}

func (s *ConfidenceFingerprintState) samplingMethod(sequence int64) string {
	methods := make([]string, 0, 2)
	if strings.TrimSpace(s.Components["baseline_chanshui_behavioral"]) != "" {
		methods = append(methods, "chanshui_behavioral")
	}
	if strings.TrimSpace(s.Components["baseline_modeltrace_numeric"]) != "" {
		methods = append(methods, "modeltrace_numeric")
	}
	distributionSupported := DistributionProbeSupportsModel(identityPlatformFromComponents(s.Components), s.Components["model"], s.Components["protocol"])
	if len(methods) == 0 {
		if distributionSupported {
			return "distribution"
		}
		return "protocol"
	}
	if !distributionSupported {
		return methods[(sequence-1)%int64(len(methods))]
	}

	// Each series gets one reproducible behavioral slot in every ten probes.
	// Hashing the series and block spreads that slot without mutable RNG state.
	block := (sequence - 1) / 10
	digest := sha256.Sum256([]byte(fmt.Sprintf("confidence-fingerprint-sampling-v2:%s:%d", s.SeriesID, block)))
	behavioralSlot := int64(digest[0] % 10)
	if (sequence-1)%10 == behavioralSlot {
		return methods[block%int64(len(methods))]
	}
	return "distribution"
}

func (s *ConfidenceFingerprintState) trimExpiredSamples(now time.Time) {
	cutoff := now.UTC().Add(-UpstreamHealthObservationRetention)
	kept := s.Samples[:0]
	for _, sample := range s.Samples {
		if !sample.ObservedAt.Before(cutoff) {
			kept = append(kept, sample)
		}
	}
	s.Samples = kept
}

func identityPlatformFromComponents(components map[string]string) string {
	return strings.TrimSpace(components["platform"])
}

func (s *ConfidenceFingerprintState) Finish(attempt *ConfidenceFingerprintAttempt, evidence ConfidenceFingerprintEvidence, now time.Time) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if attempt == nil || s.Pending == nil || !s.LeaseExpires.After(now) || attempt.SeriesID != s.SeriesID || attempt.Token != s.Pending.Token || attempt.Sequence != s.Pending.Sequence || attempt.Method != s.Pending.Method || attempt.MethodSequence != s.Pending.MethodSequence || !attempt.ObservedAt.Equal(s.Pending.ObservedAt) {
		return errors.New("confidence fingerprint lease lost")
	}
	evidence.MethodSequence = attempt.MethodSequence
	if isReferenceFingerprintMethod(attempt.Method) && attempt.MethodSequence > 0 {
		evidence.ChallengeIndex = int((attempt.MethodSequence - 1) % 3)
	}
	if err := s.recordEvidence(attempt.Method, attempt.MethodSequence, attempt.ObservedAt, evidence); err != nil {
		return err
	}
	s.Pending = nil
	s.LeaseExpires = time.Time{}
	return nil
}

func addReferenceFingerprintPayload(evidence *ConfidenceFingerprintEvidence, attempt *ConfidenceFingerprintAttempt, response string) {
	if evidence == nil || !referenceFingerprintEvidenceUsable(*evidence) || attempt == nil || !isReferenceFingerprintMethod(attempt.Method) || attempt.MethodSequence <= 0 || strings.TrimSpace(response) == "" {
		return
	}
	index := int((attempt.MethodSequence - 1) % 3)
	evidence.MethodSequence = attempt.MethodSequence
	evidence.ChallengeIndex = index
	switch attempt.Method {
	case "chanshui_behavioral":
		feature, err := ChanshuiBehaviorFeatureForChallenge(index, response)
		if err == nil {
			evidence.Feature = feature
		}
	case "modeltrace_numeric":
		numbers := modeltrace_numeric.ParseNumbers(response)
		minimum, err := modeltrace_numeric.MinimumForChallenge(index)
		if err == nil && len(numbers) >= minimum {
			evidence.Numbers = numbers
		}
	}
}

func referenceFingerprintEvidenceUsable(evidence ConfidenceFingerprintEvidence) bool {
	if evidence.ProtocolStatus != "compatible" {
		return false
	}
	_, finish, _ := strings.Cut(strings.ToLower(strings.TrimSpace(evidence.FinishClass)), ":")
	switch finish {
	case "length", "max_tokens", "max_output_tokens", "incomplete", "content_filter", "refusal", "failed", "cancelled":
		return false
	default:
		return true
	}
}

func (s *ConfidenceFingerprintState) recordEvidence(method string, methodSequence int64, observedAt time.Time, evidence ConfidenceFingerprintEvidence) error {
	sample := ConfidenceFingerprintSample{ObservedAt: observedAt.UTC(), Method: method, MethodSequence: methodSequence, Evidence: evidence}
	if evidence.ProtocolReason != "probe_not_dispatched" && evidence.ProtocolReason != "probe_not_due" {
		protocolSample := sample
		protocolSample.Method = "protocol"
		s.LastProtocolEvidence = &protocolSample
	}
	switch method {
	case "behavioral", "chanshui_behavioral", "modeltrace_numeric":
		s.Samples = append(s.Samples, sample)
		if len(s.Samples) > ConfidenceFingerprintSampleLimit {
			s.Samples = append([]ConfidenceFingerprintSample(nil), s.Samples[len(s.Samples)-ConfidenceFingerprintSampleLimit:]...)
		}
	case "protocol", "distribution":
	default:
		return fmt.Errorf("unknown confidence fingerprint sample method %q", method)
	}
	return nil
}

func (s *ConfidenceFingerprintState) RecordEvidence(method string, observedAt time.Time, evidence ConfidenceFingerprintEvidence) error {
	return s.recordEvidence(method, 0, observedAt, evidence)
}

func (s *ConfidenceFingerprintState) RecordMethodEvidence(method string, methodSequence int64, observedAt time.Time, evidence ConfidenceFingerprintEvidence) error {
	return s.recordEvidence(method, methodSequence, observedAt, evidence)
}

func NewConfidenceFingerprintState(identity ConfidenceFingerprintIdentity) (*ConfidenceFingerprintState, error) {
	if identity.Version != ConfidenceFingerprintIdentityVersion || identity.Fingerprint == "" {
		return nil, errors.New("invalid confidence fingerprint identity")
	}
	var token [18]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		return nil, fmt.Errorf("create fingerprint series: %w", err)
	}
	return &ConfidenceFingerprintState{
		PolicyVersion: ConfidenceFingerprintPolicyVersion, SamplingPolicyVersion: ConfidenceFingerprintSamplingPolicyVersion,
		SeriesID: hex.EncodeToString(token[:]), Fingerprint: identity.Fingerprint,
		Components: cloneFingerprintComponents(identity.Components), NextSequence: 1,
		Samples: []ConfidenceFingerprintSample{},
	}, nil
}

func cloneFingerprintComponents(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func ScoreConfidenceFingerprint(identity ConfidenceFingerprintIdentity, state *ConfidenceFingerprintState) ConfidenceFingerprintAttribution {
	return ScoreConfidenceFingerprintAt(identity, state, time.Now().UTC())
}

func ScoreConfidenceFingerprintAt(identity ConfidenceFingerprintIdentity, state *ConfidenceFingerprintState, now time.Time) ConfidenceFingerprintAttribution {
	minimumSamples := 0
	reason := "no_matching_baseline"
	result := ConfidenceFingerprintAttribution{
		Status: "unknown", Reason: reason, Platform: identity.Platform,
		Model: identity.Model, Protocol: identity.Protocol, ProtocolStatus: "unknown",
		TargetModel: identity.TargetModel,
		Methods: map[string]ConfidenceFingerprintMethodSummary{
			"behavioral": {Status: "unknown", MinimumSamples: minimumSamples, BaselineVersion: identity.Baseline, Reason: reason},
		},
	}
	if state == nil {
		state = &ConfidenceFingerprintState{}
	}
	cutoff := now.UTC().Add(-UpstreamHealthObservationRetention)
	latestIndex := -1
	for i := len(state.Samples) - 1; i >= 0; i-- {
		if !state.Samples[i].ObservedAt.Before(cutoff) {
			latestIndex = i
			break
		}
	}
	if state.LastProtocolEvidence != nil && !state.LastProtocolEvidence.ObservedAt.Before(cutoff) {
		latest := state.LastProtocolEvidence.Evidence
		result.ObservedProtocol = latest.Route
		result.ProtocolStatus = latest.ProtocolStatus
		result.ProtocolReason = latest.ProtocolReason
	} else if latestIndex >= 0 {
		latest := state.Samples[latestIndex].Evidence
		result.ObservedProtocol = latest.Route
		result.ProtocolStatus = latest.ProtocolStatus
		result.ProtocolReason = latest.ProtocolReason
	}
	result.Methods["behavioral"] = ConfidenceFingerprintMethodSummary{
		Status: "unknown", SampleCount: fingerprintMethodSampleCount(state.Samples, "behavioral", now),
		Reason: "no_matching_baseline",
	}
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		baseline := strings.TrimSpace(identity.Components["baseline_"+method])
		if baseline == "" {
			continue
		}
		result.Methods[method] = scoreReferenceMethod(identity, state, method, baseline, now)
	}
	result.Status, result.Reason = referenceFingerprintSummaryStatus(result.Methods)
	return result
}

func referenceFingerprintSummaryStatus(methods map[string]ConfidenceFingerprintMethodSummary) (string, string) {
	status, reason := "unknown", "no_matching_baseline"
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		summary, ok := methods[method]
		if !ok {
			continue
		}
		if summary.Status == "reference_only" {
			return "reference_only", "reference_ranking_only"
		}
		if summary.Status == "insufficient" {
			status, reason = "insufficient", "insufficient_samples"
		} else if status == "unknown" && summary.Reason == "baseline_unavailable" {
			reason = "baseline_unavailable"
		}
	}
	return status, reason
}

func scoreReferenceMethod(identity ConfidenceFingerprintIdentity, state *ConfidenceFingerprintState, method, baseline string, now time.Time) ConfidenceFingerprintMethodSummary {
	sequence := state.MethodSequences[method]
	currentSequence := sequence
	if currentSequence < 1 {
		currentSequence = 1
	}
	currentStart := ((currentSequence-1)/3)*3 + 1
	current := scoreReferenceBlock(identity, state, method, baseline, now, currentStart)
	completed := sequence / 3
	if state.Pending != nil && state.LeaseExpires.After(now) && state.Pending.Method == method && state.Pending.MethodSequence == sequence && sequence%3 == 0 {
		completed--
	}
	if completed <= 0 {
		current.Weight = confidenceMethodWeight(method)
		current.CurrentSampleCount = current.SampleCount
		return current
	}
	// Keep the last completed evidence while a new block is collecting. A
	// completed invalid block intentionally invalidates the confirmation streak.
	latest := scoreReferenceBlock(identity, state, method, baseline, now, (completed-1)*3+1)
	if latest.SampleCount == 0 && current.SampleCount > 0 {
		current.Weight = confidenceMethodWeight(method)
		current.CurrentSampleCount = current.SampleCount
		return current
	}
	latest.Weight = confidenceMethodWeight(method)
	latest.CurrentSampleCount = current.SampleCount
	candidate := qualifiedReferenceCandidate(method, latest)
	if candidate == "" {
		if latest.SampleCount == 3 {
			latest.Reason = "weak_evidence"
		} else {
			latest.Reason = "invalid_round"
		}
		return latest
	}
	latest.CandidateModel = candidate
	latest.ConfirmedRounds = 1
	latest.Reason = "awaiting_confirmation"
	if completed >= 2 {
		previous := scoreReferenceBlock(identity, state, method, baseline, now, (completed-2)*3+1)
		if qualifiedReferenceCandidate(method, previous) == candidate {
			latest.ConfirmedRounds = 2
			latest.Participating = true
			latest.EffectiveWeight = latest.Weight
			latest.Reason = "reference_evidence_confirmed"
		}
	}
	return latest
}

func scoreReferenceBlock(identity ConfidenceFingerprintIdentity, state *ConfidenceFingerprintState, method, baseline string, now time.Time, blockStart int64) ConfidenceFingerprintMethodSummary {
	minimum := 3
	features := make([][]float64, minimum)
	numbers := make([][]int, minimum)
	seen := make([]bool, minimum)
	valid := 0
	cutoff := now.UTC().Add(-UpstreamHealthObservationRetention)
	if blockStart > 0 {
		for _, sample := range state.Samples {
			if sample.Method != method || sample.MethodSequence < blockStart || sample.MethodSequence >= blockStart+int64(minimum) ||
				!referenceFingerprintEvidenceUsable(sample.Evidence) || sample.Evidence.MethodSequence != sample.MethodSequence ||
				sample.ObservedAt.Before(cutoff) || sample.ObservedAt.After(now) {
				continue
			}
			index := int(sample.MethodSequence - blockStart)
			if sample.Evidence.ChallengeIndex != index {
				continue
			}
			var accepted bool
			switch method {
			case "chanshui_behavioral":
				if len(sample.Evidence.Feature) == 6 {
					features[index] = append([]float64(nil), sample.Evidence.Feature...)
					accepted = true
				}
			case "modeltrace_numeric":
				minimumNumbers, err := modeltrace_numeric.MinimumForChallenge(index)
				if err == nil && len(sample.Evidence.Numbers) >= minimumNumbers {
					numbers[index] = append([]int(nil), sample.Evidence.Numbers...)
					accepted = true
				}
			}
			if accepted && !seen[index] {
				seen[index] = true
				valid++
			}
		}
	}
	summary := ConfidenceFingerprintMethodSummary{
		Status: "insufficient", SampleCount: valid, MinimumSamples: minimum,
		BaselineVersion: baseline, Reason: "insufficient_samples", Candidates: []ConfidenceFingerprintCandidate{},
	}
	if valid != minimum {
		return summary
	}
	if method == "chanshui_behavioral" {
		result, err := CompareChanshuiBehaviorFeatures(features)
		if err != nil || result.Status != "reference_only" {
			if err != nil {
				summary.Status, summary.Reason = "unknown", "baseline_unavailable"
			}
			return summary
		}
		for _, candidate := range result.Top3 {
			summary.Candidates = append(summary.Candidates, ConfidenceFingerprintCandidate{
				ModelID: candidate.ModelID, DisplayName: candidate.DisplayName, Score: candidate.Similarity, SampleSize: candidate.SampleSize,
			})
		}
	} else {
		outputs := make([]modeltrace_numeric.Output, minimum)
		expectedCounts := modeltrace_numeric.ChallengeExpectedCounts()
		for i, values := range numbers {
			var text strings.Builder
			for index, value := range values {
				if index > 0 {
					text.WriteByte(' ')
				}
				text.WriteString(strconv.Itoa(value))
			}
			outputs[i] = modeltrace_numeric.Output{Text: text.String(), ExpectedCount: expectedCounts[i]}
		}
		result, err := modeltrace_numeric.Analyze(outputs, identity.Model)
		if err != nil || result.Status != "reference_only" || !result.TargetPresent {
			if err != nil {
				summary.Status, summary.Reason = "unknown", "baseline_unavailable"
			}
			return summary
		}
		for _, candidate := range result.Candidates {
			summary.Candidates = append(summary.Candidates, ConfidenceFingerprintCandidate{
				ModelID: candidate.Model, DisplayName: candidate.DisplayName, Score: candidate.Score, RelativeFit: candidate.RelativeFit, ClosedSetSupport: candidate.ClosedSetSupport,
			})
		}
	}
	if len(summary.Candidates) > 3 {
		summary.Candidates = summary.Candidates[:3]
	}
	summary.Status = "reference_only"
	summary.Reason = "reference_ranking_only"
	return summary
}

func MergeConfidenceFingerprintAttribution(distribution *UpstreamConfidenceDistribution, fingerprint ConfidenceFingerprintAttribution) ConfidenceFingerprintAttribution {
	referenceStatus, referenceReason := referenceFingerprintSummaryStatus(fingerprint.Methods)
	distributionStatus := "unknown"
	distributionSamples := 0
	distributionMinimum := UpstreamConfidenceDistributionWindowSize
	distributionBaseline := ""
	if distribution != nil {
		distributionStatus = distribution.Status
		distributionSamples = distribution.ValidSamples
		distributionBaseline = distribution.BaselineVersion
		if distribution.Status != "match" && distribution.Status != "mismatch" {
			distributionStatus = "insufficient"
		}
	}
	if fingerprint.Methods == nil {
		fingerprint.Methods = map[string]ConfidenceFingerprintMethodSummary{}
	}
	if fingerprint.Platform == PlatformOpenAI {
		fingerprint.Methods["distribution"] = ConfidenceFingerprintMethodSummary{
			Status: distributionStatus, SampleCount: distributionSamples, MinimumSamples: distributionMinimum,
			BaselineVersion: distributionBaseline, Reason: func() string {
				if distribution == nil || distribution.Status == "collecting" || distribution.Status == "insufficient" {
					return "insufficient_samples"
				}
				return ""
			}(),
		}
	}
	behavioral := fingerprint.Methods["behavioral"]
	behavioralValid := behavioral.Status == "match" || behavioral.Status == "mismatch"
	distributionValid := fingerprint.Platform == PlatformOpenAI && (distributionStatus == "match" || distributionStatus == "mismatch")
	switch {
	case behavioralValid && distributionValid && behavioral.Status == distributionStatus:
		fingerprint.Status = "confirmed_" + behavioral.Status
		fingerprint.Reason = "methods_agree"
	case behavioralValid && distributionValid:
		fingerprint.Status = "conflict"
		fingerprint.Reason = "methods_conflict"
	case behavioralValid:
		fingerprint.Status = "provisional_" + behavioral.Status
		fingerprint.Reason = "single_valid_method"
	case distributionValid:
		fingerprint.Status = "provisional_" + distributionStatus
		fingerprint.Reason = "single_valid_method"
	case referenceStatus == "reference_only":
		fingerprint.Status, fingerprint.Reason = referenceStatus, referenceReason
	case behavioral.Status == "insufficient" || referenceStatus == "insufficient" || (fingerprint.Platform == PlatformOpenAI && distributionStatus == "insufficient"):
		fingerprint.Status = "insufficient"
		fingerprint.Reason = "insufficient_samples"
	default:
		fingerprint.Status = "unknown"
		if fingerprint.Reason == "" {
			fingerprint.Reason = "no_matching_baseline"
		}
	}
	return fingerprint
}

func fingerprintMethodSampleCount(samples []ConfidenceFingerprintSample, method string, now time.Time) int {
	count := 0
	for _, sample := range samples {
		if sample.Method == method && sample.Evidence.ProtocolStatus == "compatible" && !sample.ObservedAt.Before(now.UTC().Add(-UpstreamHealthObservationRetention)) {
			count++
		}
	}
	return count
}

func effectiveFingerprintProbeResult(result UpstreamHealthProbeResult) (UpstreamHealthProbeResult, string) {
	if result.Protocol != upstreamHealthProbeProtocolAdaptive {
		return result, result.Protocol
	}
	for _, protocol := range []string{upstreamHealthProbeProtocolOpenAI, upstreamHealthProbeProtocolOpenAIChat, upstreamHealthProbeProtocolAnthropic} {
		candidate, ok := result.ProtocolResults[protocol]
		if ok && candidate.Result == "success" {
			return candidate, candidate.Protocol
		}
	}
	for _, protocol := range []string{upstreamHealthProbeProtocolOpenAIChat, upstreamHealthProbeProtocolOpenAI, upstreamHealthProbeProtocolAnthropic} {
		if candidate, ok := result.ProtocolResults[protocol]; ok {
			return candidate, candidate.Protocol
		}
	}
	return result, result.Protocol
}

func ClassifyConfidenceFingerprintResponse(protocol, result, reason string, evidence map[string]any) ConfidenceFingerprintEvidence {
	item := ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", Route: strings.TrimSpace(protocol)}
	if result != "success" {
		if reason == "probe_protocol_mismatch" {
			item.ProtocolStatus = "mismatch"
			item.ProtocolReason = "protocol_shape_mismatch"
		} else {
			item.ProtocolStatus = "unknown"
			item.ProtocolReason = "probe_not_completed"
		}
		return item
	}
	item.Shape = strings.TrimSpace(protocol)
	if evidence != nil {
		for _, key := range []string{"finish_reason", "response_status", "output_type", "content_type"} {
			if value, ok := evidence[key].(string); ok {
				value = strings.ToLower(strings.TrimSpace(value))
				if value != "" {
					item.FinishClass = key + ":" + value
					break
				}
			}
		}
		for _, key := range []string{"reasoning_tokens", "reasoning_token_count"} {
			if value, ok := evidence[key]; ok && value != nil {
				item.Reasoning = true
				break
			}
		}
		if outputTokens, ok := evidence["output_tokens"].(int64); ok {
			switch {
			case outputTokens > 0 && outputTokens <= 8:
				item.OutputClass = "short"
			case outputTokens > 8 && outputTokens <= 32:
				item.OutputClass = "medium"
			case outputTokens > 32:
				item.OutputClass = "long"
			}
		}
	}
	return item
}

func confidenceFingerprintEvidenceFromProbe(result UpstreamHealthProbeResult, protocol string) ConfidenceFingerprintEvidence {
	evidence := make(map[string]any, len(result.ConfidenceEvidence)+2)
	for key, value := range result.ConfidenceEvidence {
		evidence[key] = value
	}
	if result.FinishReason != "" {
		evidence["finish_reason"] = result.FinishReason
	}
	if result.ReasoningTokens != nil && *result.ReasoningTokens > 0 {
		evidence["reasoning_tokens"] = *result.ReasoningTokens
	}
	if result.OutputTokens != nil {
		evidence["output_tokens"] = *result.OutputTokens
	}
	return ClassifyConfidenceFingerprintResponse(protocol, result.Result, result.Reason, evidence)
}
