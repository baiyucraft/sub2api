package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const listGroupRateSnapshotsSQL = `
WITH observed AS (
    SELECT group_id, MIN(effective_at) AS observed_since
      FROM group_rate_snapshots
     WHERE group_id = ANY($1)
       AND effective_at <= $3
     GROUP BY group_id
),
baseline AS (
    SELECT DISTINCT ON (group_id)
           group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
           peak_rate_multiplier, timezone, effective_at
      FROM group_rate_snapshots
     WHERE group_id = ANY($1)
       AND effective_at < $2
       AND effective_at <= $3
     ORDER BY group_id, effective_at DESC, id DESC
),
ranged AS (
    SELECT group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
           peak_rate_multiplier, timezone, effective_at
      FROM group_rate_snapshots
     WHERE group_id = ANY($1)
       AND effective_at >= $2
       AND effective_at <= $3
),
selected AS (
    SELECT * FROM baseline
    UNION ALL
    SELECT * FROM ranged
)
SELECT selected.group_id, selected.rate_multiplier, selected.peak_rate_enabled,
       selected.peak_start, selected.peak_end, selected.peak_rate_multiplier,
       selected.timezone, selected.effective_at, observed.observed_since
  FROM selected
  JOIN observed USING (group_id)
 ORDER BY selected.group_id, selected.effective_at
`

// ListGroupRateSnapshots loads every requested group's window in one query.
// It includes the last version before from as the baseline, but preserves the
// actual earliest snapshot timestamp so callers never imply fabricated history.
func (r *channelMonitorRepository) ListGroupRateSnapshots(
	ctx context.Context,
	groupIDs []int64,
	from, until time.Time,
) (map[int64]service.GroupRateSnapshotSeries, error) {
	out := make(map[int64]service.GroupRateSnapshotSeries, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, listGroupRateSnapshotsSQL, pq.Array(groupIDs), from, until)
	if err != nil {
		return nil, fmt.Errorf("list group rate snapshots: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var snapshot service.GroupRateSnapshot
		var observedSince time.Time
		if err := rows.Scan(
			&snapshot.GroupID,
			&snapshot.RateMultiplier,
			&snapshot.PeakRateEnabled,
			&snapshot.PeakStart,
			&snapshot.PeakEnd,
			&snapshot.PeakRateMultiplier,
			&snapshot.Timezone,
			&snapshot.EffectiveAt,
			&observedSince,
		); err != nil {
			return nil, fmt.Errorf("scan group rate snapshot: %w", err)
		}
		series := out[snapshot.GroupID]
		series.ObservedSince = observedSince
		series.Snapshots = append(series.Snapshots, snapshot)
		out[snapshot.GroupID] = series
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group rate snapshots: %w", err)
	}
	return out, nil
}
