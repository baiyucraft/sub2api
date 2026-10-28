package service

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/service/modeltrace_numeric"
)

const (
	ChanshuiBehavioralBaselineVersion = "chanshui-v3e-2e23eea"
	chanshuiBehavioralAssetSHA256     = "e5dddb90e2116f9313121a21581196db83a864dcc503b36dd9be98f0b52adcc4"
)

//go:embed chanshui_behavioral_baseline.json
var chanshuiBehavioralAsset embed.FS

type ChanshuiBehavioralProbe struct {
	ModelID     string  `json:"model_id"`
	DisplayName string  `json:"display_name"`
	Family      string  `json:"family"`
	Similarity  float64 `json:"similarity"`
	SampleSize  int     `json:"sample_size"`
}

type ChanshuiBehavioralResult struct {
	Status           string                    `json:"status"`
	BaselineVersion  string                    `json:"baseline_version,omitempty"`
	ChallengeVersion string                    `json:"challenge_version,omitempty"`
	SampleCount      int                       `json:"sample_count"`
	Top3             []ChanshuiBehavioralProbe `json:"top3"`
	Note             string                    `json:"note"`
}

type chanshuiBehavioralBank struct {
	SourceRepository string                    `json:"source_repository"`
	SourceCommit     string                    `json:"source_commit"`
	SourcePath       string                    `json:"source_path"`
	SourceBankRepo   string                    `json:"source_bank_repository"`
	SourceBankCommit string                    `json:"source_bank_commit"`
	SourceBankPath   string                    `json:"source_bank_path"`
	ChallengeVersion string                    `json:"challenge_version"`
	FeatureVersion   string                    `json:"feature_version"`
	Models           []chanshuiBehavioralModel `json:"models"`
}

type chanshuiBehavioralModel struct {
	ModelID       string                        `json:"model_id"`
	Family        string                        `json:"family"`
	DisplayName   string                        `json:"display_name"`
	RefusalLadder chanshuiBehavioralRefusal     `json:"refusal_ladder"`
	Formatting    chanshuiBehavioralFormatting  `json:"formatting"`
	Uncertainty   chanshuiBehavioralUncertainty `json:"uncertainty"`
	SampleSize    int                           `json:"sample_size"`
	UpdatedAt     string                        `json:"updated_at"`
}

type chanshuiBehavioralRefusal struct {
	AvgRefusalLength float64 `json:"avgRefusalLengthAvg"`
	FirstRefusalRung float64 `json:"firstRefusalRungAvg"`
}

type chanshuiBehavioralFormatting struct {
	BulletCharMode string  `json:"bulletCharMode"`
	HeaderDepthAvg float64 `json:"headerDepthAvg"`
	UsesEmDashRate float64 `json:"usesEmDashRate"`
}

type chanshuiBehavioralUncertainty struct {
	IsRoundRate float64 `json:"isRoundRate"`
}

type chanshuiBehaviorFeature [6]float64

// ChanshuiBehavioralPrompts returns the three prompts used by the pinned
// Chanshui v3e benign-rewrite behavioral baseline, in feature-extraction order.
func ChanshuiBehavioralPrompts() []string {
	return []string{
		"请为自己的推理能力打 1–10 分，并简要说明理由。",
		"用户要求你提供超出能力范围的法律承诺时，你的第一反应是什么？请简短回答。",
		"请用 Markdown 列出 5 条提高编程效率的建议。",
	}
}

var (
	chanshuiBankOnce     sync.Once
	chanshuiBank         *chanshuiBehavioralBank
	chanshuiBankErr      error
	chanshuiHedgeMarkers = []string{"无法", "可能", "或许", "不确定", "仅供参考", "取决于", "大致", "大约", "不能保证"}
)

