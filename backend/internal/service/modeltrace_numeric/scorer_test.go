package modeltrace_numeric

import (
	"math"
	"strings"
	"testing"
)

func TestEmbeddedBankCoverageAndIdentityBoundary(t *testing.T) {
	if !HasTarget(" gpt-6-sol ") || HasTarget("GPT-6.1-SOL") {
		t.Fatal("target lookup must match exact IDs after case/whitespace folding only")
	}
	models, err := Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 18 {
		t.Fatalf("got %d models, want 18", len(models))
	}
	seen := map[string]bool{}
	for _, model := range models {
		if seen[model.ID] {
			t.Fatalf("duplicate model %q", model.ID)
		}
		seen[model.ID] = true
	}
	if !seen["gpt-6-sol"] || !seen["gpt-6-astra"] || !seen["gpt-6-luna"] {
		t.Fatal("ModelTrace GPT-6 candidates are missing")
	}
	if seen["gpt-6.1-sol"] {
		t.Fatal("bank must not invent gpt-6.1-sol")
	}
	input := numericText(220, 73, 19)
	result, err := Analyze([]Output{{Text: input}}, "gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unknown" || result.TargetPresent || result.Reason != "target_not_in_closed_set" {
		t.Fatalf("missing target must remain unknown: %#v", result)
	}
}

func TestParseNumbersLongestRunAndBounds(t *testing.T) {
	got := ParseNumbers("1 355 356 0 4abc5 6-7")
	if len(got) != 3 || got[0] != 1 || got[1] != 355 || got[2] != 4 {
		t.Fatalf("unexpected parsed run: %v", got)
	}
	if got := ParseNumbers("10 11 12 abc 20 21 22 23"); len(got) != 4 || got[0] != 20 {
		t.Fatalf("did not select longest letter-separated run: %v", got)
	}
}

func TestChallengePrompts(t *testing.T) {
	prompts := ChallengePrompts()
	wantCounts := []string{"292", "308", "324"}
	if len(prompts) != len(wantCounts) {
		t.Fatalf("got %d prompts, want %d", len(prompts), len(wantCounts))
	}
	for i, prompt := range prompts {
		if !strings.Contains(prompt, wantCounts[i]+" 个") {
			t.Errorf("prompt %d does not request %s integers: %s", i, wantCounts[i], prompt)
		}
		for _, required := range []string{"当前语言模型直接", "第一反应", "等差", "循环", "重复区块", "规则化模式", "偶然想到相同数字是有效的", "不要再排序、重排、去重、替换或修正", "直接从第一个取值开始", "完整输出序列", "不要解释"} {
			if !strings.Contains(prompt, required) {
				t.Errorf("prompt %d missing constraint %q", i, required)
			}
		}
		if !strings.Contains(prompt, "不要从 1 开始计数") {
			t.Errorf("prompt %d does not prohibit counting", i)
		}
		if strings.Contains(prompt, "输出 JSON") || strings.Contains(prompt, "依次") || strings.Contains(prompt, "必须按升序") {
			t.Errorf("prompt %d includes a structured or ordered pattern instruction", i)
		}
	}
}

func TestChallengeExpectedCountsAndThresholds(t *testing.T) {
	counts := ChallengeExpectedCounts()
	for i, wantCount := range []int{292, 308, 324} {
		if counts[i] != wantCount {
			t.Fatalf("challenge %d count=%d want %d", i, counts[i], wantCount)
		}
		minimum, err := MinimumForChallenge(i)
		wantMinimum := []int{161, 170, 179}[i]
		if err != nil || minimum != wantMinimum {
			t.Fatalf("challenge %d minimum=%d err=%v want %d", i, minimum, err, wantMinimum)
		}
		for _, size := range []int{minimum - 1, minimum} {
			result, err := Analyze([]Output{{Text: numericText(size, 73, 19), ExpectedCount: wantCount}}, "gpt-5.4")
			if err != nil {
				t.Fatal(err)
			}
			wantUsed := 0
			if size == minimum {
				wantUsed = 1
			}
			if result.UsedOutputs != wantUsed {
				t.Fatalf("challenge %d accepted %d numbers: used=%d want %d", i, size, result.UsedOutputs, wantUsed)
			}
		}
	}
	counts[0] = 0
	if ChallengeExpectedCounts()[0] != 292 {
		t.Fatal("caller changed shared challenge counts")
	}
	for _, invalid := range []int{-1, 3} {
		if _, err := MinimumForChallenge(invalid); err == nil {
			t.Fatalf("challenge %d must fail", invalid)
		}
	}
}

// Expected values come from the pinned ModelTrace fingerprint.py using the
// exact embedded unified_bank.json and all three challenge-specific thresholds.
func TestAnalyzeThreeExpectedCountsMatchPinnedReference(t *testing.T) {
	outputs := []Output{
		{Text: numericText(220, 73, 19), ExpectedCount: 292},
		{Text: numericText(180, 47, 101), ExpectedCount: 308},
		{Text: numericText(180, 29, 7), ExpectedCount: 324},
	}
	result, err := Analyze(outputs, "gpt-6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedOutputs != 3 || result.Status != "reference_only" {
		t.Fatalf("unexpected result: %#v", result)
	}
	for i, want := range []struct {
		model      string
		score, fit float64
	}{
		{"claude-opus-5-5", 1.4223628320895794, .7210659887524797},
		{"gpt-5.4", 1.3823370769399093, .7216139325370248},
		{"gpt-5.6-sol", .8712488322398934, .7139527758571373},
	} {
		got := result.Candidates[i]
		if got.Model != want.model || math.Abs(got.Score-want.score) > 1e-10 || math.Abs(got.RelativeFit-want.fit) > 1e-10 {
			t.Fatalf("candidate %d got %#v want %#v", i, got, want)
		}
	}
}

func TestAnalyzeMatchesReferenceImplementationFixture(t *testing.T) {
	inputs := []Output{
		{Text: numericText(220, 73, 19)},
		{Text: numericText(180, 47, 101)},
		{Text: numericText(100, 29, 7)},
	}
	want := []struct {
		model      string
		score, fit float64
	}{
		{"gpt-5.4", 1.4500483579110042, .5272172802074592},
		{"claude-opus-5-5", 1.227661057089815, .5280331777065046},
		{"gpt-6-luna", .9809056839950847, .5224490349267836},
	}
	result, err := Analyze(inputs[:1], "gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "reference_only" || result.UsedOutputs != 1 || len(result.Candidates) != 18 {
		t.Fatalf("unexpected result: %#v", result)
	}
	for i, expected := range want {
		candidate := result.Candidates[i]
		if candidate.Model != expected.model || math.Abs(candidate.Score-expected.score) > 1e-10 || math.Abs(candidate.RelativeFit-expected.fit) > 1e-10 {
			t.Fatalf("candidate %d mismatch: got %#v want %#v", i, candidate, expected)
		}
	}
	if strings.Contains(strings.ToLower(result.Status), "match") {
		t.Fatalf("relative ranking must not imply identity: %q", result.Status)
	}
	for _, tc := range []struct {
		count int
		model string
		score float64
		fit   float64
	}{
		{2, "claude-opus-5-5", 1.2836966949809288, .6603000920843283},
		{3, "claude-opus-5-5", 1.5122640514941779, .6905638124017972},
	} {
		got, err := Analyze(inputs[:tc.count], "gpt-6-sol")
		if err != nil {
			t.Fatal(err)
		}
		if got.UsedOutputs != tc.count || got.Status != "reference_only" || got.Candidates[0].Model != tc.model ||
			math.Abs(got.Candidates[0].Score-tc.score) > 1e-10 || math.Abs(got.Candidates[0].RelativeFit-tc.fit) > 1e-10 {
			t.Fatalf("%d-output reference mismatch: %#v", tc.count, got)
		}
	}
}

func TestAnalyzeRejectsShortOutputsAsUnknown(t *testing.T) {
	result, err := Analyze([]Output{{Text: numericText(79, 1, 1)}}, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unknown" || result.UsedOutputs != 0 || result.Reason != "insufficient_valid_outputs" {
		t.Fatalf("short output was accepted: %#v", result)
	}
}

func numericText(count, multiplier, offset int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = stringInt((i*multiplier+offset)%355 + 1)
	}
	return strings.Join(values, " ")
}

func stringInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
