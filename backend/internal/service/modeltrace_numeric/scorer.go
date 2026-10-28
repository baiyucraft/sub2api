// Package modeltrace_numeric provides a standalone, reference-only implementation
// of ModelTrace's numeric fingerprint scorer.
package modeltrace_numeric

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	SourceCommit     = "6cd4a8ff0f3f4e49d6d6969dd2fbadfa1569fde2"
	SourcePath       = "xqy2006/ModelTrace:data/unified_bank.json"
	SourceBankSHA256 = "eeab6ff54d6ffb2b294ebfd6cce25bce387f7a268b66759144f64c16f6c66b9f"
	BankSHA256       = "83d6364d6c2586edc647e26685945c27e3c8cba692470ebf2098182e5ac7a266"
	ValueMin         = 1
	ValueMax         = 355
	Dimension        = ValueMax - ValueMin + 1
	MinimumValid     = 80
)

// ChallengePrompts returns ModelTrace-style fixed Chinese long-integer
// challenges. Each prompt is a separate observation and asks for the full
// sequence without explanation or tool use.
func ChallengePrompts() []string {
	// Preserve the pinned challenge_suite.py format_prompt(..., style="zh")
	// constraints, including valid accidental repetitions and no output repair.
	const suffix = " 本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器。每个位置都要单独凭第一反应选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。偶然想到相同数字是有效的；每项写出后不要再排序、重排、去重、替换或修正。数字之间用清晰的常见分隔符，直接从第一个取值开始，完整输出序列，不要解释。"
	return []string{
		"这是一次无语义数值选择。请为 292 个位置分别凭第一反应选择一个 1 到 355（含端点）的整数。" + suffix,
		"请完成无语义整数选择任务：逐项给出 308 个闭区间 [1, 355] 内的整数，每次只决定当前一项。" + suffix,
		"进行一轮快速逐项取值：分别给出 324 个取值范围为 1 至 355 的整数。" + suffix,
	}
}

// ChallengeExpectedCounts returns the requested numeric sequence lengths in
// challenge order. The returned slice can be modified by callers.
func ChallengeExpectedCounts() []int { return []int{292, 308, 324} }

// MinimumForChallenge preserves ModelTrace's 55 percent completion threshold
// for each fixed challenge instead of accepting only its generic 80-number floor.
func MinimumForChallenge(index int) (int, error) {
	counts := ChallengeExpectedCounts()
	if index < 0 || index >= len(counts) {
		return 0, fmt.Errorf("invalid ModelTrace challenge index %d", index)
	}
	return minimumValidNumbers(counts[index]), nil
}

func minimumValidNumbers(expectedCount int) int {
	return max(MinimumValid, int(math.Ceil(float64(expectedCount)*.55)))
}

//go:embed bank.json
var bankBytes []byte
var bankOnce sync.Once
var embeddedBank *bank
var embeddedBankErr error

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Family      string `json:"family"`
	FamilyName  string `json:"family_name"`
	Counts      []int  `json:"counts"`
}

type Calibration struct {
	Beta float64 `json:"beta"`
}

type artifact struct {
	FeatureMean          []float64     `json:"feature_mean"`
	FeatureScale         []float64     `json:"feature_scale"`
	NuisanceBasis        [][]float64   `json:"nuisance_basis"`
	Centroids            [][]float64   `json:"centroids"`
	EnvironmentCentroids [][][]float64 `json:"environment_centroids"`
	Weight               float64       `json:"weight"`
}

type bank struct {
	Schema  string `json:"schema"`
	BuiltAt string `json:"built_at"`
	Method  struct {
		Name               string  `json:"name"`
		Range              []int   `json:"range"`
		Alpha              float64 `json:"alpha"`
		OrderedBlockWeight float64 `json:"ordered_block_weight"`
	} `json:"method"`
	RecommendedQueries  int     `json:"recommended_queries"`
	MinimumValidNumbers int     `json:"minimum_valid_numbers"`
	Models              []Model `json:"models"`
	Robust              struct {
		ModelOrder    []string `json:"model_order"`
		Hellinger     artifact `json:"hellinger"`
		OrderedBlocks artifact `json:"ordered_blocks"`
	} `json:"robust"`
	Calibration map[string]Calibration `json:"calibration"`
}

type Output struct {
	Text          string
	ExpectedCount int
}

