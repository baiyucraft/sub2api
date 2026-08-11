package repository

import (
	"context"
	"time"
)

// ListRecentUpstreamProbeModels returns a narrow, de-duplicated model catalog
// from recent real traffic for upstream-bound accounts. It intentionally
// exposes only platform/model names and never request or credential data.
func (r *accountRepository) ListRecentUpstreamProbeModels(ctx context.Context, since time.Time, limit int) (map[string][]string, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT LOWER(TRIM(a.platform)) AS platform, TRIM(models.model_value) AS model
FROM usage_logs ul
JOIN accounts a ON a.id = ul.account_id
CROSS JOIN LATERAL (
  VALUES
    (COALESCE(NULLIF(TRIM(ul.requested_model), ''), NULLIF(TRIM(ul.model), ''))),
    (NULLIF(TRIM(ul.upstream_model), ''))
) AS models(model_value)
WHERE ul.upstream_config_id IS NOT NULL
  AND ul.created_at >= $1
  AND models.model_value IS NOT NULL
  AND LOWER(TRIM(a.platform)) IN ('openai', 'anthropic', 'gemini')
GROUP BY LOWER(TRIM(a.platform)), TRIM(models.model_value)
ORDER BY LOWER(TRIM(a.platform)), TRIM(models.model_value)
LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string][]string{}
	for rows.Next() {
		var platform, model string
		if err := rows.Scan(&platform, &model); err != nil {
			return nil, err
		}
		result[platform] = append(result[platform], model)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
