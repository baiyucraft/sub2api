package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type distributionOracleFixture struct {
	Name     string                    `json:"name"`
	Counts   map[string]map[string]int `json:"counts"`
	Expected struct {
		Verdict      string             `json:"verdict"`
		Model        string             `json:"model"`
		Matches      map[string]float64 `json:"matches"`
		Scores       map[string]float64 `json:"scores"`
		ValidSamples int                `json:"valid_samples"`
	} `json:"expected"`
}

func distributionFixtures(t *testing.T, protocol string) []distributionOracleFixture {
	t.Helper()
	raw, err := os.ReadFile("confidence_baselines/fixtures/" + protocol + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []distributionOracleFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func distributionSamplesFromCounts(counts map[string]map[string]int) []DistributionSample {
	samples := make([]DistributionSample, 0, 128)
	for _, identity := range []string{DistributionProbePunctuation, DistributionProbeCountry, DistributionProbeInteger} {
		categories := make([]string, 0, len(counts[identity]))
		for category := range counts[identity] {
			categories = append(categories, category)
		}
		sort.Strings(categories)
		for _, category := range categories {
			for i := 0; i < counts[identity][category]; i++ {
				sequence := int64(len(samples) + 1)
				samples = append(samples, DistributionSample{Sequence: sequence, ValidSequence: sequence, ProbeID: identity, Answer: category, Valid: true,
					ObservedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC).Add(time.Duration(sequence) * time.Minute)})
			}
		}
	}
	return samples
}

func distributionHasReason(result *UpstreamConfidenceDistribution, reason string) bool {
	for _, value := range result.Reasons {
		if value == reason {
			return true
		}
	}
	return false
}

func TestDistributionPredictiveMatchesIndependentUpstreamOracle(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		for _, fixture := range distributionFixtures(t, protocol) {
			t.Run(protocol+"/"+fixture.Name, func(t *testing.T) {
				result, err := ScoreUpstreamConfidenceDistribution(protocol, distributionSamplesFromCounts(fixture.Counts))
				if err != nil {
					t.Fatal(err)
				}
				if result.Status != fixture.Expected.Verdict || result.ClosestModel != fixture.Expected.Model || result.ValidSamples != fixture.Expected.ValidSamples {
					t.Fatalf("oracle verdict mismatch: got %+v, expected %+v", result, fixture.Expected)
				}
				for model, expected := range fixture.Expected.Matches {
					if math.Abs(result.Matches[model]-expected) > 1e-12 {
						t.Errorf("%s match: %.16g want %.16g", model, result.Matches[model], expected)
					}
				}
				for model, expected := range fixture.Expected.Scores {
					if math.Abs(result.Scores[model]-expected) > 1e-12 {
						t.Errorf("%s score: %.16g want %.16g", model, result.Scores[model], expected)
					}
				}
			})
		}
	}
}

func TestDistributionNormalizeAnswerFollowsPythonContract(t *testing.T) {
	tests := []struct {
		name, raw, want string
		valid           bool
	}{
		{"country casefold", " \nPERU\t ", "peru", true},
		{"full Unicode fold", " Straße Σςẞ İ ", "strasse σσss i\u0307", true},
		{"Python whitespace", "\x1c\x1d\x1e\x1fPERU\u0085", "peru", true},
		{"integer untouched", " +084 ", "+084", true},
		{"anomalous sentence valid", "I chose the imaginary country.", "i chose the imaginary country.", true},
		{"aliases untouched", "The United States of America", "the united states of america", true},
		{"empty", " \n\t ", "", false},
		{"Unicode rune length", strings.Repeat("。", 4096), strings.Repeat("。", 4096), true},
		{"normalized too long", strings.Repeat("。", 4097), "", false},
		{"casefold expansion too long", strings.Repeat("ß", 2049), "", false},
		{"raw trimmed max", strings.Repeat(" ", 65536) + "x", "", false},
		{"invalid UTF8", string([]byte{0xff}), "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := NormalizeDistributionAnswer(test.raw)
			if got != test.want || valid != test.valid {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, valid, test.want, test.valid)
			}
		})
	}
}

func TestDistributionFixedCyclePreservesEveryRollingWindow(t *testing.T) {
	order, err := NewDistributionProbeOrder()
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 128 {
		t.Fatalf("cycle length %d", len(order))
	}
	for start := 0; start < 256; start++ {
		counts := make(map[string]int)
		for i := start; i < start+128; i++ {
			counts[order[i%128]]++
		}
		if !reflect.DeepEqual(counts, DistributionProbeQuotas()) {
			t.Fatalf("start %d quota %v", start, counts)
		}
	}
	for _, identity := range order {
		if DistributionPrompt(identity) == "" {
			t.Fatal("missing prompt", identity)
		}
	}
	if DistributionPrompt("juice") != "" {
		t.Fatal("legacy prompt must not be a distribution question")
	}
}