type Candidate struct {
	Model            string  `json:"model"`
	DisplayName      string  `json:"display_name"`
	Family           string  `json:"family"`
	Score            float64 `json:"score"`
	RelativeFit      float64 `json:"relative_fit"`
	ClosedSetSupport float64 `json:"closed_set_support"`
}

type Result struct {
	Status        string      `json:"status"`
	Reason        string      `json:"reason,omitempty"`
	Method        string      `json:"method"`
	SourceCommit  string      `json:"source_commit"`
	BankVersion   string      `json:"bank_version"`
	UsedOutputs   int         `json:"used_outputs"`
	Candidates    []Candidate `json:"candidates"`
	TargetPresent bool        `json:"target_present"`
}

var digits = regexp.MustCompile(`[0-9]+`)

func loadBank() (*bank, error) {
	bankOnce.Do(func() {
		h := sha256.Sum256(bankBytes)
		if hex.EncodeToString(h[:]) != BankSHA256 {
			embeddedBankErr = fmt.Errorf("ModelTrace numeric bank checksum mismatch")
			return
		}
		var b bank
		if err := json.Unmarshal(bankBytes, &b); err != nil {
			embeddedBankErr = fmt.Errorf("decode ModelTrace numeric bank: %w", err)
			return
		}
		if err := validateBank(&b); err != nil {
			embeddedBankErr = err
			return
		}
		embeddedBank = &b
	})
	return embeddedBank, embeddedBankErr
}

func validateBank(b *bank) error {
	if b.Schema != "robust-number-fingerprint-bank" || b.MinimumValidNumbers != MinimumValid || len(b.Models) != 18 || len(b.Calibration) != 3 || len(b.Robust.ModelOrder) != len(b.Models) {
		return fmt.Errorf("ModelTrace numeric bank contract mismatch")
	}
	for i, m := range b.Models {
		if m.ID == "" || i >= len(b.Robust.ModelOrder) || b.Robust.ModelOrder[i] != m.ID || len(m.Counts) != Dimension {
			return fmt.Errorf("ModelTrace numeric model contract mismatch at %d", i)
		}
	}
	for _, key := range []string{"1", "2", "3"} {
		if c, ok := b.Calibration[key]; !ok || c.Beta <= 0 || math.IsNaN(c.Beta) || math.IsInf(c.Beta, 0) {
			return fmt.Errorf("ModelTrace calibration %s unavailable", key)
		}
	}
	return nil
}

// Models returns a copy of the exact closed-set candidate catalog.
func Models() ([]Model, error) {
	b, err := loadBank()
	if err != nil {
		return nil, err
	}
	out := make([]Model, len(b.Models))
	for i, model := range b.Models {
		out[i] = model
		out[i].Counts = append([]int(nil), model.Counts...)
	}
	return out, nil
}

// HasTarget reports whether targetModel is an exact ID in the embedded bank.
// Matching folds case and surrounding whitespace only; it does not resolve aliases.
func HasTarget(targetModel string) bool {
	targetModel = strings.ToLower(strings.TrimSpace(targetModel))
	if targetModel == "" {
		return false
	}
	b, err := loadBank()
	if err != nil {
		return false
	}
	for _, model := range b.Models {
		if strings.ToLower(strings.TrimSpace(model.ID)) == targetModel {
			return true
		}
	}
	return false
}

// ParseNumbers mirrors ModelTrace's longest digit-run parser (letter-separated
// numeric runs are split; only values 1..355 are retained).
func ParseNumbers(text string) []int {
	matches := digits.FindAllStringIndex(text, -1)
	var runs [][]int
	current := make([]int, 0)
	previousEnd := 0
	for _, span := range matches {
		separator := []rune(text[previousEnd:span[0]])
		value := 0
		for _, r := range text[span[0]:span[1]] {
			value = value*10 + int(r-'0')
			if value > ValueMax {
				break
			}
		}
		letterSeparator := false
		for _, r := range separator {
			if unicode.IsLetter(r) {
				letterSeparator = true
				break
			}
		}
		if len(current) > 0 && letterSeparator {
			runs = append(runs, current)
			current = make([]int, 0)
		}
		if value >= ValueMin && value <= ValueMax {
			current = append(current, value)
		}
		previousEnd = span[1]
	}
	if len(current) > 0 {
		runs = append(runs, current)
	}
	best := []int{}
	for _, run := range runs {
		if len(run) > len(best) {
			best = run
		}
	}
	return best
}

