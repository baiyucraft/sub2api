package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
)

var _ upstreamcacheguard.IncrementalStore = (*upstreamCacheGuardRepository)(nil)

func (t *upstreamCacheGuardTx) FactVersion(account, key int64) (int64, error) {
	if account != t.accountID || key != t.keyID {
		return 0, errors.New("cache guard revision identity mismatch")
	}
	var version int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT version FROM upstream_cache_guard_fact_versions WHERE account_id=$1 AND upstream_key_id=$2`, account, key).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}

func (r *upstreamCacheGuardRepository) IdentityVersions(ctx context.Context) ([]upstreamcacheguard.IdentityVersion, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	rows, err := r.db.QueryContext(ctx, `SELECT a.id,a.upstream_key_id,COALESCE(s.version,0),COALESCE(v.version,0),
  s.account_id IS NULL AND EXISTS(SELECT 1 FROM upstream_cache_guard_runtime old WHERE old.state_kind='account' AND old.state_json->>'account_id'=a.id::text AND old.state_json->>'upstream_key_id'=a.upstream_key_id::text)
  FROM accounts a LEFT JOIN upstream_cache_guard_accounts s ON s.account_id=a.id AND s.upstream_key_id=a.upstream_key_id
  LEFT JOIN upstream_cache_guard_fact_versions v ON v.account_id=a.id AND v.upstream_key_id=a.upstream_key_id
  WHERE a.deleted_at IS NULL AND a.upstream_key_id>0 AND (s.account_id IS NOT NULL OR v.account_id IS NOT NULL OR EXISTS(SELECT 1 FROM upstream_cache_guard_runtime old WHERE old.state_kind='account' AND old.state_json->>'account_id'=a.id::text AND old.state_json->>'upstream_key_id'=a.upstream_key_id::text))
  ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upstreamcacheguard.IdentityVersion
	for rows.Next() {
		var v upstreamcacheguard.IdentityVersion
		if err = rows.Scan(&v.AccountID, &v.UpstreamKeyID, &v.Version, &v.FactVersion, &v.Legacy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Only persisted recently touched sessions and explicitly resident identities
// participate in the one-second refresh; old sessions remain addressable by PK.
func (r *upstreamCacheGuardRepository) ActiveRuntimeRows(ctx context.Context, active []upstreamcacheguard.SessionIdentity, since time.Time) ([]upstreamcacheguard.RuntimeRow, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	groups := make([]int64, len(active))
	keys := make([]string, len(active))
	versions := make([]int64, len(active))
	for i, v := range active {
		groups[i], keys[i], versions[i] = v.GroupID, v.Key, v.Version
	}
	rows, err := r.db.QueryContext(ctx, `WITH resident AS (SELECT * FROM unnest($2::bigint[],$3::text[],$5::bigint[]) AS r(g,k,v)),
 live AS (
 SELECT group_id,state_kind,state_key,version FROM upstream_cache_guard_runtime WHERE state_kind='group'
 UNION
 SELECT group_id,state_kind,state_key,version FROM upstream_cache_guard_runtime WHERE state_kind='session' AND updated_at >= $1
 UNION
 SELECT state.group_id,state.state_kind,state.state_key,state.version FROM resident r JOIN upstream_cache_guard_runtime state ON state.group_id=r.g AND state.state_kind='session' AND state.state_key=r.k
 )
 SELECT state.group_id,state.state_kind,state.state_key,state.version,state.state_json,state.updated_at
 FROM live JOIN upstream_cache_guard_runtime state ON state.group_id=live.group_id AND state.state_kind=live.state_kind AND state.state_key=live.state_key
 LEFT JOIN resident r ON r.g=live.group_id AND r.k=live.state_key AND live.state_kind='session'
 WHERE live.state_kind='group' OR r.v IS NULL OR r.v<>live.version
 ORDER BY state.group_id,state.state_kind,state.state_key LIMIT $4`, since, pq.Array(groups), pq.Array(keys), upstreamcacheguard.MaxRuntimeRows+1, pq.Array(versions))
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
			return nil, errors.New("cache guard active runtime bound exceeded")
		}
	}
	return out, rows.Err()
}

func (r *upstreamCacheGuardRepository) LoadSession(ctx context.Context, group int64, key string) (*upstreamcacheguard.SessionState, int64, error) {
	defer upstreamcacheguard.MeasureStage(ctx, "session_cold_load")()
	if r == nil || r.db == nil {
		return nil, 0, upstreamcacheguard.ErrUnavailable
	}
	var raw []byte
	var version int64
	err := r.db.QueryRowContext(ctx, `SELECT state_json,version FROM upstream_cache_guard_runtime WHERE group_id=$1 AND state_kind='session' AND state_key=$2`, group, key).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var value upstreamcacheguard.SessionState
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, 0, err
	}
	return &value, version, nil
}