func TestDistributionCollectsUntil128AndDrops129thOldestValidSample(t *testing.T) {
	fixture := distributionFixtures(t, "responses")[0]
	samples := distributionSamplesFromCounts(fixture.Counts)
	before := append([]DistributionSample(nil), samples...)
	partial, err := ScoreUpstreamConfidenceDistribution("responses", samples[:127])
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "collecting" || partial.Attempted != 127 || len(partial.Matches) != 0 || partial.ClosestModel != "" {
		t.Fatalf("premature judgment: %+v", partial)
	}
	newest := samples[0]
	newest.Sequence, newest.ValidSequence = 129, 129
	newest.ObservedAt = before[127].ObservedAt.Add(time.Minute)
	samples = append(samples, newest)
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	result, err := ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempted != 128 || result.ValidSamples != 128 || result.Status != "match" || result.Cells[DistributionProbePunctuation].Completed != 64 {
		t.Fatalf("old valid sample retained: %+v", result)
	}
	if !result.WindowStart.Equal(before[1].ObservedAt) || !result.WindowEnd.Equal(newest.ObservedAt) {
		t.Fatal("window timestamps must follow retained sequence")
	}
	if !reflect.DeepEqual(samples[0], newest) {
		t.Fatal("caller samples mutated")
	}
}

func TestDistributionFailuresDoNotFillOrEvictTheValidWindow(t *testing.T) {
	fixture := distributionFixtures(t, "responses")[0]
	samples := distributionSamplesFromCounts(fixture.Counts)
	withFailures := make([]DistributionSample, 0, 256)
	for i := range samples {
		samples[i].Sequence = int64(2*i + 2)
		withFailures = append(withFailures, DistributionSample{Sequence: int64(2*i + 1), ProbeID: samples[i].ProbeID, Reason: "timeout"}, samples[i])
	}
	partial, err := ScoreUpstreamConfidenceDistribution("responses", withFailures[:254])
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "collecting" || partial.ValidSamples != 127 || len(partial.Matches) != 0 {
		t.Fatalf("failed requests filled the window: %+v", partial)
	}
	result, err := ScoreUpstreamConfidenceDistribution("responses", withFailures)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != fixture.Expected.Verdict || result.ValidSamples != 128 || distributionHasReason(result, "sequence_incomplete") {
		t.Fatalf("actual request sequence gaps invalidated valid samples: %+v", result)
	}
	for identity, count := range DistributionProbeQuotas() {
		if result.Cells[identity].Valid != count || result.Cells[identity].Minimum != count {
			t.Fatalf("%s quota/minimum mismatch: %+v", identity, result.Cells[identity])
		}
	}
	withFailures = append(withFailures, DistributionSample{Sequence: 257, ProbeID: DistributionProbeInteger, Reason: "timeout"})
	afterFailure, err := ScoreUpstreamConfidenceDistribution("responses", withFailures)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, afterFailure) {
		t.Fatal("later failure evicted or changed a valid window")
	}
	// Empty content is invalid even if the transport caller marked it valid.
	samples = distributionSamplesFromCounts(fixture.Counts)
	for i := range samples {
		samples[i].Answer = ""
	}
	result, err = ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.ValidSamples != 0 || result.Attempted != 0 || result.Status != "collecting" || !distributionHasReason(result, "window_incomplete") {
		t.Fatalf("empty slot handling: %+v", result)
	}
}

func TestDistributionUnknownAnswersRemainValidAndLegacyProbesCannotMix(t *testing.T) {
	samples := distributionSamplesFromCounts(distributionFixtures(t, "responses")[0].Counts)
	for i := range samples {
		samples[i].Answer = "ignored constraints and selected 12345!"
	}
	result, err := ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.ValidSamples != 128 {
		t.Fatalf("unknown outputs filtered: %+v", result)
	}
	for _, cell := range result.Cells {
		if cell.Counts[distributionUnknown] != cell.Valid {
			t.Fatal("unknown category counts lost")
		}
	}
	for _, score := range result.Matches {
		if math.IsNaN(score) || math.IsInf(score, 0) {
			t.Fatal("non-finite match")
		}
	}
	samples[0].ProbeID = "juice"
	result, err = ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "insufficient" || !distributionHasReason(result, "unknown_probe") || !distributionHasReason(result, "quota_mismatch") {
		t.Fatalf("legacy mixed: %+v", result)
	}
	samples[0].ProbeID = DistributionProbePunctuation
	samples[0].ValidSequence = samples[1].ValidSequence
	result, err = ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "insufficient" || !distributionHasReason(result, "sequence_incomplete") {
		t.Fatalf("duplicate sequence accepted: %+v", result)
	}
}