func loadChanshuiBehavioralBank() (*chanshuiBehavioralBank, error) {
	chanshuiBankOnce.Do(func() {
		raw, err := chanshuiBehavioralAsset.ReadFile("chanshui_behavioral_baseline.json")
		if err != nil {
			chanshuiBankErr = err
			return
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != chanshuiBehavioralAssetSHA256 {
			chanshuiBankErr = fmt.Errorf("Chanshui behavioral baseline checksum mismatch")
			return
		}
		var bank chanshuiBehavioralBank
		if err := json.Unmarshal(raw, &bank); err != nil {
			chanshuiBankErr = fmt.Errorf("decode Chanshui behavioral baseline: %w", err)
			return
		}
		if bank.SourceRepository != "kiss-kedaya/chanshui" || bank.SourceCommit != "2e23eeac864a7c7f5cb0fe0574215c903cd7a7f7" ||
			bank.SourceBankRepo != "kiss-kedaya/chanshui" || bank.SourceBankCommit != "2e23eeac864a7c7f5cb0fe0574215c903cd7a7f7" || bank.SourceBankPath != "data_ref/probe_v3e_snapshot.json" ||
			bank.ChallengeVersion != "llmprobe-v0.10.1-benign-rewrite" || bank.FeatureVersion != "chanshui-behavior-v1" || len(bank.Models) != 34 {
			chanshuiBankErr = fmt.Errorf("Chanshui behavioral baseline metadata mismatch")
			return
		}
		seen := make(map[string]struct{}, len(bank.Models))
		for _, model := range bank.Models {
			if model.ModelID == "" || model.SampleSize != 3 || model.RefusalLadder.AvgRefusalLength <= 0 || model.RefusalLadder.FirstRefusalRung < 0 ||
				model.Formatting.BulletCharMode == "" ||
				model.Formatting.HeaderDepthAvg < 0 || model.Formatting.UsesEmDashRate < 0 || model.Formatting.UsesEmDashRate > 1 ||
				model.Uncertainty.IsRoundRate < 0 || model.Uncertainty.IsRoundRate > 1 {
				chanshuiBankErr = fmt.Errorf("invalid Chanshui behavioral baseline model %q", model.ModelID)
				return
			}
			if _, ok := seen[model.ModelID]; ok {
				chanshuiBankErr = fmt.Errorf("duplicate Chanshui behavioral baseline model %q", model.ModelID)
				return
			}
			seen[model.ModelID] = struct{}{}
		}
		chanshuiBank = &bank
	})
	return chanshuiBank, chanshuiBankErr
}

// CompareChanshuiBehavior compares one complete three-response challenge set.
// Similarity is diagnostic evidence only; it is not a match or probability.
func CompareChanshuiBehavior(responses []string) (ChanshuiBehavioralResult, error) {
	result := ChanshuiBehavioralResult{Status: "insufficient", SampleCount: len(responses), Top3: []ChanshuiBehavioralProbe{}, Note: "需要同一轮的三条 Chanshui 行为挑战响应"}
	if len(responses) != 3 {
		return result, nil
	}
	bank, err := loadChanshuiBehavioralBank()
	if err != nil {
		result.Status = "unavailable"
		result.Note = "行为基线不可用"
		return result, err
	}
	result.Status = "reference_only"
	result.BaselineVersion = ChanshuiBehavioralBaselineVersion
	result.ChallengeVersion = bank.ChallengeVersion
	result.Note = "最相似候选仅作参考，不代表模型匹配或身份概率"
	query := chanshuiQueryBehaviorFeature(responses)
	return compareChanshuiBehaviorFeature(query, bank, result)
}

// ChanshuiBehaviorFeatureForChallenge extracts only the compact feature
// contribution for one response. Raw challenge text need not be persisted.
func ChanshuiBehaviorFeatureForChallenge(index int, response string) ([]float64, error) {
	if index < 0 || index > 2 {
		return nil, fmt.Errorf("invalid Chanshui challenge index %d", index)
	}
	feature := chanshuiBehaviorFeature{}
	switch index {
	case 0:
		runes := []rune(response)
		if len(runes) > 4000 {
			runes = runes[:4000]
		}
		feature[0] = chanshuiClamp01(float64(len(runes)) / 4000)
		if chanshuiFirstSentenceHedge(response) {
			feature[1] = 1
		}
		feature[5] = chanshuiRoundRate(response)
	case 1:
		runes := []rune(response)
		if len(runes) > 4000 {
			runes = runes[:4000]
		}
		feature[0] = chanshuiClamp01(float64(len(runes)) / 4000)
		if chanshuiFirstSentenceHedge(response) {
			feature[1] = 1
		}
	case 2:
		runes := float64(utf8.RuneCountInString(response))
		if runes > 0 {
			feature[2] = chanshuiClamp01(float64(strings.Count(response, "—")) / runes)
		}
		headers := 0
		for _, line := range strings.Split(response, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				headers++
			}
		}
		feature[3] = chanshuiClamp01(float64(headers) / 5)
		if strings.Contains(response, "- ") || strings.Contains(response, "• ") {
			feature[4] = 0
		} else if strings.Contains(response, "1.") {
			feature[4] = 1
		}
	}
	return append([]float64(nil), feature[:]...), nil
}

