package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type extraCostRepository struct {
	db *sql.DB
}

func NewExtraCostRepository(db *sql.DB) service.ExtraCostRepository {
	return &extraCostRepository{db: db}
}

func (r *extraCostRepository) List(ctx context.Context, filter service.ExtraCostFilter) ([]service.ExtraCostEntry, int64, error) {
	where := []string{"1=1"}
	args := make([]any, 0, 4)
	if filter.StartDate != nil {
		where = append(where, fmt.Sprintf("cost_date >= $%d", len(args)+1))
		args = append(args, filter.StartDate.Format("2006-01-02"))
	}
	if filter.EndDate != nil {
		where = append(where, fmt.Sprintf("cost_date < $%d", len(args)+1))
		args = append(args, filter.EndDate.Format("2006-01-02"))
	}
	if filter.Category != "" {
		where = append(where, fmt.Sprintf("category = $%d", len(args)+1))
		args = append(args, filter.Category)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM extra_cost_entries WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > service.ExtraCostMaxPageSize {
		pageSize = 20
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query := `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version
		FROM extra_cost_entries WHERE ` + whereSQL + fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]service.ExtraCostEntry, 0)
	for rows.Next() {
		entry, err := scanExtraCostRow(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *extraCostRepository) Create(ctx context.Context, entry service.ExtraCostEntry) (*service.ExtraCostEntry, error) {
	query := `INSERT INTO extra_cost_entries
		(cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`
	result, err := scanExtraCostRow(r.db.QueryRowContext(ctx, query,
		entry.CostDate, entry.Amount, entry.Category, entry.Notes, entry.CreatedBy, entry.CreatedAt, entry.ReversalOf, nullableString(entry.IdempotencyKey), entry.RuleVersion))
	if err == nil {
		return &result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if entry.IdempotencyKey == "" {
		return nil, sql.ErrNoRows
	}
	var existing service.ExtraCostEntry
	existing, err = scanExtraCostRow(r.db.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = $1`, entry.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	if existing.ReversalOf != nil {
		return nil, service.ErrExtraCostIdempotencyConflict
	}
	if !sameExtraCostCreateRequest(existing, entry) {
		return nil, service.ErrExtraCostIdempotencyConflict
	}
	return &existing, nil
}

func (r *extraCostRepository) GetByID(ctx context.Context, id int64) (*service.ExtraCostEntry, error) {
	entry, err := scanExtraCostRow(r.db.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrExtraCostNotFound
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (r *extraCostRepository) Reverse(ctx context.Context, id int64, adjustment service.ExtraCostEntry) (*service.ExtraCostEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var original service.ExtraCostEntry
	original, err = scanExtraCostRow(tx.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrExtraCostNotFound
	}
	if err != nil {
		return nil, err
	}
	if adjustment.IdempotencyKey == "" {
		adjustment.IdempotencyKey = fmt.Sprintf("reverse:%d", id)
	}
	existing, err := scanExtraCostRow(tx.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = $1`, adjustment.IdempotencyKey))
	if err == nil {
		if !sameExtraCostReversalRequest(existing, id, adjustment) {
			return nil, service.ErrExtraCostIdempotencyConflict
		}
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var existingID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM extra_cost_entries WHERE reversal_of = $1 LIMIT 1`, id).Scan(&existingID); err == nil {
		return nil, service.ErrExtraCostAlreadyReversed
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	entry, err := scanExtraCostRow(tx.QueryRowContext(ctx, `INSERT INTO extra_cost_entries
		(cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, 'adjustment', $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`,
		adjustment.CostDate, -original.Amount, adjustment.Notes, adjustment.CreatedBy, adjustment.CreatedAt, id, adjustment.IdempotencyKey, service.ExtraCostRuleVersion))
	if errors.Is(err, sql.ErrNoRows) {
		entry, err = scanExtraCostRow(tx.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = $1`, adjustment.IdempotencyKey))
	}
	if err != nil {
		return nil, err
	}
	if !sameExtraCostReversalRequest(entry, id, adjustment) {
		return nil, service.ErrExtraCostIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &entry, nil
}

func sameExtraCostCreateRequest(existing, requested service.ExtraCostEntry) bool {
	return amountsEqualAtExtraCostScale(existing.Amount, requested.Amount) &&
		existing.Category == requested.Category &&
		existing.Notes == requested.Notes &&
		sameOptionalInt64(existing.CreatedBy, requested.CreatedBy)
}

func amountsEqualAtExtraCostScale(left, right float64) bool {
	const scale = 100_000_000.0
	return math.Round(left*scale) == math.Round(right*scale)
}

func sameExtraCostReversalRequest(existing service.ExtraCostEntry, originalID int64, requested service.ExtraCostEntry) bool {
	return existing.ReversalOf != nil && *existing.ReversalOf == originalID &&
		existing.Notes == requested.Notes &&
		sameOptionalInt64(existing.CreatedBy, requested.CreatedBy)
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *extraCostRepository) Sum(ctx context.Context, start, end *time.Time) (float64, error) {
	where := []string{"1=1"}
	args := make([]any, 0, 2)
	if start != nil {
		where = append(where, fmt.Sprintf("cost_date >= $%d", len(args)+1))
		args = append(args, start.Format("2006-01-02"))
	}
	if end != nil {
		where = append(where, fmt.Sprintf("cost_date < $%d", len(args)+1))
		args = append(args, end.Format("2006-01-02"))
	}
	var sum float64
	err := r.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount), 0) FROM extra_cost_entries WHERE "+strings.Join(where, " AND "), args...).Scan(&sum)
	return sum, err
}

func (r *extraCostRepository) DailySums(ctx context.Context, start, end *time.Time) (map[string]float64, error) {
	where := []string{"1=1"}
	args := make([]any, 0, 2)
	if start != nil {
		where = append(where, fmt.Sprintf("cost_date >= $%d", len(args)+1))
		args = append(args, start.Format("2006-01-02"))
	}
	if end != nil {
		where = append(where, fmt.Sprintf("cost_date < $%d", len(args)+1))
		args = append(args, end.Format("2006-01-02"))
	}
	rows, err := r.db.QueryContext(ctx, "SELECT cost_date::text, COALESCE(SUM(amount), 0) FROM extra_cost_entries WHERE "+strings.Join(where, " AND ")+" GROUP BY cost_date ORDER BY cost_date", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]float64)
	for rows.Next() {
		var date string
		var amount float64
		if err := rows.Scan(&date, &amount); err != nil {
			return nil, err
		}
		result[date] = amount
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type extraCostScanner interface{ Scan(dest ...any) error }

func scanExtraCostRow(row extraCostScanner) (service.ExtraCostEntry, error) {
	var e service.ExtraCostEntry
	var createdBy, reversalOf sql.NullInt64
	var createdAt sql.NullTime
	var idempotency, ruleVersion sql.NullString
	err := row.Scan(&e.ID, &e.CostDate, &e.Amount, &e.Category, &e.Notes, &createdBy, &createdAt, &reversalOf, &idempotency, &ruleVersion)
	if createdBy.Valid {
		e.CreatedBy = &createdBy.Int64
	}
	if reversalOf.Valid {
		e.ReversalOf = &reversalOf.Int64
	}
	if createdAt.Valid {
		e.CreatedAt = createdAt.Time
	}
	if idempotency.Valid {
		e.IdempotencyKey = idempotency.String
	}
	e.RuleVersion = ruleVersion.String
	return e, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
