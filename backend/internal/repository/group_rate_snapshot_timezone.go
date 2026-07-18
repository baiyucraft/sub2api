package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type sqlStateError interface {
	SQLState() string
}

const ensureGroupRateTimezoneSnapshotsSQL = `
INSERT INTO group_rate_snapshots
    (group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
     peak_rate_multiplier, timezone, effective_at)
SELECT g.id, g.rate_multiplier, g.peak_rate_enabled, g.peak_start, g.peak_end,
       g.peak_rate_multiplier, $1::varchar(64), NOW()
  FROM groups g
  LEFT JOIN LATERAL (
      SELECT s.timezone
        FROM group_rate_snapshots s
       WHERE s.group_id = g.id
       ORDER BY s.effective_at DESC, s.id DESC
       LIMIT 1
  ) latest ON TRUE
 WHERE g.deleted_at IS NULL
   AND latest.timezone IS DISTINCT FROM $1::varchar(64)
`

const groupRateTimezoneLockSQL = `SELECT pg_advisory_xact_lock(195, 1)`

// ensureGroupRateTimezoneSnapshots appends one current configuration snapshot
// when the application timezone changes. Database connections already receive
// this timezone through the DSN; this explicit startup step also captures a
// timezone-only configuration change that would not fire the groups trigger.
func ensureGroupRateTimezoneSnapshots(ctx context.Context, db *sql.DB, timezoneName string) error {
	timezoneName = strings.TrimSpace(timezoneName)
	if timezoneName == "" || db == nil {
		return fmt.Errorf("group rate snapshot timezone and database are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin group rate timezone snapshots: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, groupRateTimezoneLockSQL); err != nil {
		return fmt.Errorf("lock group rate timezone snapshots: %w", err)
	}
	if _, err := tx.ExecContext(ctx, ensureGroupRateTimezoneSnapshotsSQL, timezoneName); err != nil {
		timezoneHash := sha256.Sum256([]byte(timezoneName))
		var stateError sqlStateError
		if errors.As(err, &stateError) {
			return fmt.Errorf("ensure group rate timezone snapshots (timezone_len=%d timezone_sha=%x sqlstate=%s): %w", len(timezoneName), timezoneHash[:6], stateError.SQLState(), err)
		}
		return fmt.Errorf("ensure group rate timezone snapshots (timezone_len=%d timezone_sha=%x): %w", len(timezoneName), timezoneHash[:6], err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group rate timezone snapshots: %w", err)
	}
	return nil
}
