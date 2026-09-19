package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type groupTTFTGuardPolicyRepository struct {
	db *sql.DB
}

func NewGroupTTFTGuardPolicyRepository(db *sql.DB) service.GroupTTFTGuardPolicyRepository {
	return &groupTTFTGuardPolicyRepository{db: db}
}

func (r *groupTTFTGuardPolicyRepository) List(ctx context.Context) ([]service.GroupTTFTGuardPolicyRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.name, g.platform, p.mode, p.degradation_ttft_seconds, p.min_samples, p.updated_at
		FROM groups g
		LEFT JOIN fork_group_ttft_guard_policies p ON p.group_id = g.id
		WHERE g.deleted_at IS NULL AND g.platform IN ($1, $2)
		ORDER BY g.sort_order ASC, g.name ASC, g.id ASC`, service.PlatformOpenAI, service.PlatformComposite)
	if err != nil {
		return nil, fmt.Errorf("list group TTFT guard policies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.GroupTTFTGuardPolicyRecord, 0)
	for rows.Next() {
		record, scanErr := scanGroupTTFTGuardPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group TTFT guard policies: %w", err)
	}
	return result, nil
}

func (r *groupTTFTGuardPolicyRepository) Get(ctx context.Context, groupID int64) (*service.GroupTTFTGuardPolicyRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.platform, p.mode, p.degradation_ttft_seconds, p.min_samples, p.updated_at
		FROM groups g
		LEFT JOIN fork_group_ttft_guard_policies p ON p.group_id = g.id
		WHERE g.id = $1 AND g.deleted_at IS NULL`, groupID)
	record, err := scanGroupTTFTGuardPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get group TTFT guard policy: %w", err)
	}
	return &record, nil
}

func (r *groupTTFTGuardPolicyRepository) Put(ctx context.Context, groupID int64, mode string, degradationTTFTSeconds, minSamples int) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin group TTFT guard policy transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var changedGroupID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO fork_group_ttft_guard_policies (
			group_id, mode, degradation_ttft_seconds, min_samples, created_at, updated_at
		) VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (group_id) DO UPDATE SET
			mode = EXCLUDED.mode,
			degradation_ttft_seconds = EXCLUDED.degradation_ttft_seconds,
			min_samples = EXCLUDED.min_samples,
			updated_at = NOW()
		WHERE fork_group_ttft_guard_policies.mode IS DISTINCT FROM EXCLUDED.mode
		   OR fork_group_ttft_guard_policies.degradation_ttft_seconds IS DISTINCT FROM EXCLUDED.degradation_ttft_seconds
		   OR fork_group_ttft_guard_policies.min_samples IS DISTINCT FROM EXCLUDED.min_samples
		RETURNING group_id`, groupID, mode, degradationTTFTSeconds, minSamples).Scan(&changedGroupID)
	changed := true
	if errors.Is(err, sql.ErrNoRows) {
		changed = false
	} else if err != nil {
		return false, fmt.Errorf("put group TTFT guard policy: %w", err)
	}
	if changed {
		payload := map[string]any{service.SchedulerOutboxPayloadTTFTGuardPolicyChanged: true}
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventGroupChanged, nil, &changedGroupID, payload); err != nil {
			return false, fmt.Errorf("enqueue group TTFT guard policy change: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit group TTFT guard policy: %w", err)
	}
	return changed, nil
}

type groupTTFTGuardPolicyScanner interface {
	Scan(dest ...any) error
}

func scanGroupTTFTGuardPolicy(scanner groupTTFTGuardPolicyScanner) (service.GroupTTFTGuardPolicyRecord, error) {
	var record service.GroupTTFTGuardPolicyRecord
	var mode sql.NullString
	var threshold sql.NullInt32
	var minSamples sql.NullInt32
	var updatedAt sql.NullTime
	if err := scanner.Scan(&record.GroupID, &record.GroupName, &record.GroupPlatform, &mode, &threshold, &minSamples, &updatedAt); err != nil {
		return record, err
	}
	if mode.Valid {
		value := mode.String
		record.Mode = &value
	}
	if threshold.Valid {
		value := int(threshold.Int32)
		record.DegradationTTFTSeconds = &value
	}
	if minSamples.Valid {
		value := int(minSamples.Int32)
		record.MinSamples = &value
	}
	if updatedAt.Valid {
		value := updatedAt.Time
		record.UpdatedAt = &value
	}
	return record, nil
}
