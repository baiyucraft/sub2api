package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type groupSchedulingProtectionPolicyRepository struct{ db *sql.DB }

func NewGroupSchedulingProtectionPolicyRepository(db *sql.DB) service.GroupSchedulingProtectionPolicyRepository {
	return &groupSchedulingProtectionPolicyRepository{db: db}
}

const groupProtectionSelect = `SELECT g.id, g.name, g.platform,
 t.mode, t.degradation_ttft_seconds, t.min_samples, t.updated_at,
 p.cache_mode, p.threshold_percent, p.recovery_threshold_percent, p.min_valid_requests, p.priority, p.updated_at
 FROM groups g
 LEFT JOIN fork_group_ttft_guard_policies t ON t.group_id = g.id
 LEFT JOIN fork_group_scheduling_protection_policies p ON p.group_id = g.id`

func (r *groupSchedulingProtectionPolicyRepository) List(ctx context.Context) ([]service.GroupSchedulingProtectionPolicyRecord, error) {
	rows, err := r.db.QueryContext(ctx, groupProtectionSelect+` WHERE g.deleted_at IS NULL ORDER BY g.sort_order, g.name, g.id`)
	if err != nil {
		return nil, fmt.Errorf("list group protection policies: %w", err)
	}
	defer rows.Close()
	out := []service.GroupSchedulingProtectionPolicyRecord{}
	for rows.Next() {
		record, err := scanGroupProtectionPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (r *groupSchedulingProtectionPolicyRepository) Get(ctx context.Context, id int64) (*service.GroupSchedulingProtectionPolicyRecord, error) {
	record, err := scanGroupProtectionPolicy(r.db.QueryRowContext(ctx, groupProtectionSelect+` WHERE g.id = $1 AND g.deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func scanGroupProtectionPolicy(scanner groupTTFTGuardPolicyScanner) (service.GroupSchedulingProtectionPolicyRecord, error) {
	var record service.GroupSchedulingProtectionPolicyRecord
	var ttftMode, cacheMode, priority sql.NullString
	var ttftThreshold, ttftMin, cacheMin sql.NullInt64
	var threshold, recovery sql.NullFloat64
	var ttftUpdated, updated sql.NullTime
	err := scanner.Scan(&record.TTFT.GroupID, &record.TTFT.GroupName, &record.TTFT.GroupPlatform, &ttftMode, &ttftThreshold, &ttftMin, &ttftUpdated, &cacheMode, &threshold, &recovery, &cacheMin, &priority, &updated)
	if err != nil {
		return record, err
	}
	if ttftMode.Valid {
		record.TTFT.Mode = &ttftMode.String
	}
	if ttftThreshold.Valid {
		value := int(ttftThreshold.Int64)
		record.TTFT.DegradationTTFTSeconds = &value
	}
	if ttftMin.Valid {
		value := int(ttftMin.Int64)
		record.TTFT.MinSamples = &value
	}
	if ttftUpdated.Valid {
		record.TTFT.UpdatedAt = &ttftUpdated.Time
	}
	if cacheMode.Valid {
		record.CacheMode = &cacheMode.String
	}
	if threshold.Valid {
		record.ThresholdPercent = &threshold.Float64
	}
	if recovery.Valid {
		record.RecoveryThresholdPercent = &recovery.Float64
	}
	if cacheMin.Valid {
		value := int(cacheMin.Int64)
		record.MinValidRequests = &value
	}
	if priority.Valid {
		record.Priority = &priority.String
	}
	if updated.Valid {
		record.UpdatedAt = &updated.Time
	}
	return record, nil
}

// Both compatibility TTFT storage and joint cache/priority storage commit with
// one durable invalidation event; either write failing rolls the whole PUT back.
func (r *groupSchedulingProtectionPolicyRepository) Put(ctx context.Context, id int64, policy service.GroupSchedulingProtectionPolicyWrite) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, service.ErrGroupNotFound
		}
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fork_group_ttft_guard_policies (group_id, mode, degradation_ttft_seconds, min_samples, created_at, updated_at)
 VALUES ($1,$2,$3,$4,NOW(),NOW()) ON CONFLICT (group_id) DO UPDATE SET mode=EXCLUDED.mode,
 degradation_ttft_seconds=EXCLUDED.degradation_ttft_seconds,min_samples=EXCLUDED.min_samples,updated_at=NOW()`, id, policy.TTFTMode, policy.TTFTThresholdSeconds, policy.TTFTMinSamples)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fork_group_scheduling_protection_policies (group_id,cache_mode,threshold_percent,recovery_threshold_percent,min_valid_requests,priority,created_at,updated_at)
 VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW()) ON CONFLICT (group_id) DO UPDATE SET cache_mode=EXCLUDED.cache_mode,
 threshold_percent=EXCLUDED.threshold_percent,recovery_threshold_percent=EXCLUDED.recovery_threshold_percent,
 min_valid_requests=EXCLUDED.min_valid_requests,priority=EXCLUDED.priority,updated_at=NOW()`, id, policy.CacheMode, policy.ThresholdPercent, policy.RecoveryThresholdPercent, policy.MinValidRequests, policy.Priority)
	if err != nil {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventGroupChanged, nil, &id, map[string]any{service.SchedulerOutboxPayloadTTFTGuardPolicyChanged: true}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
