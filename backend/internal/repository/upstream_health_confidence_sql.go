package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// encoding/json decodes interface-valued numbers as float64. The first
// positive integer that rounds to infinity is 2^1024 - 2^970 (the midpoint
// above MaxFloat64); equality overflows under round-to-nearest, ties-to-even.
// Keep the exact numeric boundary: PostgreSQL float8 casts also reject tiny
// underflows, whereas Go accepts those as zero. JSONPath numeric comparisons
// preserve that distinction while validating nested, otherwise unused evidence.
const confidenceEvidenceFloat64Overflow = "179769313486231580793728971405303415079934132710037826936173778980444968292764750946649017977587207096330286416692887910946555547851940402630657488671505820681908902000708383676273854845817711531764475730270069855571366959622842914819860834936475292719074168444365510704342711559699508093042880177904174497792"

// Counts remain exact over the complete seven-day window. Only the newest
// evidence and newest non-null score cross the database boundary as raw data.
// There is intentionally no upper time bound: the legacy reader includes future
// observations, and changing that rule would change its existing semantics.
const upstreamHealthConfidenceSummarySQL = `
WITH facts AS MATERIALIZED (
 SELECT upstream_key_id, observed_at, confidence_checks, confidence_evidence,
   CASE WHEN jsonb_typeof(confidence_evidence->'kind')='string'
     THEN confidence_evidence->>'kind' ELSE '' END AS kind
 FROM upstream_health_observations
 WHERE upstream_key_id=ANY($1::bigint[]) AND platform=$2 AND source='probe'
   AND confidence_prompt_version=$3 AND observed_at >= $4
), evidence_bounds AS MATERIALIZED (
 SELECT jsonb_build_object('overflow', ` + confidenceEvidenceFloat64Overflow + `::numeric,
   'negative_overflow', -` + confidenceEvidenceFloat64Overflow + `::numeric) AS limits
), check_shapes AS MATERIALIZED (
 -- JSONB equality treats 1 and 1.0 as equal, but Go's map[string]int decoder
 -- rejects the latter. Deduplicate the exact serialized form instead.
 SELECT DISTINCT ON (confidence_checks::text) confidence_checks
 FROM facts ORDER BY confidence_checks::text
), checks_validity AS (
 SELECT bool_and(
   (confidence_checks IS NULL OR jsonb_typeof(confidence_checks) IN ('object','null'))
   AND NOT EXISTS (
     SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(confidence_checks)='object' THEN confidence_checks ELSE '{}'::jsonb END) entry
     WHERE CASE
       WHEN entry.value='null'::jsonb THEN false
       WHEN jsonb_typeof(entry.value)='number' AND entry.value::text ~ '^-?[0-9]+$'
         THEN CASE WHEN length(entry.value::text) < 19 THEN false
           ELSE (entry.value::text)::numeric NOT BETWEEN -9223372036854775808 AND 9223372036854775807 END
       ELSE true END
   )) AS checks_valid
 FROM check_shapes
), classified AS (
 SELECT *, kind IN ('','juice') AS juice FROM facts
), counts AS (
 SELECT upstream_key_id,
   bool_and(CASE
     WHEN confidence_evidence IS NULL OR jsonb_typeof(confidence_evidence)='null' THEN true
     WHEN jsonb_typeof(confidence_evidence)<>'object' THEN false
     ELSE NOT jsonb_path_exists(confidence_evidence,
       'strict $.** ? (@.type() == "number" && (@ >= $overflow || @ <= $negative_overflow))',
       (SELECT limits FROM evidence_bounds), false) END) AS facts_valid,
   jsonb_build_array(COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'valid_completed')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'current_success')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'mixed')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'unsuccessful')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM(COALESCE((confidence_checks->>'network_error')::bigint,0)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM(COALESCE(NULLIF((confidence_checks->>'attempted')::bigint,0),1)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM((CASE WHEN kind='coverage' AND confidence_evidence->'hard_anomaly'='true'::jsonb THEN 1 ELSE 0 END)) FILTER (WHERE observed_at >= $5),0),
        COALESCE(SUM((CASE WHEN kind='output_integrity' AND confidence_evidence->>'classification'='output_rewrite_40_prefix' THEN 1 ELSE 0 END)) FILTER (WHERE observed_at >= $5),0)) AS counts24,
   jsonb_build_array(COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'valid_completed')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'current_success')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'mixed')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM((CASE WHEN juice THEN COALESCE((confidence_checks->>'unsuccessful')::bigint,0) ELSE 0 END)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM(COALESCE((confidence_checks->>'network_error')::bigint,0)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM(COALESCE(NULLIF((confidence_checks->>'attempted')::bigint,0),1)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM((CASE WHEN kind='coverage' AND confidence_evidence->'hard_anomaly'='true'::jsonb THEN 1 ELSE 0 END)) FILTER (WHERE observed_at >= $6),0),
        COALESCE(SUM((CASE WHEN kind='output_integrity' AND confidence_evidence->>'classification'='output_rewrite_40_prefix' THEN 1 ELSE 0 END)) FILTER (WHERE observed_at >= $6),0)) AS counts7
 FROM classified GROUP BY upstream_key_id
)
SELECT requested.key_id, counts.counts24, counts.counts7, COALESCE(counts.facts_valid,true) AND COALESCE((SELECT checks_valid FROM checks_validity),true),
 latest.observed_at, latest.confidence_checks, latest.confidence_evidence,
 latest.confidence_prompt_version, latest.requested_effort, latest.reasoning_tokens,
 score.confidence_score
FROM unnest($1::bigint[]) AS requested(key_id)
LEFT JOIN counts ON counts.upstream_key_id=requested.key_id
LEFT JOIN LATERAL (
 SELECT observed_at, confidence_checks, confidence_evidence,
   confidence_prompt_version, requested_effort, reasoning_tokens
 FROM upstream_health_observations
 WHERE upstream_key_id=requested.key_id AND platform=$2 AND source='probe'
   AND confidence_prompt_version=$3 AND observed_at >= $4
 ORDER BY observed_at DESC,id DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
 SELECT confidence_score FROM upstream_health_observations
 WHERE upstream_key_id=requested.key_id AND platform=$2 AND source='probe'
   AND confidence_prompt_version=$3 AND observed_at >= $4
   AND confidence_score IS NOT NULL
 ORDER BY observed_at DESC,id DESC LIMIT 1
) score ON true
ORDER BY requested.key_id`

