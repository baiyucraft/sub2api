package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
)

var _ upstreamcacheguard.AdmissionTx = (*upstreamCacheGuardTx)(nil)
var _ upstreamcacheguard.AdmissionStore = (*upstreamCacheGuardRepository)(nil)

func marshalCacheGuardRuntime(kind string, value any) ([]byte, error) {
	if kind != upstreamcacheguard.SessionKind {
		return json.Marshal(value)
	}
	// Do not copy the admission ledger into the body, including for a caller
	// still holding a legacy in-memory session during the first refresh.
	switch state := value.(type) {
	case *upstreamcacheguard.SessionState:
		if state == nil {
			return nil, errors.New("cache guard nil session")
		}
		copy := *state
		copy.Admissions = nil
		return json.Marshal(copy)
	case upstreamcacheguard.SessionState:
		state.Admissions = nil
		return json.Marshal(state)
	default:
		return nil, errors.New("cache guard session state must be typed")
	}
}

func (t *upstreamCacheGuardTx) ReadAdmission(sessionKey, key string) (upstreamcacheguard.Admission, bool, error) {
	defer upstreamcacheguard.MeasureStage(t.ctx, "admission_read")()
	if err := t.lockState(upstreamcacheguard.SessionKind, sessionKey); err != nil {
		return upstreamcacheguard.Admission{}, false, err
	}
	var raw []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT admission_json FROM upstream_cache_guard_admissions WHERE group_id=$1 AND session_key=$2 AND admission_key=$3`, t.groupID, sessionKey, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return upstreamcacheguard.Admission{}, false, nil
	}
	if err != nil {
		return upstreamcacheguard.Admission{}, false, err
	}
	var value upstreamcacheguard.Admission
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, false, fmt.Errorf("cache guard admission decode: %w", err)
	}
	return value, true, nil
}

func (t *upstreamCacheGuardTx) WriteAdmission(sessionKey, key string, value upstreamcacheguard.Admission) error {
	defer upstreamcacheguard.MeasureStage(t.ctx, "admission_write")()
	if err := t.lockState(upstreamcacheguard.SessionKind, sessionKey); err != nil {
		return err
	}
	if value.SessionKey != sessionKey || value.GroupID != t.groupID || value.AccountID <= 0 || value.UpstreamKeyID < 0 || value.StartedAt.IsZero() || (t.accountID > 0 && value.AccountID != t.accountID) || (t.keyID > 0 && value.UpstreamKeyID > 0 && value.UpstreamKeyID != t.keyID) {
		return errors.New("cache guard admission identity mismatch")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// A completed row can never become pending again. All classification fields
	// remain the immutable values captured before the upstream request began.
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO upstream_cache_guard_admissions(group_id,session_key,admission_key,account_id,upstream_key_id,route_key,started_at,completed,admission_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(group_id,session_key,admission_key) DO UPDATE
		SET completed=upstream_cache_guard_admissions.completed OR EXCLUDED.completed,
		admission_json=jsonb_set(upstream_cache_guard_admissions.admission_json,'{completed}',to_jsonb(upstream_cache_guard_admissions.completed OR EXCLUDED.completed))
		WHERE EXCLUDED.completed AND NOT upstream_cache_guard_admissions.completed`, t.groupID, sessionKey, key, value.AccountID, value.UpstreamKeyID, value.RouteKey, value.StartedAt.UTC(), value.Completed, string(raw))
	return err
}

