package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
)

type upstreamCacheGuardRepository struct{ db *sql.DB }

var _ upstreamcacheguard.Store = (*upstreamCacheGuardRepository)(nil)
var _ upstreamcacheguard.AccountStore = (*upstreamCacheGuardRepository)(nil)

func NewUpstreamCacheGuardRepository(db *sql.DB) upstreamcacheguard.Store {
	return &upstreamCacheGuardRepository{db: db}
}

type upstreamCacheGuardTx struct {
	tx            *sql.Tx
	ctx           context.Context
	groupID       int64
	accountID     int64
	keyID         int64
	quotaLocked   bool
	sessionLocked string
}

var _ upstreamcacheguard.AccountTx = (*upstreamCacheGuardTx)(nil)

func (r *upstreamCacheGuardRepository) TransactAccount(ctx context.Context, groupID, accountID, keyID int64, fn func(upstreamcacheguard.Tx) error) error {
	if r == nil || r.db == nil {
		return upstreamcacheguard.ErrUnavailable
	}
	if groupID < 0 || accountID <= 0 || keyID <= 0 {
		return errors.New("invalid cache guard account identity")
	}
	beginDone := upstreamcacheguard.MeasureStage(ctx, "db_begin")
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	beginDone()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Account lock precedes the group lock in every account transaction.
	lockDone := upstreamcacheguard.MeasureStage(ctx, "account_lock")
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(298, ($1 % 2147483647)::integer)`, accountID)
	lockDone()
	if err != nil {
		return err
	}
	if err = fn(&upstreamCacheGuardTx{tx: tx, ctx: ctx, groupID: groupID, accountID: accountID, keyID: keyID}); err != nil {
		return err
	}
	commitDone := upstreamcacheguard.MeasureStage(ctx, "transaction_commit")
	err = tx.Commit()
	commitDone()
	return err
}

func (t *upstreamCacheGuardTx) ReadAccount(accountID, keyID int64, out any) (int64, error) {
	if accountID != t.accountID || keyID != t.keyID {
		return 0, errors.New("cache guard account identity changed in transaction")
	}
	var raw []byte
	var version int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT state_json,version FROM upstream_cache_guard_accounts WHERE account_id=$1 AND upstream_key_id=$2`, accountID, keyID).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return 0, fmt.Errorf("cache guard account state decode: %w", err)
	}
	return version, nil
}