// Analyze produces relative candidate fit. A closed-set top candidate is never
// promoted to a confirmed identity; absent requested labels stay unknown.
func Analyze(outputs []Output, targetModel string) (Result, error) {
	b, err := loadBank()
	if err != nil {
		return Result{}, err
	}
	out := Result{Status: "unknown", Reason: "insufficient_valid_outputs", Method: b.Method.Name, SourceCommit: SourceCommit, BankVersion: b.BuiltAt, Candidates: []Candidate{}}
	for _, m := range b.Models {
		if strings.EqualFold(strings.TrimSpace(m.ID), strings.TrimSpace(targetModel)) {
			out.TargetPresent = true
			break
		}
	}
	if !out.TargetPresent && strings.TrimSpace(targetModel) != "" {
		out.Reason = "target_not_in_closed_set"
	}
	type accepted struct {
		numbers []int
		counts  []int
		scores  []float64
	}
	valid := make([]accepted, 0, 3)
	for _, item := range outputs {
		if len(valid) == 3 {
			break
		}
		numbers := ParseNumbers(item.Text)
		minimum := minimumValidNumbers(item.ExpectedCount)
		if len(numbers) < minimum {
			continue
		}
		counts := countNumbers(numbers)
		scores := robustScoreNumbers(numbers, b)
		valid = append(valid, accepted{numbers, counts, scores})
	}
	if len(valid) == 0 {
		return out, nil
	}
	out.UsedOutputs = len(valid)
	combined := make([]float64, len(b.Models))
	pooled := make([]int, Dimension)
	for _, v := range valid {
		for i, score := range v.scores {
			combined[i] += score / float64(len(valid))
		}
		for i, n := range v.counts {
			pooled[i] += n
		}
	}
	cal := b.Calibration[fmt.Sprint(len(valid))]
	probabilities := softmaxScaled(combined, cal.Beta)
	for i, m := range b.Models {
		out.Candidates = append(out.Candidates, Candidate{Model: m.ID, DisplayName: m.DisplayName, Family: m.Family, Score: combined[i], RelativeFit: jsSimilarity(pooled, m.Counts), ClosedSetSupport: probabilities[i]})
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		return probabilities[indexOfModel(b.Models, out.Candidates[i].Model)] > probabilities[indexOfModel(b.Models, out.Candidates[j].Model)]
	})
	if out.TargetPresent {
		out.Status = "reference_only"
		out.Reason = "closed_set_relative_fit_not_identity_probability"
	}
	return out, nil
}

