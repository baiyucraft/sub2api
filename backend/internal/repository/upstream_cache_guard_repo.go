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
)

type upstreamCacheGuardRepository struct{ db *sql.DB }

var _ upstreamcacheguard.Store = (*upstreamCacheGuardRepository)(nil)

func NewUpstreamCacheGuardRepository(db *sql.DB) upstreamcacheguard.Store {
	return &upstreamCacheGuardRepository{db: db}
}

type upstreamCacheGuardTx struct {
	tx      *sql.Tx
	ctx     context.Context
	groupID int64
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
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,completed_at,valid_sample,evidence_json,processed) VALUES($1,$2,$3,$4,$5,$6,TRUE) ON CONFLICT(group_id,evidence_id) DO UPDATE SET processed=TRUE,claim_owner=NULL,claim_until=NULL WHERE upstream_cache_guard_facts.processed=FALSE`, t.groupID, e.ID, e.Request.AccountID, e.CompletedAt, e.ValidSample(), string(raw))
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
	_, err = r.db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,completed_at,valid_sample,evidence_json,processed) VALUES($1,$2,$3,$4,$5,$6,FALSE) ON CONFLICT(group_id,evidence_id) DO NOTHING`, e.Request.GroupID, e.ID, e.Request.AccountID, e.CompletedAt, e.ValidSample(), string(raw))
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
