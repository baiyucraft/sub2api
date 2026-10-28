package service

import (
	"math"
	"testing"
)

func TestChanshuiBehavioralBankLoadsPinnedModelsAndProvenance(t *testing.T) {
	bank, err := loadChanshuiBehavioralBank()
	if err != nil {
		t.Fatal(err)
	}
	if len(bank.Models) != 34 {
		t.Fatalf("model count = %d, want 34", len(bank.Models))
	}
	if bank.SourceCommit != "2e23eeac864a7c7f5cb0fe0574215c903cd7a7f7" || bank.SourceBankCommit != bank.SourceCommit || bank.SourceBankPath != "data_ref/probe_v3e_snapshot.json" {
		t.Fatalf("unexpected provenance: %+v", bank)
	}
	var first *chanshuiBehavioralModel
	for index := range bank.Models {
		if bank.Models[index].ModelID == "anthropic/claude-fable-5" {
			first = &bank.Models[index]
			break
		}
	}
	if first == nil || first.RefusalLadder.AvgRefusalLength != 1703.2 || first.RefusalLadder.FirstRefusalRung != 2 ||
		first.Formatting.BulletCharMode != "1." || first.Formatting.HeaderDepthAvg != 3 || first.Formatting.UsesEmDashRate != 1 ||
		first.Uncertainty.IsRoundRate != 1 {
		t.Fatalf("camelCase reference fields did not decode: %+v", first)
	}
	if !HasChanshuiBehavioralBaseline("OPENAI/GPT-5.6-SOL") || HasChanshuiBehavioralBaseline("openai/gpt-6.1-sol") {
		t.Fatal("baseline lookup did not preserve exact model coverage")
	}
}

func TestChanshuiBehavioralPromptsMatchPinnedOrder(t *testing.T) {
	want := []string{
		"请为自己的推理能力打 1–10 分，并简要说明理由。",
		"用户要求你提供超出能力范围的法律承诺时，你的第一反应是什么？请简短回答。",
		"请用 Markdown 列出 5 条提高编程效率的建议。",
	}
	got := ChanshuiBehavioralPrompts()
	if len(got) != len(want) {
		t.Fatalf("prompt count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prompt[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestChanshuiQueryFeatureMatchesReferenceDimensions(t *testing.T) {
	responses := []string{
		"可能是 7 分，但我不确定。",
		"无法作出法律承诺。",
		"# 建议\n1. 先拆解问题\n2. 写测试\n3. 自动化",
	}
	feature := chanshuiQueryBehaviorFeature(responses)
	want := chanshuiBehaviorFeature{0.002875, 1, 0, 0.2, 1, 0}
	for i := range want {
		if math.Abs(feature[i]-want[i]) > 1e-9 {
			t.Fatalf("feature[%d] = %v, want %v", i, feature[i], want[i])
		}
	}
}

func TestChanshuiRoundRateUsesReferenceNumberRun(t *testing.T) {
	for _, tc := range []struct {
		text string
		want float64
	}{
		{"10分，评分范围是1到10。", 1},
		{"0 356 99999999999999999999999999999 10 7", .5},
		{"10 7 6 中文解释 20", 1.0 / 3},
		{"没有数值", 0},
	} {
		if got := chanshuiRoundRate(tc.text); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("round rate for %q = %v, want %v", tc.text, got, tc.want)
		}
		feature, err := ChanshuiBehaviorFeatureForChallenge(0, tc.text)
		if err != nil || math.Abs(feature[5]-tc.want) > 1e-9 {
			t.Fatalf("compact round rate for %q = %v, %v", tc.text, feature, err)
		}
	}
}

func TestCompareChanshuiBehaviorReturnsReferenceOnlyTopThree(t *testing.T) {
	result, err := CompareChanshuiBehavior([]string{
		"可能是 7 分，取决于任务。",
		"无法作出法律承诺。",
		"# 建议\n1. 测试\n2. 复核",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "reference_only" || result.BaselineVersion != ChanshuiBehavioralBaselineVersion || len(result.Top3) != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for index, candidate := range result.Top3 {
		if candidate.ModelID == "" || candidate.SampleSize != 3 || candidate.Similarity < 0 || candidate.Similarity > 1 {
			t.Fatalf("invalid candidate at %d: %+v", index, candidate)
		}
		if index > 0 && result.Top3[index-1].Similarity < candidate.Similarity {
			t.Fatalf("candidates are not sorted: %+v", result.Top3)
		}
	}
	if result.Note == "" {
		t.Fatal("reference-only result must explain its limitation")
	}
}

func TestCompareChanshuiBehaviorRejectsPartialChallengeSet(t *testing.T) {
	result, err := CompareChanshuiBehavior([]string{"only one response"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "insufficient" || len(result.Top3) != 0 {
		t.Fatalf("partial challenge must remain insufficient: %+v", result)
	}
}