func (t *upstreamCacheGuardTx) WriteAccount(accountID, keyID int64, value any) (int64, error) {
	if accountID != t.accountID || keyID != t.keyID {
		return 0, errors.New("cache guard account identity changed in transaction")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	var version int64
	err = t.tx.QueryRowContext(t.ctx, `INSERT INTO upstream_cache_guard_accounts(account_id,upstream_key_id,version,state_json,updated_at) VALUES($1,$2,1,$3,CURRENT_TIMESTAMP) ON CONFLICT(account_id,upstream_key_id) DO UPDATE SET version=upstream_cache_guard_accounts.version + CASE WHEN upstream_cache_guard_accounts.state_json IS DISTINCT FROM EXCLUDED.state_json THEN 1 ELSE 0 END,state_json=EXCLUDED.state_json,updated_at=CASE WHEN upstream_cache_guard_accounts.state_json IS DISTINCT FROM EXCLUDED.state_json THEN EXCLUDED.updated_at ELSE upstream_cache_guard_accounts.updated_at END RETURNING version`, accountID, keyID, string(raw)).Scan(&version)
	return version, err
}

func (t *upstreamCacheGuardTx) AccountFacts(accountID, keyID int64, since time.Time) ([]upstreamcacheguard.Evidence, error) {
	if accountID != t.accountID || keyID != t.keyID {
		return nil, errors.New("cache guard account identity changed in transaction")
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT evidence_json FROM upstream_cache_guard_facts WHERE account_id=$1 AND upstream_key_id=$2 AND processed=TRUE AND completed_at>=$3 ORDER BY completed_at,group_id,evidence_id LIMIT $4`, accountID, keyID, since.UTC(), upstreamcacheguard.MaxFacts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []upstreamcacheguard.Evidence
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e upstreamcacheguard.Evidence
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("cache guard account fact decode: %w", err)
		}
		facts = append(facts, e)
		if len(facts) > upstreamcacheguard.MaxFacts {
			return nil, errors.New("cache guard account fact bound exceeded")
		}
	}
	return facts, rows.Err()
}

func (t *upstreamCacheGuardTx) LegacyAccounts(accountID, keyID int64) ([]upstreamcacheguard.LegacyAccountState, error) {
	if accountID != t.accountID || keyID != t.keyID {
		return nil, errors.New("cache guard account identity changed in transaction")
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT legacy.group_id,legacy.state_json FROM upstream_cache_guard_runtime AS legacy
		JOIN accounts AS account ON account.id=$1 AND account.upstream_key_id=$2 AND account.deleted_at IS NULL
		WHERE legacy.state_kind='account' AND legacy.state_json->>'account_id'=$1::text AND legacy.state_json->>'upstream_key_id'=$2::text
		ORDER BY legacy.group_id,legacy.state_key LIMIT $3`, accountID, keyID, upstreamcacheguard.MaxRuntimeRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.LegacyAccountState
	for rows.Next() {
		var raw []byte
		var v upstreamcacheguard.LegacyAccountState
		if err = rows.Scan(&v.GroupID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v.AccountState); err != nil {
			return nil, fmt.Errorf("cache guard legacy state decode: %w", err)
		}
		out = append(out, v)
		if len(out) > upstreamcacheguard.MaxRuntimeRows {
			return nil, errors.New("cache guard legacy account bound exceeded")
		}
	}
	return out, rows.Err()
}

func (r *upstreamCacheGuardRepository) AccountRows(ctx context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	return r.accountRows(ctx, nil)
}
func (r *upstreamCacheGuardRepository) AccountRowsFor(ctx context.Context, ids []int64) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return r.accountRows(ctx, ids)
}
func (r *upstreamCacheGuardRepository) accountRows(ctx context.Context, ids []int64) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	// Include legacy-only current-Key accounts so a refresh cannot discard an
	// existing protection before its first account-scoped fact arrives.
	rows, err := r.db.QueryContext(ctx, `SELECT state.account_id,state.upstream_key_id,state.version,state.state_json,state.updated_at
		FROM upstream_cache_guard_accounts AS state
		JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id
		WHERE account.deleted_at IS NULL AND ($2::bigint[] IS NULL OR state.account_id=ANY($2))
		UNION ALL
		SELECT DISTINCT a.id,a.upstream_key_id,0::bigint,NULL::jsonb,CURRENT_TIMESTAMP
		FROM upstream_cache_guard_runtime AS legacy
		JOIN accounts AS a ON a.id::text=legacy.state_json->>'account_id' AND a.upstream_key_id::text=legacy.state_json->>'upstream_key_id'
		WHERE legacy.state_kind='account' AND a.upstream_key_id>0 AND a.deleted_at IS NULL AND ($2::bigint[] IS NULL OR a.id=ANY($2))
		AND NOT EXISTS(SELECT 1 FROM upstream_cache_guard_accounts AS state WHERE state.account_id=a.id AND state.upstream_key_id=a.upstream_key_id)
		ORDER BY account_id,upstream_key_id LIMIT $1`, upstreamcacheguard.MaxRuntimeRows+1, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.AccountRuntimeRow
	for rows.Next() {
		var v upstreamcacheguard.AccountRuntimeRow
		if err = rows.Scan(&v.AccountID, &v.UpstreamKeyID, &v.Version, &v.Payload, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > upstreamcacheguard.MaxRuntimeRows {
			return nil, errors.New("cache guard account runtime bound exceeded")
		}
	}
	return out, rows.Err()
}

func (r *upstreamCacheGuardRepository) AccountWindows(ctx context.Context, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	return r.accountWindows(ctx, nil, since, until)
}
func (r *upstreamCacheGuardRepository) AccountWindowsFor(ctx context.Context, ids []int64, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	if len(ids) == 0 {
		return map[string]upstreamcacheguard.AccountWindow{}, nil
	}
	return r.accountWindows(ctx, ids, since, until)
}
func (r *upstreamCacheGuardRepository) accountWindows(ctx context.Context, ids []int64, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	// The persisted valid_sample bit is authoritative; the JSON number fields
	// are the raw upstream usage before billing or model normalization.
	rows, err := r.db.QueryContext(ctx, `WITH eligible AS (
		SELECT state.account_id,state.upstream_key_id,
			fact.group_id,fact.completed_at,fact.evidence_json->'admission' AS admission,
			fact.evidence_json->'request' AS request,
			COALESCE(fact.evidence_json->>'protection_paused','false') = 'true' AS protection_paused,
			(fact.evidence_json->'raw_usage'->>'TotalInputTokens')::BIGINT AS input_tokens,
			(fact.evidence_json->'raw_usage'->>'CacheReadTokens')::BIGINT AS cache_read_tokens,
			fact.evidence_json->'admission'->>'session_key' AS session_key,
			(COALESCE(fact.evidence_json->>'protection_paused','false') <> 'true' AND fact.evidence_json->'admission'->>'trial' = 'true'
				AND NULLIF(state.state_json->>'cohort_id','') IS NOT NULL
				AND fact.evidence_json->'admission'->>'cohort_id' = state.state_json->>'cohort_id') AS trial
		FROM upstream_cache_guard_accounts AS state
		JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id AND account.deleted_at IS NULL
		JOIN upstream_cache_guard_facts AS fact ON fact.account_id=state.account_id AND fact.upstream_key_id=state.upstream_key_id
		WHERE ($4::bigint[] IS NULL OR state.account_id=ANY($4)) AND fact.processed=TRUE AND fact.valid_sample=TRUE AND fact.completed_at >= $1 AND fact.completed_at <= $2
			AND fact.evidence_json->'raw_usage'->>'Status' = 'known'
			AND jsonb_typeof(fact.evidence_json->'raw_usage'->'TotalInputTokens') = 'number'
			AND jsonb_typeof(fact.evidence_json->'raw_usage'->'CacheReadTokens') = 'number'
			AND (state.state_json ? 'policies' OR fact.completed_at > COALESCE(NULLIF(state.state_json->>'quality_since','')::TIMESTAMPTZ, '-infinity'::TIMESTAMPTZ))
			AND fact.completed_at > COALESCE(NULLIF(state.state_json->'legacy_quality_since'->>(fact.group_id::TEXT),'')::TIMESTAMPTZ, '-infinity'::TIMESTAMPTZ)
	)
	SELECT state.account_id,state.upstream_key_id,state.version,state.state_json,
		COUNT(fact.group_id),COALESCE(SUM(fact.input_tokens),0)::BIGINT,COALESCE(SUM(fact.cache_read_tokens),0)::BIGINT,
		COUNT(fact.group_id) FILTER (WHERE fact.trial),
		COALESCE(SUM(fact.input_tokens) FILTER (WHERE fact.trial),0)::BIGINT,
		COALESCE(SUM(fact.cache_read_tokens) FILTER (WHERE fact.trial),0)::BIGINT,
		COUNT(DISTINCT (fact.group_id,fact.session_key)) FILTER (WHERE fact.trial),
		MAX(fact.completed_at),
        MIN(fact.completed_at),
        (SELECT MIN(future.completed_at) FROM upstream_cache_guard_facts future WHERE future.account_id=state.account_id AND future.upstream_key_id=state.upstream_key_id AND future.processed=TRUE AND future.valid_sample=TRUE AND future.completed_at>$2),
		ARRAY_AGG(DISTINCT fact.group_id) FILTER (WHERE fact.group_id IS NOT NULL),
		ARRAY_AGG(DISTINCT fact.group_id) FILTER (WHERE fact.trial),
		COALESCE((SELECT jsonb_object_agg(policy.key, projection_window.metrics)
			FROM jsonb_each(COALESCE(state.state_json->'policies','{}'::JSONB)) AS policy
			CROSS JOIN LATERAL (SELECT jsonb_build_object(
				'ValidRequests',COUNT(f.group_id),
				'TotalInputTokens',COALESCE(SUM(f.input_tokens),0)::BIGINT,
				'CacheReadTokens',COALESCE(SUM(f.cache_read_tokens),0)::BIGINT,
				'TrialValidRequests',COUNT(f.group_id) FILTER (WHERE f.cohort_trial),
				'TrialInputTokens',COALESCE(SUM(f.input_tokens) FILTER (WHERE f.cohort_trial),0)::BIGINT,
				'TrialCacheReadTokens',COALESCE(SUM(f.cache_read_tokens) FILTER (WHERE f.cohort_trial),0)::BIGINT,
				'TrialSessions',COUNT(DISTINCT (f.group_id,f.session_key)) FILTER (WHERE f.cohort_trial),
				'OrdinaryValidRequests',COUNT(f.group_id) FILTER (WHERE f.ordinary_recovery),
				'OrdinarySessions',COUNT(DISTINCT f.admission->>'ordinary_recovery_session_id') FILTER (WHERE f.ordinary_recovery),
				'OrdinaryInputTokens',COALESCE(SUM(f.input_tokens) FILTER (WHERE f.ordinary_recovery),0)::BIGINT,
				'OrdinaryCacheReadTokens',COALESCE(SUM(f.cache_read_tokens) FILTER (WHERE f.ordinary_recovery),0)::BIGINT,
				'LastEvidenceAt',to_char(MAX(f.completed_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
				'GroupIDs',COALESCE(ARRAY_AGG(DISTINCT f.group_id),'{}'::BIGINT[]),
				'TrialGroupIDs',COALESCE(ARRAY_AGG(DISTINCT f.group_id) FILTER (WHERE f.cohort_trial),'{}'::BIGINT[])) AS metrics
				FROM (SELECT normal.*,
					(NOT normal.protection_paused AND normal.admission->>'trial' = 'true'
					 AND NULLIF(policy.value->'state'->>'cohort_id','') IS NOT NULL
					 AND normal.admission->>'cohort_id'=policy.value->'state'->>'cohort_id'
					 AND (COALESCE(normal.admission->>'policy_signature','')=''
					      OR normal.admission->>'policy_signature'=policy.key)) AS cohort_trial,
					(NOT normal.protection_paused
					 AND COALESCE(normal.admission->>'trial','false') <> 'true'
					 AND COALESCE(normal.admission->>'capture_only','false') <> 'true'
					 AND normal.admission->>'ordinary_recovery_session_id' ~ '^[0-9a-f]{64}$'
					 AND normal.admission->>'ordinary_recovery_session_id'=normal.request->>'ordinary_recovery_session_id'
					 AND normal.admission->>'ordinary_recovery_identity_generation' ~ '^[0-9a-f]{64}$'
					 AND normal.admission->>'ordinary_recovery_identity_generation'=normal.request->>'ordinary_recovery_identity_generation'
					 AND normal.admission->>'ordinary_recovery_identity_generation'=policy.value->'state'->'ordinary_recovery'->>'identity_generation'
					 AND NULLIF(normal.request->>'started_at','')::TIMESTAMPTZ >= NULLIF(policy.value->'state'->'ordinary_recovery'->>'barrier_at','')::TIMESTAMPTZ
					 AND normal.completed_at >= NULLIF(policy.value->'state'->'ordinary_recovery'->>'barrier_at','')::TIMESTAMPTZ) AS ordinary_recovery
					FROM eligible AS normal
					WHERE normal.account_id=state.account_id AND normal.upstream_key_id=state.upstream_key_id
					AND normal.completed_at>COALESCE(NULLIF(policy.value->'state'->>'quality_since','')::TIMESTAMPTZ,'-infinity'::TIMESTAMPTZ)) AS f) AS projection_window),'{}'::JSONB)
	FROM upstream_cache_guard_accounts AS state
	JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id AND account.deleted_at IS NULL
	LEFT JOIN eligible AS fact ON fact.account_id=state.account_id AND fact.upstream_key_id=state.upstream_key_id
	WHERE ($4::bigint[] IS NULL OR state.account_id=ANY($4))
	GROUP BY state.account_id,state.upstream_key_id,state.version,state.state_json
	ORDER BY state.account_id,state.upstream_key_id LIMIT $3`, since.UTC(), until.UTC(), upstreamcacheguard.MaxRuntimeRows+1, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]upstreamcacheguard.AccountWindow)
	for rows.Next() {
		var w upstreamcacheguard.AccountWindow
		var stateJSON []byte
		var policyJSON []byte
		var latest, oldest, future sql.NullTime
		var groups, trialGroups pq.Int64Array
		if err = rows.Scan(&w.AccountID, &w.UpstreamKeyID, &w.Version, &stateJSON, &w.ValidRequests,
			&w.TotalInputTokens, &w.CacheReadTokens, &w.TrialValidRequests,
			&w.TrialInputTokens, &w.TrialCacheReadTokens, &w.TrialSessions,
			&latest, &oldest, &future, &groups, &trialGroups, &policyJSON); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(stateJSON, &w.State); err != nil {
			w.Err = fmt.Errorf("cache guard account window state decode: %w", err)
		}
		if len(policyJSON) > 0 {
			if err = json.Unmarshal(policyJSON, &w.Policies); err != nil {
				w.Err = errors.Join(w.Err, fmt.Errorf("cache guard policy window decode: %w", err))
			}
		}
		if w.State.AccountID != w.AccountID || w.State.UpstreamKeyID != w.UpstreamKeyID {
			w.Err = errors.Join(w.Err, errors.New("cache guard account window identity mismatch"))
		}
		if w.ValidRequests > upstreamcacheguard.MaxFacts {
			w.Err = errors.Join(w.Err, errors.New("cache guard account window fact bound exceeded"))
		}
		if latest.Valid {
			w.LastEvidenceAt = latest.Time
		}
		if oldest.Valid {
			w.NextBoundary = oldest.Time.Add(until.Sub(since)).Add(time.Nanosecond)
		}
		if future.Valid && (w.NextBoundary.IsZero() || future.Time.Before(w.NextBoundary)) {
			w.NextBoundary = future.Time
		}
		w.GroupIDs, w.TrialGroupIDs = []int64(groups), []int64(trialGroups)
		out[fmt.Sprintf("%d:%d", w.AccountID, w.UpstreamKeyID)] = w
		if len(out) > upstreamcacheguard.MaxRuntimeRows {
			return nil, errors.New("cache guard account window bound exceeded")
		}
	}
	return out, rows.Err()
}

func (r *upstreamCacheGuardRepository) Transact(ctx context.Context, groupID int64, fn func(upstreamcacheguard.Tx) error) error {
	if r == nil || r.db == nil {
		return upstreamcacheguard.ErrUnavailable
	}
	if groupID <= 0 {
		return errors.New("invalid cache guard group")
	}
	beginDone := upstreamcacheguard.MeasureStage(ctx, "db_begin")
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	beginDone()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The namespace is private to this guard. Collisions for unusually large
	// group IDs only serialize unrelated groups; they cannot weaken atomicity.
	if err = fn(&upstreamCacheGuardTx{tx: tx, ctx: ctx, groupID: groupID}); err != nil {
		return err
	}
	commitDone := upstreamcacheguard.MeasureStage(ctx, "transaction_commit")
	err = tx.Commit()
	commitDone()
	return err
}
func (t *upstreamCacheGuardTx) Read(kind, key string, out any) (int64, error) {
	if err := t.lockState(kind, key); err != nil {
		return 0, err
	}
	var raw []byte
	var version int64
	readStage, decodeStage := "runtime_read_sql", "runtime_decode"
	if kind == upstreamcacheguard.SessionKind {
		readStage, decodeStage = "session_read_sql", "session_decode"
	}
	readDone := upstreamcacheguard.MeasureStage(t.ctx, readStage)
	err := t.tx.QueryRowContext(t.ctx, `SELECT state_json, version FROM upstream_cache_guard_runtime WHERE group_id=$1 AND state_kind=$2 AND state_key=$3`, t.groupID, kind, key).Scan(&raw, &version)
	readDone()
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	decodeDone := upstreamcacheguard.MeasureStage(t.ctx, decodeStage)
	err = json.Unmarshal(raw, out)
	decodeDone()
	if err != nil {
		return 0, fmt.Errorf("cache guard typed state decode: %w", err)
	}
	return version, nil
}
func (t *upstreamCacheGuardTx) Write(kind, key string, value any) (int64, error) {
	if err := t.lockState(kind, key); err != nil {
		return 0, err
	}
	if kind != upstreamcacheguard.GroupKind && kind != upstreamcacheguard.AccountKind && kind != upstreamcacheguard.SessionKind {
		return 0, errors.New("invalid cache guard state kind")
	}
	encodeStage, writeStage := "runtime_encode", "runtime_write_sql"
	if kind == upstreamcacheguard.SessionKind {
		encodeStage, writeStage = "session_encode", "session_write_sql"
	}
	encodeDone := upstreamcacheguard.MeasureStage(t.ctx, encodeStage)
	raw, err := marshalCacheGuardRuntime(kind, value)
	encodeDone()
	if err != nil {
		return 0, err
	}
	var version int64
	writeDone := upstreamcacheguard.MeasureStage(t.ctx, writeStage)
	err = t.tx.QueryRowContext(t.ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at) VALUES($1,$2,$3,1,$4,CURRENT_TIMESTAMP) ON CONFLICT(group_id,state_kind,state_key) DO UPDATE SET version=upstream_cache_guard_runtime.version+1,state_json=EXCLUDED.state_json,updated_at=EXCLUDED.updated_at RETURNING version`, t.groupID, kind, key, string(raw)).Scan(&version)
	writeDone()
	return version, err
}
func (t *upstreamCacheGuardTx) InsertFact(e upstreamcacheguard.Evidence) (bool, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	// A pending event is acknowledged in the same transaction as its state.
	// An already processed event remains immutable and is never counted twice.
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,upstream_key_id,completed_at,valid_sample,evidence_json,processed) VALUES($1,$2,$3,$4,$5,$6,$7,TRUE) ON CONFLICT(group_id,evidence_id) DO UPDATE SET processed=TRUE,claim_owner=NULL,claim_until=NULL WHERE upstream_cache_guard_facts.processed=FALSE`, t.groupID, e.ID, e.Request.AccountID, e.Request.UpstreamKeyID, e.CompletedAt, e.ValidSample(), string(raw))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (t *upstreamCacheGuardTx) Facts(accountID int64, since time.Time) ([]upstreamcacheguard.Evidence, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT evidence_json FROM upstream_cache_guard_facts WHERE group_id=$1 AND account_id=$2 AND processed=TRUE AND completed_at>=$3 ORDER BY completed_at,evidence_id LIMIT $4`, t.groupID, accountID, since.UTC(), upstreamcacheguard.MaxFacts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []upstreamcacheguard.Evidence
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e upstreamcacheguard.Evidence
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("cache guard fact decode: %w", err)
		}
		facts = append(facts, e)
		if len(facts) > upstreamcacheguard.MaxFacts {
			return nil, errors.New("cache guard fact bound exceeded")
		}
	}
	return facts, rows.Err()
}
func (r *upstreamCacheGuardRepository) Rows(ctx context.Context, groupIDs []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	query := `SELECT group_id,state_kind,state_key,version,state_json,updated_at FROM upstream_cache_guard_runtime`
	args := []any{}
	if len(groupIDs) > 0 {
		holders := make([]string, 0, len(groupIDs))
		for _, id := range groupIDs {
			args = append(args, id)
			holders = append(holders, fmt.Sprintf("$%d", len(args)))
		}
		query += ` WHERE group_id IN (` + strings.Join(holders, ",") + `)`
	}
	args = append(args, upstreamcacheguard.MaxRuntimeRows+1)
	query += fmt.Sprintf(` ORDER BY group_id,state_kind,state_key LIMIT $%d`, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.RuntimeRow
	for rows.Next() {
		var v upstreamcacheguard.RuntimeRow
		if err = rows.Scan(&v.GroupID, &v.Kind, &v.Key, &v.Version, &v.Payload, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > upstreamcacheguard.MaxRuntimeRows {
			return nil, errors.New("cache guard runtime bound exceeded")
		}
	}
	return out, rows.Err()
}
func (r *upstreamCacheGuardRepository) Cleanup(ctx context.Context, before time.Time) error {
	_, err := r.CleanupBatch(ctx, before, 1000)
	return err
}

func (r *upstreamCacheGuardRepository) CleanupBatch(ctx context.Context, before time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, upstreamcacheguard.ErrUnavailable
	}
	// Only processed facts and completed admissions expire. Typed session
	// identity, route information and unfinished classifications never expire.
	if limit < 1 || limit > 1000 {
		return 0, errors.New("invalid cache guard cleanup batch")
	}
	var deleted int64
	err := r.db.QueryRowContext(ctx, `WITH expired_admissions AS (
		SELECT group_id,session_key,admission_key FROM upstream_cache_guard_admissions
		WHERE completed=TRUE AND started_at<$1 ORDER BY started_at,group_id,session_key,admission_key LIMIT $2 FOR UPDATE SKIP LOCKED
	), deleted_admissions AS (
		DELETE FROM upstream_cache_guard_admissions a USING expired_admissions old
		WHERE a.group_id=old.group_id AND a.session_key=old.session_key AND a.admission_key=old.admission_key RETURNING 1
	), expired_facts AS (
		SELECT group_id,evidence_id FROM upstream_cache_guard_facts WHERE processed=TRUE AND completed_at<$1
		ORDER BY completed_at,group_id,evidence_id LIMIT (SELECT $2-COUNT(*) FROM deleted_admissions) FOR UPDATE SKIP LOCKED
	), deleted_facts AS (
		DELETE FROM upstream_cache_guard_facts f USING expired_facts old
		WHERE f.group_id=old.group_id AND f.evidence_id=old.evidence_id RETURNING 1
	) SELECT (SELECT COUNT(*) FROM deleted_admissions)+(SELECT COUNT(*) FROM deleted_facts)`, before.UTC(), limit).Scan(&deleted)
	return deleted, err
}

func (t *upstreamCacheGuardTx) lockState(kind, key string) error {
	if kind == upstreamcacheguard.SessionKind && t.sessionLocked != key {
		if t.quotaLocked {
			return errors.New("cache guard session lock after quota")
		}
		if t.sessionLocked != "" {
			return errors.New("cache guard multiple session locks")
		}
		lockDone := upstreamcacheguard.MeasureStage(t.ctx, "session_lock")
		_, err := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(299,hashtext($1::text || ':' || $2::text))`, t.groupID, key)
		lockDone()
		if err != nil {
			return err
		}
		t.sessionLocked = key
	}
	if kind == upstreamcacheguard.GroupKind && !t.quotaLocked {
		_, err := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(296, ($1 % 2147483647)::integer)`, t.groupID)
		if err != nil {
			return err
		}
		t.quotaLocked = true
	}
	return nil
}

func (r *upstreamCacheGuardRepository) Enqueue(ctx context.Context, e upstreamcacheguard.Evidence) error {
	if r == nil || r.db == nil {
		return upstreamcacheguard.ErrUnavailable
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,upstream_key_id,completed_at,valid_sample,evidence_json,processed) VALUES($1,$2,$3,$4,$5,$6,$7,FALSE) ON CONFLICT(group_id,evidence_id) DO NOTHING`, e.Request.GroupID, e.ID, e.Request.AccountID, e.Request.UpstreamKeyID, e.CompletedAt, e.ValidSample(), string(raw))
	return err
}