func indexOfModel(models []Model, id string) int {
	for i, m := range models {
		if m.ID == id {
			return i
		}
	}
	return 0
}
func countNumbers(numbers []int) []int {
	out := make([]int, Dimension)
	for _, n := range numbers {
		if n >= ValueMin && n <= ValueMax {
			out[n-ValueMin]++
		}
	}
	return out
}
func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
func standardize(v []float64) []float64 {
	c := mean(v)
	variance := 0.0
	for _, x := range v {
		variance += (x - c) * (x - c)
	}
	variance /= float64(max(1, len(v)))
	scale := math.Max(math.Sqrt(variance), 1e-12)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = (x - c) / scale
	}
	return out
}
func dot(a, b []float64) float64 {
	s := 0.0
	for i := 0; i < len(a) && i < len(b); i++ {
		s += a[i] * b[i]
	}
	return s
}
func norm(v []float64) float64 { return math.Sqrt(dot(v, v)) }
func normalized(v []float64) []float64 {
	n := math.Max(norm(v), 1e-12)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
func subtractBasis(v []float64, basis [][]float64) []float64 {
	out := append([]float64(nil), v...)
	for _, vec := range basis {
		p := dot(out, vec)
		for i := range out {
			if i < len(vec) {
				out[i] -= p * vec[i]
			}
		}
	}
	return out
}
func hellingerFeature(counts []int) []float64 {
	total := float64(0.5 * Dimension)
	for _, n := range counts {
		total += float64(n)
	}
	out := make([]float64, Dimension)
	for i, n := range counts {
		out[i] = math.Sqrt((float64(n) + .5) / total)
	}
	return out
}
func robustScoreCounts(counts []int, b *bank) []float64 {
	a := b.Robust.Hellinger
	f := hellingerFeature(counts)
	for i := range f {
		f[i] = (f[i] - a.FeatureMean[i]) / a.FeatureScale[i]
	}
	p := normalized(subtractBasis(f, a.NuisanceBasis))
	scores := make([]float64, len(a.Centroids))
	for i, c := range a.Centroids {
		scores[i] = dot(p, c)
	}
	return standardize(scores)
}
func splitFour(v []int) [][]int {
	chunks := make([][]int, 4)
	base, rem := len(v)/4, len(v)%4
	start := 0
	for i := 0; i < 4; i++ {
		n := base
		if i < rem {
			n++
		}
		chunks[i] = v[start : start+n]
		start += n
	}
	return chunks
}
func orderedFeature(numbers []int) []float64 {
	pieces := make([]float64, 0, 74)
	for _, chunk := range splitFour(numbers) {
		bins := make([]float64, 16)
		for i := range bins {
			bins[i] = .5
		}
		for _, n := range chunk {
			idx := min(15, int(math.Floor(float64(n-1)/355*16)))
			bins[idx]++
		}
		total := 0.0
		for _, x := range bins {
			total += x
		}
		for _, x := range bins {
			pieces = append(pieces, math.Sqrt(x/total))
		}
	}
	last := make([]float64, 10)
	for i := range last {
		last[i] = .5
	}
	for _, n := range numbers {
		last[n%10]++
	}
	total := 0.0
	for _, x := range last {
		total += x
	}
	for _, x := range last {
		pieces = append(pieces, math.Sqrt(x/total))
	}
	return pieces
}
func orderedBlockScores(numbers []int, b *bank) []float64 {
	a := b.Robust.OrderedBlocks
	f := orderedFeature(numbers)
	for i := range f {
		f[i] = (f[i] - a.FeatureMean[i]) / a.FeatureScale[i]
	}
	unit := normalized(f)
	env := make([][]float64, len(a.EnvironmentCentroids))
	for i, models := range a.EnvironmentCentroids {
		env[i] = make([]float64, len(models))
		for j, c := range models {
			env[i][j] = dot(unit, c)
		}
	}
	template := make([]float64, len(a.Centroids))
	for j := range template {
		for i := range env {
			if env[i][j] > template[j] || i == 0 {
				template[j] = env[i][j]
			}
		}
	}
	template = standardize(template)
	projected := normalized(subtractBasis(f, a.NuisanceBasis))
	nuisance := make([]float64, len(a.Centroids))
	for i, c := range a.Centroids {
		nuisance[i] = dot(projected, c)
	}
	nuisance = standardize(nuisance)
	mixed := make([]float64, len(template))
	for i := range mixed {
		mixed[i] = .5*template[i] + .5*nuisance[i]
	}
	return standardize(mixed)
}
func robustScoreNumbers(numbers []int, b *bank) []float64 {
	marginal := robustScoreCounts(countNumbers(numbers), b)
	weight := b.Robust.OrderedBlocks.Weight
	if weight == 0 {
		return marginal
	}
	ordered := orderedBlockScores(numbers, b)
	for i := range marginal {
		marginal[i] = (1-weight)*marginal[i] + weight*ordered[i]
	}
	return marginal
}
func softmaxScaled(values []float64, beta float64) []float64 {
	maxv := math.Inf(-1)
	for _, x := range values {
		if x*beta > maxv {
			maxv = x * beta
		}
	}
	out := make([]float64, len(values))
	total := 0.0
	for i, x := range values {
		out[i] = math.Exp(x*beta - maxv)
		total += out[i]
	}
	for i := range out {
		out[i] /= total
	}
	return out
}
func jsSimilarity(left, right []int) float64 {
	lt, rt := 0.0, 0.5*Dimension
	for _, x := range left {
		lt += float64(x)
	}
	for _, x := range right {
		rt += float64(x)
	}
	js := 0.0
	for i := range left {
		p := float64(left[i]) / lt
		q := (float64(right[i]) + .5) / rt
		m := (p + q) / 2
		if p > 0 {
			js += .5 * p * math.Log(p/m)
		}
		if q > 0 {
			js += .5 * q * math.Log(q/m)
		}
	}
	return 1 - math.Sqrt(js/math.Log(2))
}
