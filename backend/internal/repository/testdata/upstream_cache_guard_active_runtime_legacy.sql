WITH resident AS (SELECT * FROM unnest($2::bigint[],$3::text[],$5::bigint[]) AS r(g,k,v)),
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
 ORDER BY state.group_id,state.state_kind,state.state_key LIMIT $4
