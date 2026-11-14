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

// Recent and historical resident sessions are disjoint. Exclude unchanged
// versions before returning JSON, without forming and rejoining a live union
// whose underestimated cardinality can repeatedly rescan the resident CTE.
const upstreamCacheGuardActiveRuntimeRowsSQL = `WITH resident AS MATERIALIZED (
 SELECT * FROM unnest($2::bigint[],$3::text[],$5::bigint[]) AS r(g,k,v)
), changed AS (
 SELECT state.group_id,state.state_kind,state.state_key,state.version,state.state_json,state.updated_at
 FROM upstream_cache_guard_runtime state WHERE state.state_kind='group'
 UNION ALL
 SELECT state.group_id,state.state_kind,state.state_key,state.version,state.state_json,state.updated_at
 FROM upstream_cache_guard_runtime state
 WHERE state.state_kind='session' AND state.updated_at >= $1
 AND NOT EXISTS (
  SELECT 1 FROM resident r WHERE r.g=state.group_id AND r.k=state.state_key AND r.v=state.version
 )
 UNION ALL
 SELECT state.group_id,state.state_kind,state.state_key,state.version,state.state_json,state.updated_at
 FROM resident r JOIN upstream_cache_guard_runtime state
 ON state.group_id=r.g AND state.state_kind='session' AND state.state_key=r.k
 WHERE state.updated_at < $1 AND state.version<>r.v
)
SELECT group_id,state_kind,state_key,version,state_json,updated_at
FROM changed ORDER BY group_id,state_kind,state_key LIMIT $4`

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
// The engine's session map supplies one version per resident (group, key).
func (r *upstreamCacheGuardRepository) ActiveRuntimeRows(ctx context.Context, active []upstreamcacheguard.SessionIdentity, since time.Time) ([]upstreamcacheguard.RuntimeRow, error) {
	if r == nil || r.db == nil {
		return nil, upstreamcacheguard.ErrUnavailable
	}
	upstreamcacheguard.ObserveStageCount(ctx, "active_runtime_resident_sessions", int64(len(active)))
	var returnedRows, payloadBytes int64
	defer func() {
		upstreamcacheguard.ObserveStageCount(ctx, "active_runtime_returned_rows", returnedRows)
		upstreamcacheguard.ObserveStageCount(ctx, "active_runtime_payload_bytes", payloadBytes)
	}()
	groups := make([]int64, len(active))
	keys := make([]string, len(active))
	versions := make([]int64, len(active))
	for i, v := range active {
		groups[i], keys[i], versions[i] = v.GroupID, v.Key, v.Version
	}
	queryDone := upstreamcacheguard.MeasureStage(ctx, "active_runtime_query")
	rows, err := r.db.QueryContext(ctx, upstreamCacheGuardActiveRuntimeRowsSQL, since, pq.Array(groups), pq.Array(keys), upstreamcacheguard.MaxRuntimeRows+1, pq.Array(versions))
	queryDone()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	defer upstreamcacheguard.MeasureStage(ctx, "active_runtime_read")()
	var out []upstreamcacheguard.RuntimeRow
	for rows.Next() {
		var v upstreamcacheguard.RuntimeRow
		if err = rows.Scan(&v.GroupID, &v.Kind, &v.Key, &v.Version, &v.Payload, &v.UpdatedAt); err != nil {
			return nil, err
		}
		returnedRows++
		payloadBytes += int64(len(v.Payload))
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