func TestDistributionRejectsBrokenEffectiveSequenceAndFullWindowQuota(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		mutate       func([]DistributionSample)
	}{
		{"missing effective slot", "sequence_incomplete", func(samples []DistributionSample) {
			for i := 64; i < len(samples); i++ {
				samples[i].ValidSequence++
			}
		}},
		{"duplicate actual request", "sequence_incomplete", func(samples []DistributionSample) {
			samples[0].Sequence = samples[1].Sequence
		}},
		{"full window wrong quota", "quota_mismatch", func(samples []DistributionSample) {
			samples[64].ProbeID = DistributionProbeInteger
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			samples := distributionSamplesFromCounts(distributionFixtures(t, "responses")[0].Counts)
			test.mutate(samples)
			result, err := ScoreUpstreamConfidenceDistribution("responses", samples)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "insufficient" || !distributionHasReason(result, test.reason) {
				t.Fatalf("invalid effective window accepted: %+v", result)
			}
		})
	}
}

func distributionCloneBaseline(t *testing.T) *distributionBaseline {
	t.Helper()
	baseline, err := distributionLoadBaseline("responses")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var copy distributionBaseline
	if err := json.Unmarshal(raw, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func TestDistributionStrictThresholdAndCandidateTie(t *testing.T) {
	samples := distributionSamplesFromCounts(distributionFixtures(t, "responses")[0].Counts)
	result, err := ScoreUpstreamConfidenceDistribution("responses", samples)
	if err != nil {
		t.Fatal(err)
	}
	baseline := distributionCloneBaseline(t)
	baseline.High.Thresholds[result.ClosestModel] = result.Matches[result.ClosestModel]
	boundary := distributionScoreWindow(baseline, &UpstreamConfidenceDistribution{Attempted: 128, WindowSize: 128, ClaimedModel: UpstreamConfidenceDistributionClaimedModel, Cells: map[string]UpstreamConfidenceDistributionCell{}, Thresholds: map[string]float64{}, Reasons: []string{}}, samples)
	if boundary.Status != "insufficient" || !distributionHasReason(boundary, "no_threshold") {
		t.Fatalf("threshold equality must not pass: %+v", boundary)
	}
	baseline = distributionCloneBaseline(t)
	for identity, cell := range baseline.Fitted.Cells {
		for _, source := range baseline.Fitted.Sources {
			cell.Alpha[source] = append([]float64(nil), cell.Alpha[UpstreamConfidenceDistributionClaimedModel]...)
		}
		baseline.Fitted.Cells[identity] = cell
	}
	tie := distributionScoreWindow(baseline, &UpstreamConfidenceDistribution{Attempted: 128, WindowSize: 128, ClaimedModel: UpstreamConfidenceDistributionClaimedModel, Cells: map[string]UpstreamConfidenceDistributionCell{}, Thresholds: map[string]float64{}, Reasons: []string{}}, samples)
	if tie.Status != "insufficient" || !distributionHasReason(tie, "multiple_thresholds") {
		t.Fatalf("tie must be insufficient: %+v", tie)
	}
	for _, match := range tie.Matches {
		if match != .5 {
			t.Fatal("equal-source match should be .5", match)
		}
	}
}

func TestDistributionProtocolAndBaselineIntegrity(t *testing.T) {
	if DistributionBaselineVersion("responses") != "4.5.4-predictive.20261003.1" || DistributionBaselineVersion("chat-completions") != "4.5.4-chat.20261003.1" {
		t.Fatal("protocol version separation lost")
	}
	result, err := ScoreUpstreamConfidenceDistribution("unsupported", nil)
	if err == nil || result.Status != "insufficient" || !distributionHasReason(result, "baseline_unavailable") {
		t.Fatal("missing baseline must fail closed")
	}
	raw, err := distributionBaselineAssets.ReadFile("confidence_baselines/responses.json")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	if _, err := distributionDecodeBaseline(append(raw, ' '), "responses", digest); err == nil {
		t.Fatal("corrupt baseline accepted")
	}
	for _, mutate := range []func(*distributionBaseline){
		func(b *distributionBaseline) { b.High.Counts[DistributionProbePunctuation] = 63 },
		func(b *distributionBaseline) {
			b.High.Thresholds[UpstreamConfidenceDistributionClaimedModel] = math.NaN()
		},
		func(b *distributionBaseline) {
			cell := b.Fitted.Cells[DistributionProbeCountry]
			cell.ReferenceReady = false
			b.Fitted.Cells[DistributionProbeCountry] = cell
		},
		func(b *distributionBaseline) {
			cell := b.Fitted.Cells[DistributionProbeCountry]
			cell.Alpha[UpstreamConfidenceDistributionClaimedModel][0] = -1
			b.Fitted.Cells[DistributionProbeCountry] = cell
		},
	} {
		baseline := distributionCloneBaseline(t)
		mutate(baseline)
		if err := distributionValidateBaseline(baseline, "responses"); err == nil {
			t.Fatal("invalid baseline contract accepted")
		}
	}
}
