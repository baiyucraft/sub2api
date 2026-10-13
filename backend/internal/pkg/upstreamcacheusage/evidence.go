// Package upstreamcacheusage carries immutable, pre-billing upstream cache facts.
// It deliberately contains no prompts, cache keys, session identifiers or credentials.
package upstreamcacheusage

import (
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

type Status string

const (
	Known       Status = "known"
	Absent      Status = "absent"
	Invalid     Status = "invalid"
	Unsupported Status = "unsupported"
)

type Evidence struct {
	Status              Status
	Protocol            string
	TotalInputTokens    int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Source              string
	CompletedAt         time.Time
	Success             bool
	// Internal transport evidence; never persisted as a token usage field.
	StreamIncomplete bool `json:"-"`
}

type RawUsage = Evidence

func (e Evidence) Known() bool {
	return e.Status == Known && e.TotalInputTokens > 0 && e.CacheReadTokens >= 0 &&
		e.CacheReadTokens <= e.TotalInputTokens && e.CacheCreationTokens >= 0 &&
		e.CacheCreationTokens <= e.TotalInputTokens-e.CacheReadTokens
}

func (e Evidence) Rate() (float64, bool) {
	if !e.Known() {
		return 0, false
	}
	return float64(e.CacheReadTokens) / float64(e.TotalInputTokens) * 100, true
}

// Complete returns a copy; billing transforms and later stream frames cannot
// alter a completed fact retained by a recorder.
func Complete(e *Evidence, protocol string, success bool, at time.Time) *Evidence {
	out := Evidence{Status: Absent, Protocol: protocol}
	if e != nil {
		out = *e
	}
	if protocol != "" {
		out.Protocol = protocol
	}
	out.Success, out.CompletedAt = success, at.UTC()
	return &out
}

func field(value gjson.Result, paths ...string) (int64, bool, bool) {
	for _, path := range paths {
		v := value.Get(path)
		if !v.Exists() {
			continue
		}
		if v.Type != gjson.Number {
			return 0, true, false
		}
		n, err := strconv.ParseInt(v.Raw, 10, 64)
		return n, true, err == nil && n >= 0
	}
	return 0, false, true
}

func ParseOpenAI(value gjson.Result) *Evidence {
	e := &Evidence{Status: Absent, Protocol: "openai", Source: "usage"}
	if !value.Exists() {
		return e
	}
	if !value.IsObject() {
		e.Status = Invalid
		return e
	}
	total, hasTotal, totalOK := field(value, "input_tokens", "prompt_tokens")
	read, hasRead, readOK := field(value, "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cache_read_input_tokens", "cache_read_tokens", "cached_tokens")
	create, _, createOK := field(value, "input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens")
	e.TotalInputTokens, e.CacheReadTokens, e.CacheCreationTokens = total, read, create
	if !totalOK || !readOK || !createOK {
		e.Status = Invalid
		return e
	}
	if !hasTotal || !hasRead {
		return e
	}
	if read > total || create > total-read {
		e.Status = Invalid
		return e
	}
	e.Status = Known
	return e
}

func ParseAnthropic(value gjson.Result) *Evidence {
	e := &Evidence{Status: Absent, Protocol: "anthropic", Source: "usage"}
	if !value.Exists() {
		return e
	}
	if !value.IsObject() {
		e.Status = Invalid
		return e
	}
	// Compatible Anthropic endpoints may declare prompt_tokens as the inclusive
	// total. Prefer that explicit schema instead of adding cache tokens twice.
	if value.Get("prompt_tokens").Exists() {
		total, hasTotal, totalOK := field(value, "prompt_tokens")
		read, hasRead, readOK := field(value, "cache_read_input_tokens", "prompt_tokens_details.cached_tokens", "cached_tokens", "prompt_cache_hit_tokens")
		create, _, createOK := field(value, "cache_creation_input_tokens")
		e.TotalInputTokens, e.CacheReadTokens, e.CacheCreationTokens = total, read, create
		if !totalOK || !readOK || !createOK || read > total || create > total-read {
			e.Status = Invalid
			return e
		}
		if hasTotal && hasRead {
			e.Status = Known
		}
		return e
	}
	input, hasInput, inputOK := field(value, "input_tokens")
	read, hasRead, readOK := field(value, "cache_read_input_tokens")
	create, _, createOK := field(value, "cache_creation_input_tokens")
	const maxInt64 = int64(^uint64(0) >> 1)
	if !inputOK || !readOK || !createOK || input > maxInt64-read || input+read > maxInt64-create {
		e.Status = Invalid
		return e
	}
	e.TotalInputTokens, e.CacheReadTokens, e.CacheCreationTokens = input+read+create, read, create
	if !hasInput || !hasRead {
		return e
	}
	e.Status = Known
	return e
}

func ParseGemini(value gjson.Result) *Evidence {
	e := &Evidence{Status: Absent, Protocol: "gemini", Source: "usageMetadata"}
	if !value.Exists() {
		return e
	}
	if !value.IsObject() {
		e.Status = Invalid
		return e
	}
	total, hasTotal, totalOK := field(value, "promptTokenCount")
	read, hasRead, readOK := field(value, "cachedContentTokenCount")
	e.TotalInputTokens, e.CacheReadTokens = total, read
	if !totalOK || !readOK {
		e.Status = Invalid
		return e
	}
	if !hasTotal || !hasRead {
		return e
	}
	if read > total {
		e.Status = Invalid
		return e
	}
	e.Status = Known
	return e
}
