package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type sqlStateError interface {
	SQLState() string
}

const ensureGroupRateTimezoneSnapshotsSQL = `
WITH timezone_lock AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(195, 1)
)
INSERT INTO group_rate_snapshots
    (group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
     peak_rate_multiplier, timezone, effective_at)
SELECT g.id, g.rate_multiplier, g.peak_rate_enabled, g.peak_start, g.peak_end,
       g.peak_rate_multiplier, $1, NOW()
  FROM groups g
  CROSS JOIN timezone_lock
  LEFT JOIN LATERAL (
      SELECT s.timezone
        FROM group_rate_snapshots s
       WHERE s.group_id = g.id
       ORDER BY s.effective_at DESC, s.id DESC
       LIMIT 1
  ) latest ON TRUE
 WHERE g.deleted_at IS NULL
   AND latest.timezone IS DISTINCT FROM $1
`

// ensureGroupRateTimezoneSnapshots appends one current configuration snapshot
// when the application timezone changes. Database connections already receive
// this timezone through the DSN; this explicit startup step also captures a
// timezone-only configuration change that would not fire the groups trigger.
func ensureGroupRateTimezoneSnapshots(ctx context.Context, db *sql.DB, timezoneName string) error {
	timezoneName = strings.TrimSpace(timezoneName)
	if timezoneName == "" || db == nil {
		return fmt.Errorf("group rate snapshot timezone and database are required")
	}
	if _, err := db.ExecContext(ctx, ensureGroupRateTimezoneSnapshotsSQL, timezoneName); err != nil {
		var stateError sqlStateError
		if errors.As(err, &stateError) {
			return fmt.Errorf("ensure group rate timezone snapshots (sqlstate=%s): %w", stateError.SQLState(), err)
		}
		return fmt.Errorf("ensure group rate timezone snapshots: %w", err)
	}
	return nil
}