// PostgreSQL stores microseconds. A Go age comparison against a nanosecond
// clock includes only stored timestamps at or after the ceiling boundary.
// The legacy evidence query intentionally retains its original SQL parameter.
func confidenceCountBoundary(value time.Time) time.Time {
	truncated := value.Truncate(time.Microsecond)
	if truncated.Before(value) {
		return truncated.Add(time.Microsecond)
	}
	return truncated
}

// SQL SUM(bigint) promotes to arbitrary precision, whereas the legacy Go int
// counters wrap. Preserve that arithmetic after aggregation without weakening
// the validation of each raw observation's integer representation and range.
func decodeConfidenceCounts(raw []byte) ([]int, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, nil
	}
	counts := make([]int, len(values))
	for i, value := range values {
		value = bytes.TrimSpace(value)
		if bytes.Equal(value, []byte("null")) {
			continue // Same null-to-zero behavior as json.Unmarshal into []int.
		}
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err == nil {
			counts[i] = int(parsed)
			continue
		}
		if !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		integer, ok := new(big.Int).SetString(string(value), 10)
		if !ok {
			return nil, fmt.Errorf("invalid confidence count integer")
		}
		// Reduce explicitly before conversion: big.Int's out-of-range Int64
		// result is not a contract. Uint64 is defined for this reduced value.
		modulus := new(big.Int).Lsh(big.NewInt(1), 64)
		integer.Mod(integer, modulus)
		counts[i] = int(int64(integer.Uint64()))
	}
	return counts, nil
}