func (r *upstreamCacheGuardRepository) ClaimPending(ctx context.Context, owner string, limit int, lease time.Duration) ([]upstreamcacheguard.Evidence, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	if owner == "" || len(owner) > 64 || limit < 1 || limit > 256 || lease <= 0 {
		return nil, errors.New("invalid cache guard outbox claim")
	}
	rows, err := r.db.QueryContext(ctx, `WITH pending AS (SELECT group_id,evidence_id FROM upstream_cache_guard_facts WHERE processed=FALSE AND (claim_until IS NULL OR claim_until<CURRENT_TIMESTAMP) ORDER BY completed_at,group_id,evidence_id FOR UPDATE SKIP LOCKED LIMIT $1) UPDATE upstream_cache_guard_facts AS f SET claim_owner=$2,claim_until=CURRENT_TIMESTAMP+($3 * INTERVAL '1 millisecond') FROM pending WHERE f.group_id=pending.group_id AND f.evidence_id=pending.evidence_id RETURNING f.evidence_json`, limit, owner, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.Evidence
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e upstreamcacheguard.Evidence
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *upstreamCacheGuardRepository) HasPending(ctx context.Context) (bool, error) {
	if r == nil || r.db == nil {
		return false, upstreamcacheguard.ErrUnavailable
	}
	var pending bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_cache_guard_facts WHERE processed=FALSE)`).Scan(&pending)
	return pending, err
}

func (r *upstreamCacheGuardRepository) HasPendingBefore(ctx context.Context, cutoff time.Time) (bool, error) {
	if r == nil || r.db == nil {
		return false, upstreamcacheguard.ErrUnavailable
	}
	var pending bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_cache_guard_facts WHERE processed=FALSE AND completed_at<$1)`, cutoff.UTC()).Scan(&pending)
	return pending, err
}