func (t *upstreamCacheGuardTx) PendingAdmissions(sessionKey string, accountID int64, routeKey string) ([]upstreamcacheguard.Admission, error) {
	defer upstreamcacheguard.MeasureStage(t.ctx, "admission_pending")()
	if err := t.lockState(upstreamcacheguard.SessionKind, sessionKey); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT admission_json FROM upstream_cache_guard_admissions
		WHERE group_id=$1 AND session_key=$2 AND account_id=$3 AND completed=FALSE AND (route_key=$4 OR route_key='')
		ORDER BY started_at,admission_key LIMIT $5`, t.groupID, sessionKey, accountID, routeKey, upstreamcacheguard.MaxFacts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.Admission
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var a upstreamcacheguard.Admission
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("cache guard pending admission decode: %w", err)
		}
		out = append(out, a)
		if len(out) > upstreamcacheguard.MaxFacts {
			return nil, errors.New("cache guard pending admission bound exceeded")
		}
	}
	return out, rows.Err()
}

func (t *upstreamCacheGuardTx) RetainedAdmissionCount(sessionKey string, since time.Time) (int, error) {
	defer upstreamcacheguard.MeasureStage(t.ctx, "admission_count")()
	if err := t.lockState(upstreamcacheguard.SessionKind, sessionKey); err != nil {
		return 0, err
	}
	var count int
	// The engine only distinguishes values below the 100,000 admission bound
	// from values at/above it. Stop the index walk there even for damaged or
	// imported sessions larger than the admission cap.
	err := t.tx.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM (
		SELECT 1 FROM upstream_cache_guard_admissions WHERE group_id=$1 AND session_key=$2 AND completed=FALSE
		UNION ALL
		SELECT 1 FROM upstream_cache_guard_admissions WHERE group_id=$1 AND session_key=$2 AND completed=TRUE AND started_at>=$3
		LIMIT 100000
	) retained`, t.groupID, sessionKey, since.UTC()).Scan(&count)
	return count, err
}

// AdmissionHealth distinguishes unfinished upstream attempts from completed
// evidence awaiting processing. Only the latter can withhold an account's new
// snapshot. A consumer lease never removes a fact from this completeness check.
func (r *upstreamCacheGuardRepository) AdmissionHealth(ctx context.Context, cutoff time.Time, failures []upstreamcacheguard.AdmissionFailure) (upstreamcacheguard.AdmissionHealth, error) {
	var health upstreamcacheguard.AdmissionHealth
	if r == nil || r.db == nil {
		return health, upstreamcacheguard.ErrUnavailable
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id,upstream_key_id,MIN(completed_at)
		FROM upstream_cache_guard_facts
		WHERE processed=FALSE AND completed_at<$1
		GROUP BY account_id,upstream_key_id
		ORDER BY account_id,upstream_key_id LIMIT $2`, cutoff.UTC(), upstreamcacheguard.MaxRuntimeRows+1)
	if err != nil {
		return health, err
	}
	for rows.Next() {
		var fact upstreamcacheguard.PendingFact
		if err = rows.Scan(&fact.AccountID, &fact.UpstreamKeyID, &fact.CompletedAt); err != nil {
			rows.Close()
			return health, fmt.Errorf("cache guard pending fact health decode: %w", err)
		}
		health.PendingFacts = append(health.PendingFacts, fact)
		if len(health.PendingFacts) > upstreamcacheguard.MaxRuntimeRows {
			rows.Close()
			return health, errors.New("cache guard pending fact health bound exceeded")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(failures) == 0 {
		return health, err
	}
	accounts, keys, admissions := make([]int64, len(failures)), make([]int64, len(failures)), make([]string, len(failures))
	for i, f := range failures {
		accounts[i], keys[i], admissions[i] = f.AccountID, f.UpstreamKeyID, f.Key
	}
	rows, err = r.db.QueryContext(ctx, `SELECT DISTINCT request.account_id,request.key_id,request.admission_key
		FROM unnest($1::BIGINT[],$2::BIGINT[],$3::TEXT[]) request(account_id,key_id,admission_key)
		WHERE EXISTS (SELECT 1 FROM upstream_cache_guard_admissions a
		WHERE a.account_id=request.account_id AND a.upstream_key_id=request.key_id AND a.admission_key=request.admission_key AND a.completed=TRUE)`, pq.Array(accounts), pq.Array(keys), pq.Array(admissions))
	if err != nil {
		return health, err
	}
	defer rows.Close()
	for rows.Next() {
		var f upstreamcacheguard.AdmissionFailure
		if err := rows.Scan(&f.AccountID, &f.UpstreamKeyID, &f.Key); err != nil {
			return health, err
		}
		health.Acknowledged = append(health.Acknowledged, f)
	}
	return health, rows.Err()
}