func readUpstreamHealthConfidencesSQL(ctx context.Context, db *sql.DB, ids []int64, now time.Time) (map[int64]service.UpstreamHealthConfidenceSummary, error) {
	rows, err := db.QueryContext(ctx, upstreamHealthConfidenceSummarySQL, pq.Array(ids),
		service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour), confidenceCountBoundary(now.Add(-24*time.Hour)), confidenceCountBoundary(now.Add(-7*24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]service.UpstreamHealthConfidenceSummary, len(ids))
	for rows.Next() {
		var id int64
		var c24, c7, checks, evidence []byte
		var factsValid bool
		var observed sql.NullTime
		var version, effort sql.NullString
		var tokens, score sql.NullInt64
		if err := rows.Scan(&id, &c24, &c7, &factsValid, &observed, &checks, &evidence, &version, &effort, &tokens, &score); err != nil {
			return nil, err
		}
		if !factsValid {
			return nil, fmt.Errorf("invalid confidence observation JSON")
		}
		// Reuse the legacy evidence projection, including nil and empty-map behavior.
		var latest []*dbent.UpstreamHealthObservation
		if observed.Valid {
			row := &dbent.UpstreamHealthObservation{ObservedAt: observed.Time}
			if version.Valid {
				row.ConfidencePromptVersion = &version.String
			}
			if effort.Valid {
				row.RequestedEffort = &effort.String
			}
			if tokens.Valid {
				row.ReasoningTokens = &tokens.Int64
			}
			if len(checks) > 0 {
				if err := json.Unmarshal(checks, &row.ConfidenceChecks); err != nil {
					return nil, err
				}
			}
			if len(evidence) > 0 {
				if err := json.Unmarshal(evidence, &row.ConfidenceEvidence); err != nil {
					return nil, err
				}
			}
			latest = []*dbent.UpstreamHealthObservation{row}
		}
		summary := aggregateUpstreamHealthConfidence(now, latest)
		if score.Valid {
			value := int(score.Int64)
			summary.LastScore = &value
		}
		var a, b []int
		if len(c24) > 0 {
			a, err = decodeConfidenceCounts(c24)
			if err != nil {
				return nil, err
			}
		}
		if len(c7) > 0 {
			b, err = decodeConfidenceCounts(c7)
			if err != nil {
				return nil, err
			}
		}
		if (len(a) != 0 && len(a) != 8) || (len(b) != 0 && len(b) != 8) {
			return nil, fmt.Errorf("invalid confidence aggregate shape")
		}
		if len(a) == 0 {
			a = make([]int, 8)
		}
		if len(b) == 0 {
			b = make([]int, 8)
		}
		summary.SampleCount24h, summary.SampleCount7d = a[0], b[0]
		summary.ValidCompleted24h, summary.ValidCompleted7d = a[0], b[0]
		summary.CurrentSuccess24h, summary.CurrentSuccess7d = a[1], b[1]
		summary.Mixed24h, summary.Mixed7d = a[2], b[2]
		summary.Unsuccessful24h, summary.Unsuccessful7d = a[3], b[3]
		summary.NetworkError24h, summary.NetworkError7d = a[4], b[4]
		summary.Attempted24h, summary.Attempted7d = a[5], b[5]
		summary.CoverageHardAnomaly24h, summary.CoverageHardAnomaly7d = a[6], b[6]
		summary.OutputRewrite24h, summary.OutputRewrite7d = a[7], b[7]
		summary.Score24h, summary.Score7d = nil, nil
		if a[0] > 0 {
			value := float64(a[1]) / float64(a[0]) * 100
			summary.Score24h = &value
		}
		if b[0] > 0 {
			value := float64(b[1]) / float64(b[0]) * 100
			summary.Score7d = &value
		}
		switch {
		case b[0] == 0:
			summary.Status = "data_insufficient"
		case b[2] > 0:
			summary.Status = "mixed"
		case b[1] > 0:
			summary.Status = "current_success"
		default:
			summary.Status = "unsuccessful"
		}
		out[id] = summary
	}
	return out, rows.Err()
}
