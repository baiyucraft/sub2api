package service

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

const (
	UpstreamConfidenceDistributionWindowSize    = 128
	UpstreamConfidenceDistributionClaimedModel  = "gpt-6.1-sol"
	UpstreamConfidenceDistributionPromptVersion = "openai-sol-distribution-v1"
	DistributionProbePunctuation                = "gpt__screen067"
	DistributionProbeCountry                    = "gpt__screen101"
	DistributionProbeInteger                    = "gpt__screen108"
	distributionUnknown                         = "__UNSEEN_IN_TRAINING__"
	distributionOther                           = "other_known_external"
	distributionScoringVersion                  = "meow-fingerprint-v3-predictive"
)

// DistributionSample represents one issued request, including unsuccessful
// requests. Series/protocol isolation is the persistent collector's contract.
type DistributionSample struct {
	Sequence   int64     `json:"sequence"`
	ProbeID    string    `json:"probe_id"`
	Answer     string    `json:"answer,omitempty"`
	Valid      bool      `json:"valid"`
	Reason     string    `json:"reason,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type UpstreamConfidenceDistributionCell struct {
	Planned   int            `json:"planned"`
	Minimum   int            `json:"minimum"`
	Completed int            `json:"completed"`
	Valid     int            `json:"valid"`
	Counts    map[string]int `json:"counts"`
}

// ConfidenceDistributionSeriesReset is safe display metadata, never raw identity.
type ConfidenceDistributionSeriesReset struct {
	Pending           bool       `json:"pending"`
	At                *time.Time `json:"at,omitempty"`
	Reasons           []string   `json:"reasons"`
	PreviousAttempted int        `json:"previous_attempted"`
}

// Matches are relative behavioral fit scores, not identity probabilities.
type UpstreamConfidenceDistribution struct {
	SeriesReset     *ConfidenceDistributionSeriesReset            `json:"series_reset,omitempty"`
	Status          string                                        `json:"status"`
	WindowSize      int                                           `json:"window_size"`
	Attempted       int                                           `json:"attempted"`
	ValidSamples    int                                           `json:"valid_samples"`
	Cells           map[string]UpstreamConfidenceDistributionCell `json:"cells"`
	Matches         map[string]float64                            `json:"matches"`
	Scores          map[string]float64                            `json:"scores"`
	Thresholds      map[string]float64                            `json:"thresholds"`
	ClosestModel    string                                        `json:"closest_model,omitempty"`
	ClaimedModel    string                                        `json:"claimed_model"`
	WindowStart     *time.Time                                    `json:"window_start,omitempty"`
	WindowEnd       *time.Time                                    `json:"window_end,omitempty"`
	BaselineVersion string                                        `json:"baseline_version"`
	Protocol        string                                        `json:"protocol"`
	Reasons         []string                                      `json:"reasons"`
}

type distributionBaselineCell struct {
	Categories     []string             `json:"categories"`
	Alpha          map[string][]float64 `json:"alpha"`
	SourceSamples  map[string]int       `json:"source_samples"`
	ReferenceReady bool                 `json:"reference_ready"`
}

type distributionBaseline struct {
	ID                  string `json:"id"`
	Version             string `json:"version"`
	Protocol            string `json:"protocol"`
	SourceContentSHA256 string `json:"source_content_sha256"`
	Fitted              struct {
		ScoringVersion   string                              `json:"scoring_version"`
		PriorMass        float64                             `json:"prior_mass"`
		Models           []string                            `json:"models"`
		Sources          []string                            `json:"sources"`
		ReferenceSources []string                            `json:"reference_sources"`
		Aggregation      string                              `json:"aggregation"`
		Cells            map[string]distributionBaselineCell `json:"cells"`
	} `json:"fitted"`
	High struct {
		Counts     map[string]int     `json:"counts"`
		Thresholds map[string]float64 `json:"thresholds"`
	} `json:"high"`
}

//go:embed confidence_baselines/responses.json confidence_baselines/chat_completions.json
var distributionBaselineAssets embed.FS

var distributionBaselineCache struct {
	sync.Once
	values map[string]*distributionBaseline
	errors map[string]error
}

func distributionProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "responses", "response":
		return "responses"
	case "chat_completions", "chat-completions", "chat":
		return "chat_completions"
	default:
		return protocol
	}
}

func distributionLoadBaseline(protocol string) (*distributionBaseline, error) {
	distributionBaselineCache.Do(func() {
		distributionBaselineCache.values = make(map[string]*distributionBaseline)
		distributionBaselineCache.errors = make(map[string]error)
		for name, digest := range map[string]string{
			"responses":        "df60424854f82407aae68a5df9d7025d4f714af3311d3709233f14b9d001e638",
			"chat_completions": "ee756a7e8a784c5e70c31a41d1a5a80e77647246b41222c9b5b4e7d1131c4f0c",
		} {
			raw, err := distributionBaselineAssets.ReadFile("confidence_baselines/" + name + ".json")
			var baseline *distributionBaseline
			if err == nil {
				baseline, err = distributionDecodeBaseline(raw, name, digest)
			}
			distributionBaselineCache.values[name], distributionBaselineCache.errors[name] = baseline, err
		}
	})
	protocol = distributionProtocol(protocol)
	baseline, ok := distributionBaselineCache.values[protocol]
	if !ok {
		return nil, fmt.Errorf("unsupported confidence distribution protocol %q", protocol)
	}
	return baseline, distributionBaselineCache.errors[protocol]
}

func distributionDecodeBaseline(raw []byte, protocol, digest string) (*distributionBaseline, error) {
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != digest {
		return nil, fmt.Errorf("confidence distribution baseline checksum mismatch")
	}
	var baseline distributionBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return nil, fmt.Errorf("decode confidence distribution baseline: %w", err)
	}
	if err := distributionValidateBaseline(&baseline, protocol); err != nil {
		return nil, err
	}
	return &baseline, nil
}

func distributionValidateBaseline(b *distributionBaseline, protocol string) error {
	if b == nil || b.Protocol != protocol || b.ID == "" || b.Version == "" || len(b.SourceContentSHA256) != 64 ||
		b.Fitted.ScoringVersion != distributionScoringVersion || b.Fitted.Aggregation != "nearest_source" || b.Fitted.PriorMass != 1 ||
		len(b.Fitted.Models) != 5 || len(b.Fitted.Sources) != 8 || len(b.Fitted.ReferenceSources) != 4 {
		return fmt.Errorf("confidence distribution baseline contract mismatch")
	}
	models := []string{UpstreamConfidenceDistributionClaimedModel, "gpt-6-astra", "gpt-5.6-terra", "gpt-6-luna", distributionOther}
	for i, model := range models {
		threshold, ok := b.High.Thresholds[model]
		if b.Fitted.Models[i] != model || !ok || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < .5 || threshold > .98 {
			return fmt.Errorf("confidence distribution baseline model thresholds mismatch")
		}
	}
	if len(b.High.Counts) != 3 || len(b.High.Thresholds) != 5 || len(b.Fitted.Cells) != 3 {
		return fmt.Errorf("confidence distribution baseline quota mismatch")
	}
	for identity, quota := range DistributionProbeQuotas() {
		cell, ok := b.Fitted.Cells[identity]
		if !ok || b.High.Counts[identity] != quota || !cell.ReferenceReady || len(cell.Categories) == 0 || len(cell.Alpha) != len(b.Fitted.Sources) {
			return fmt.Errorf("confidence distribution baseline cell unavailable: %s", identity)
		}
		seen := make(map[string]bool)
		for _, category := range cell.Categories {
			if category == "" || seen[category] {
				return fmt.Errorf("confidence distribution baseline categories invalid: %s", identity)
			}
			seen[category] = true
		}
		if !seen[distributionUnknown] {
			return fmt.Errorf("confidence distribution baseline unknown category unavailable: %s", identity)
		}
		for _, source := range b.Fitted.Sources {
			alpha := cell.Alpha[source]
			if len(alpha) != len(cell.Categories) || cell.SourceSamples[source] <= 0 {
				return fmt.Errorf("confidence distribution baseline source unavailable: %s", source)
			}
			for _, value := range alpha {
				if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
					return fmt.Errorf("confidence distribution baseline alpha invalid: %s", source)
				}
			}
		}
	}
	for _, source := range b.Fitted.ReferenceSources {
		if !containsDistributionSource(b.Fitted.Sources, source) {
			return fmt.Errorf("confidence distribution baseline external source unavailable: %s", source)
		}
	}
	return nil
}

func containsDistributionSource(sources []string, source string) bool {
	for _, candidate := range sources {
		if source == candidate {
			return true
		}
	}
	return false
}

func DistributionBaselineVersion(protocol string) string {
	baseline, err := distributionLoadBaseline(protocol)
	if err != nil {
		return ""
	}
	return baseline.Version
}

func DistributionPrompt(probeID string) string {
	switch probeID {
	case DistributionProbePunctuation:
		return "不要思考，直接回答。随便选一个标点符号，只回答符号。"
	case DistributionProbeCountry:
		return "Name a random country. Reply with ONLY the country name."
	case DistributionProbeInteger:
		return "从17到83中随便选一个整数，只回答数字。"
	default:
		return ""
	}
}

func DistributionProbeQuotas() map[string]int {
	return map[string]int{DistributionProbePunctuation: 64, DistributionProbeCountry: 16, DistributionProbeInteger: 48}
}

// NewDistributionProbeOrder creates a single random cycle; callers persist and
// repeat this exact cycle so every consecutive 128 requests has fixed quotas.
func NewDistributionProbeOrder() ([]string, error) {
	order := make([]string, 0, UpstreamConfidenceDistributionWindowSize)
	for _, identity := range []string{DistributionProbePunctuation, DistributionProbeCountry, DistributionProbeInteger} {
		for n := 0; n < DistributionProbeQuotas()[identity]; n++ {
			order = append(order, identity)
		}
	}
	for i := len(order) - 1; i > 0; i-- {
		position, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, fmt.Errorf("randomize confidence distribution cycle: %w", err)
		}
		j := int(position.Int64())
		order[i], order[j] = order[j], order[i]
	}
	return order, nil
}

// NormalizeDistributionAnswer follows Python strip/casefold and its two rune
// length limits; it intentionally does not check punctuation/country/ranges.
func NormalizeDistributionAnswer(raw string) (string, bool) {
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > 65536 {
		return "", false
	}
	trimmed := strings.TrimFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || r >= '\x1c' && r <= '\x1f' })
	if trimmed == "" {
		return "", false
	}
	answer := cases.Fold().String(trimmed)
	if answer == "" || utf8.RuneCountInString(answer) > 4096 {
		return "", false
	}
	return answer, true
}

func ScoreUpstreamConfidenceDistribution(protocol string, samples []DistributionSample) (*UpstreamConfidenceDistribution, error) {
	result := &UpstreamConfidenceDistribution{
		Status: "collecting", WindowSize: UpstreamConfidenceDistributionWindowSize,
		ClaimedModel: UpstreamConfidenceDistributionClaimedModel, Protocol: distributionProtocol(protocol),
		Cells: make(map[string]UpstreamConfidenceDistributionCell), Matches: make(map[string]float64),
		Scores: make(map[string]float64), Thresholds: make(map[string]float64), Reasons: []string{},
	}
	window := append([]DistributionSample(nil), samples...)
	sort.SliceStable(window, func(i, j int) bool { return window[i].Sequence < window[j].Sequence })
	if len(window) > result.WindowSize {
		window = window[len(window)-result.WindowSize:]
	}
	result.Attempted = len(window)
	if len(window) > 0 {
		start, end := window[0].ObservedAt, window[len(window)-1].ObservedAt
		result.WindowStart, result.WindowEnd = &start, &end
	}
	baseline, err := distributionLoadBaseline(protocol)
	if err != nil {
		result.Status, result.Reasons = "insufficient", []string{"baseline_unavailable"}
		return result, err
	}
	result.BaselineVersion = baseline.Version
	return distributionScoreWindow(baseline, result, window), nil
}

func distributionScoreWindow(baseline *distributionBaseline, result *UpstreamConfidenceDistribution, window []DistributionSample) *UpstreamConfidenceDistribution {
	for identity, planned := range baseline.High.Counts {
		result.Cells[identity] = UpstreamConfidenceDistributionCell{Planned: planned, Minimum: int(math.Ceil(.6 * float64(planned))), Counts: make(map[string]int)}
	}
	for model, threshold := range baseline.High.Thresholds {
		result.Thresholds[model] = threshold
	}
	for i, sample := range window {
		if i > 0 && sample.Sequence != window[i-1].Sequence+1 {
			result.Reasons = append(result.Reasons, "sequence_incomplete")
		}
		cell, exists := result.Cells[sample.ProbeID]
		if !exists {
			result.Reasons = append(result.Reasons, "unknown_probe")
			continue
		}
		cell.Completed++
		answer, valid := NormalizeDistributionAnswer(sample.Answer)
		if sample.Valid && valid {
			known := false
			for _, category := range baseline.Fitted.Cells[sample.ProbeID].Categories {
				if category == answer {
					known = true
					break
				}
			}
			if !known {
				answer = distributionUnknown
			}
			cell.Counts[answer]++
			cell.Valid++
			result.ValidSamples++
		}
		result.Cells[sample.ProbeID] = cell
	}
	// No numerical result is published before the complete 128-attempt window.
	if result.Attempted < result.WindowSize {
		result.Reasons = []string{"window_incomplete"}
		return result
	}
	for _, cell := range result.Cells {
		if cell.Completed != cell.Planned {
			result.Reasons = append(result.Reasons, "quota_mismatch")
		}
		if cell.Valid < cell.Minimum {
			result.Reasons = append(result.Reasons, "samples_incomplete")
		}
	}
	if result.ValidSamples < 78 {
		result.Reasons = append(result.Reasons, "samples_incomplete")
	}
	if result.ValidSamples == 0 {
		result.Reasons = append(result.Reasons, "no_valid_samples")
	}
	result.Matches, result.Scores = distributionPredictiveScores(baseline, result.Cells, result.ValidSamples)
	winner := ""
	best := math.Inf(-1)
	tied := false
	for _, model := range baseline.Fitted.Models {
		value := result.Matches[model]
		if value > best {
			winner, best, tied = model, value, false
		} else if value == best {
			tied = true
		}
	}
	result.ClosestModel = winner
	if tied {
		result.Reasons = append(result.Reasons, "multiple_thresholds")
	}
	if best <= result.Thresholds[winner] {
		result.Reasons = append(result.Reasons, "no_threshold")
	}
	sort.Strings(result.Reasons)
	unique := result.Reasons[:0]
	for _, reason := range result.Reasons {
		if len(unique) == 0 || unique[len(unique)-1] != reason {
			unique = append(unique, reason)
		}
	}
	result.Reasons = unique
	if len(result.Reasons) > 0 {
		result.Status = "insufficient"
	} else if winner == result.ClaimedModel {
		result.Status = "match"
	} else {
		result.Status = "mismatch"
	}
	return result
}

// Integrating categorical probabilities under a Dirichlet posterior yields
// log Γ(sum α) - log Γ(sum α+n) + Σ(log Γ(αk+nk)-log Γ(αk)).
// Public multinomial coefficients cancel between every candidate source.
func distributionPredictiveScores(b *distributionBaseline, cells map[string]UpstreamConfidenceDistributionCell, total int) (map[string]float64, map[string]float64) {
	evidence := make(map[string]float64, len(b.Fitted.Sources))
	identities := make([]string, 0, len(cells))
	for identity := range cells {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		cell, fitted := cells[identity], b.Fitted.Cells[identity]
		for _, source := range b.Fitted.Sources {
			alpha := fitted.Alpha[source]
			mass := 0.0
			for _, a := range alpha {
				mass += a
			}
			before, _ := math.Lgamma(mass)
			after, _ := math.Lgamma(mass + float64(cell.Valid))
			value := before - after
			for i, category := range fitted.Categories {
				before, _ = math.Lgamma(alpha[i])
				after, _ = math.Lgamma(alpha[i] + float64(cell.Counts[category]))
				value += after - before
			}
			evidence[source] += value
		}
	}
	other := math.Inf(-1)
	for _, source := range b.Fitted.ReferenceSources {
		other = math.Max(other, evidence[source])
	}
	evidence[distributionOther] = other
	matches, scores := make(map[string]float64), make(map[string]float64)
	divisor := math.Max(float64(total), 1)
	for _, model := range b.Fitted.Models {
		rival := math.Inf(-1)
		for _, candidate := range b.Fitted.Models {
			if candidate != model {
				rival = math.Max(rival, evidence[candidate])
			}
		}
		margin := (evidence[model] - rival) / divisor
		if margin >= 0 {
			matches[model] = 1 / (1 + math.Exp(-margin))
		} else {
			e := math.Exp(margin)
			matches[model] = e / (1 + e)
		}
		scores[model] = evidence[model] / divisor
	}
	return matches, scores
}
