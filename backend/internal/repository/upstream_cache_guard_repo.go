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
	tx        *sql.Tx
	ctx       context.Context
	groupID   int64
	accountID int64
	keyID     int64
}

var _ upstreamcacheguard.AccountTx = (*upstreamCacheGuardTx)(nil)

func (r *upstreamCacheGuardRepository) TransactAccount(ctx context.Context, groupID, accountID, keyID int64, fn func(upstreamcacheguard.Tx) error) error {
	if r == nil || r.db == nil {
		return upstreamcacheguard.ErrUnavailable
	}
	if groupID < 0 || accountID <= 0 || keyID <= 0 {
		return errors.New("invalid cache guard account identity")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Account lock precedes the group lock in every account transaction.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(298, ($1 % 2147483647)::integer)`, accountID); err != nil {
		return err
	}
	if groupID > 0 {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(296, ($1 % 2147483647)::integer)`, groupID); err != nil {
			return err
		}
	}
	if err = fn(&upstreamCacheGuardTx{tx: tx, ctx: ctx, groupID: groupID, accountID: accountID, keyID: keyID}); err != nil {
		return err
	}
	return tx.Commit()
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
	err = t.tx.QueryRowContext(t.ctx, `INSERT INTO upstream_cache_guard_accounts(account_id,upstream_key_id,version,state_json,updated_at) VALUES($1,$2,1,$3,CURRENT_TIMESTAMP) ON CONFLICT(account_id,upstream_key_id) DO UPDATE SET version=upstream_cache_guard_accounts.version+1,state_json=EXCLUDED.state_json,updated_at=EXCLUDED.updated_at RETURNING version`, accountID, keyID, string(raw)).Scan(&version)
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
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	// Include legacy-only current-Key accounts so a refresh cannot discard an
	// existing protection before its first account-scoped fact arrives.
	rows, err := r.db.QueryContext(ctx, `SELECT state.account_id,state.upstream_key_id,state.version,state.state_json,state.updated_at
		FROM upstream_cache_guard_accounts AS state
		JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id
		WHERE account.deleted_at IS NULL
		UNION ALL
		SELECT DISTINCT a.id,a.upstream_key_id,0::bigint,NULL::jsonb,CURRENT_TIMESTAMP
		FROM upstream_cache_guard_runtime AS legacy
		JOIN accounts AS a ON a.id::text=legacy.state_json->>'account_id' AND a.upstream_key_id::text=legacy.state_json->>'upstream_key_id'
		WHERE legacy.state_kind='account' AND a.upstream_key_id>0 AND a.deleted_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM upstream_cache_guard_accounts AS state WHERE state.account_id=a.id AND state.upstream_key_id=a.upstream_key_id)
		ORDER BY account_id,upstream_key_id LIMIT $1`, upstreamcacheguard.MaxRuntimeRows+1)
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
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	// The persisted valid_sample bit is authoritative; the JSON number fields
	// are the raw upstream usage before billing or model normalization.
	rows, err := r.db.QueryContext(ctx, `WITH eligible AS (
		SELECT state.account_id,state.upstream_key_id,
			fact.group_id,fact.completed_at,fact.evidence_json->'admission' AS admission,
			(fact.evidence_json->'raw_usage'->>'TotalInputTokens')::BIGINT AS input_tokens,
			(fact.evidence_json->'raw_usage'->>'CacheReadTokens')::BIGINT AS cache_read_tokens,
			fact.evidence_json->'admission'->>'session_key' AS session_key,
			(fact.evidence_json->'admission'->>'trial' = 'true'
				AND NULLIF(state.state_json->>'cohort_id','') IS NOT NULL
				AND fact.evidence_json->'admission'->>'cohort_id' = state.state_json->>'cohort_id') AS trial
		FROM upstream_cache_guard_accounts AS state
		JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id AND account.deleted_at IS NULL
		JOIN upstream_cache_guard_facts AS fact ON fact.account_id=state.account_id AND fact.upstream_key_id=state.upstream_key_id
		WHERE fact.processed=TRUE AND fact.valid_sample=TRUE AND fact.completed_at >= $1 AND fact.completed_at <= $2
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
				'LastEvidenceAt',to_char(MAX(f.completed_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
				'GroupIDs',COALESCE(ARRAY_AGG(DISTINCT f.group_id),'{}'::BIGINT[]),
				'TrialGroupIDs',COALESCE(ARRAY_AGG(DISTINCT f.group_id) FILTER (WHERE f.cohort_trial),'{}'::BIGINT[])) AS metrics
				FROM (SELECT normal.*,
					(normal.admission->>'trial' = 'true'
					 AND NULLIF(policy.value->'state'->>'cohort_id','') IS NOT NULL
					 AND normal.admission->>'cohort_id'=policy.value->'state'->>'cohort_id'
					 AND (COALESCE(normal.admission->>'policy_signature','')=''
					      OR normal.admission->>'policy_signature'=policy.key)) AS cohort_trial
					FROM eligible AS normal
					WHERE normal.account_id=state.account_id AND normal.upstream_key_id=state.upstream_key_id
					AND normal.completed_at>COALESCE(NULLIF(policy.value->'state'->>'quality_since','')::TIMESTAMPTZ,'-infinity'::TIMESTAMPTZ)) AS f) AS projection_window),'{}'::JSONB)
	FROM upstream_cache_guard_accounts AS state
	JOIN accounts AS account ON account.id=state.account_id AND account.upstream_key_id=state.upstream_key_id AND account.deleted_at IS NULL
	LEFT JOIN eligible AS fact ON fact.account_id=state.account_id AND fact.upstream_key_id=state.upstream_key_id
	GROUP BY state.account_id,state.upstream_key_id,state.version,state.state_json
	ORDER BY state.account_id,state.upstream_key_id LIMIT $3`, since.UTC(), until.UTC(), upstreamcacheguard.MaxRuntimeRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]upstreamcacheguard.AccountWindow)
	for rows.Next() {
		var w upstreamcacheguard.AccountWindow
		var stateJSON []byte
		var policyJSON []byte
		var latest sql.NullTime
		var groups, trialGroups pq.Int64Array
		if err = rows.Scan(&w.AccountID, &w.UpstreamKeyID, &w.Version, &stateJSON, &w.ValidRequests,
			&w.TotalInputTokens, &w.CacheReadTokens, &w.TrialValidRequests,
			&w.TrialInputTokens, &w.TrialCacheReadTokens, &w.TrialSessions,
			&latest, &groups, &trialGroups, &policyJSON); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(stateJSON, &w.State); err != nil {
			return nil, fmt.Errorf("cache guard account window state decode: %w", err)
		}
		if len(policyJSON) > 0 {
			if err = json.Unmarshal(policyJSON, &w.Policies); err != nil {
				return nil, fmt.Errorf("cache guard policy window decode: %w", err)
			}
		}
		if w.State.AccountID != w.AccountID || w.State.UpstreamKeyID != w.UpstreamKeyID {
			return nil, errors.New("cache guard account window identity mismatch")
		}
		if w.ValidRequests > upstreamcacheguard.MaxFacts {
			return nil, errors.New("cache guard account window fact bound exceeded")
		}
		if latest.Valid {
			w.LastEvidenceAt = latest.Time
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
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The namespace is private to this guard. Collisions for unusually large
	// group IDs only serialize unrelated groups; they cannot weaken atomicity.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(296, ($1 % 2147483647)::integer)`, groupID); err != nil {
		return err
	}
	if err = fn(&upstreamCacheGuardTx{tx: tx, ctx: ctx, groupID: groupID}); err != nil {
		return err
	}
	return tx.Commit()
}
func (t *upstreamCacheGuardTx) Read(kind, key string, out any) (int64, error) {
	var raw []byte
	var version int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT state_json, version FROM upstream_cache_guard_runtime WHERE group_id=$1 AND state_kind=$2 AND state_key=$3`, t.groupID, kind, key).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return 0, fmt.Errorf("cache guard typed state decode: %w", err)
	}
	return version, nil
}
func (t *upstreamCacheGuardTx) Write(kind, key string, value any) (int64, error) {
	if kind != upstreamcacheguard.GroupKind && kind != upstreamcacheguard.AccountKind && kind != upstreamcacheguard.SessionKind {
		return 0, errors.New("invalid cache guard state kind")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	var version int64
	err = t.tx.QueryRowContext(t.ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at) VALUES($1,$2,$3,1,$4,CURRENT_TIMESTAMP) ON CONFLICT(group_id,state_kind,state_key) DO UPDATE SET version=upstream_cache_guard_runtime.version+1,state_json=EXCLUDED.state_json,updated_at=EXCLUDED.updated_at RETURNING version`, t.groupID, kind, key, string(raw)).Scan(&version)
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
	if r == nil || r.db == nil {
		return upstreamcacheguard.ErrUnavailable
	}
	// processed facts alone expire. Typed session identity and degraded status do
	// not expire into new sessions or automatic healthy status.
	_, err := r.db.ExecContext(ctx, `DELETE FROM upstream_cache_guard_facts WHERE (group_id,evidence_id) IN (SELECT group_id,evidence_id FROM upstream_cache_guard_facts WHERE processed=TRUE AND completed_at<$1 ORDER BY completed_at LIMIT 10000)`, before.UTC())
	return err
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
