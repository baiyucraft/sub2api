WITH eligible AS (
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
	ORDER BY state.account_id,state.upstream_key_id LIMIT $3