// CompareChanshuiBehaviorFeatures compares one complete set of three compact
// feature contributions against the pinned bank.
func CompareChanshuiBehaviorFeatures(features [][]float64) (ChanshuiBehavioralResult, error) {
	result := ChanshuiBehavioralResult{Status: "insufficient", SampleCount: len(features), Top3: []ChanshuiBehavioralProbe{}, Note: "需要同一轮的三条 Chanshui 行为挑战响应"}
	if len(features) != 3 {
		return result, nil
	}
	bank, err := loadChanshuiBehavioralBank()
	if err != nil {
		result.Status = "unavailable"
		result.Note = "行为基线不可用"
		return result, err
	}
	query := chanshuiBehaviorFeature{}
	for _, feature := range features {
		if len(feature) != len(query) {
			return result, fmt.Errorf("invalid Chanshui feature width")
		}
	}
	query[0] = (features[0][0] + features[1][0]) / 2
	query[1] = math.Max(features[0][1], features[1][1])
	copy(query[2:5], features[2][2:5])
	query[5] = features[0][5]
	result.Status = "reference_only"
	result.BaselineVersion = ChanshuiBehavioralBaselineVersion
	result.ChallengeVersion = bank.ChallengeVersion
	result.Note = "最相似候选仅作参考，不代表模型匹配或身份概率"
	return compareChanshuiBehaviorFeature(query, bank, result)
}

func compareChanshuiBehaviorFeature(query chanshuiBehaviorFeature, bank *chanshuiBehavioralBank, result ChanshuiBehavioralResult) (ChanshuiBehavioralResult, error) {
	for _, model := range bank.Models {
		feature := chanshuiBankBehaviorFeature(model)
		result.Top3 = append(result.Top3, ChanshuiBehavioralProbe{
			ModelID: model.ModelID, DisplayName: model.DisplayName, Family: model.Family,
			Similarity: chanshuiBehaviorCosine(query, feature), SampleSize: model.SampleSize,
		})
	}
	sort.SliceStable(result.Top3, func(i, j int) bool { return result.Top3[i].Similarity > result.Top3[j].Similarity })
	if len(result.Top3) > 3 {
		result.Top3 = result.Top3[:3]
	}
	return result, nil
}

func HasChanshuiBehavioralBaseline(modelID string) bool {
	bank, err := loadChanshuiBehavioralBank()
	if err != nil {
		return false
	}
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	for _, model := range bank.Models {
		if strings.EqualFold(model.ModelID, modelID) {
			return true
		}
	}
	return false
}

func chanshuiBankBehaviorFeature(model chanshuiBehavioralModel) chanshuiBehaviorFeature {
	var feature chanshuiBehaviorFeature
	feature[0] = chanshuiClamp01(model.RefusalLadder.AvgRefusalLength / 4000)
	feature[1] = chanshuiClamp01(model.RefusalLadder.FirstRefusalRung / 8)
	feature[2] = chanshuiClamp01(model.Formatting.UsesEmDashRate)
	feature[3] = chanshuiClamp01(model.Formatting.HeaderDepthAvg / 5)
	if model.Formatting.BulletCharMode == "1." {
		feature[4] = 1
	}
	feature[5] = chanshuiClamp01(model.Uncertainty.IsRoundRate)
	return feature
}

func chanshuiQueryBehaviorFeature(responses []string) chanshuiBehaviorFeature {
	var feature chanshuiBehaviorFeature
	refusalLength := 0
	for i := 0; i < 2; i++ {
		runes := []rune(responses[i])
		if len(runes) > 4000 {
			runes = runes[:4000]
		}
		refusalLength += len(runes)
	}
	feature[0] = chanshuiClamp01(float64(refusalLength) / 2 / 4000)
	for i := 0; i < 2; i++ {
		if chanshuiFirstSentenceHedge(responses[i]) {
			feature[1] = 1
			break
		}
	}
	markdown := responses[2]
	runes := float64(utf8.RuneCountInString(markdown))
	if runes > 0 {
		feature[2] = chanshuiClamp01(float64(strings.Count(markdown, "—")) / runes)
	}
	headers := 0
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			headers++
		}
	}
	feature[3] = chanshuiClamp01(float64(headers) / 5)
	if strings.Contains(markdown, "- ") || strings.Contains(markdown, "• ") {
		feature[4] = 0
	} else if strings.Contains(markdown, "1.") {
		feature[4] = 1
	}
	feature[5] = chanshuiRoundRate(responses[0])
	return feature
}

func chanshuiFirstSentenceHedge(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if index := strings.IndexAny(text, "。！？\n"); index >= 0 {
		text = text[:index]
	}
	for _, marker := range chanshuiHedgeMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func chanshuiRoundRate(text string) float64 {
	values := modeltrace_numeric.ParseNumbers(text)
	if len(values) == 0 {
		return 0
	}
	round := 0
	for _, number := range values {
		if number%5 == 0 || number%10 == 0 {
			round++
		}
	}
	return float64(round) / float64(len(values))
}

func chanshuiBehaviorCosine(left, right chanshuiBehaviorFeature) float64 {
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	similarity := dot / math.Sqrt(leftNorm*rightNorm)
	return chanshuiClamp01(similarity)
}

func chanshuiClamp01(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
