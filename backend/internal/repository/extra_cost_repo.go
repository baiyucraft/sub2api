package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		FROM extra_cost_entries WHERE ` + whereSQL + fmt.Sprintf(" ORDER BY cost_date DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
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
		(cost_date, amount, category, notes, created_by, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`
	result, err := scanExtraCostRow(r.db.QueryRowContext(ctx, query,
		entry.CostDate, entry.Amount, entry.Category, entry.Notes, entry.CreatedBy, entry.ReversalOf, nullableString(entry.IdempotencyKey), entry.RuleVersion))
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

func (r *extraCostRepository) Reverse(ctx context.Context, id int64, createdBy *int64, reason, idempotencyKey string) (*service.ExtraCostEntry, error) {
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
	var existingID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM extra_cost_entries WHERE reversal_of = $1 LIMIT 1`, id).Scan(&existingID); err == nil {
		return nil, service.ErrExtraCostAlreadyReversed
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("reverse:%d", id)
	}
	entry, err := scanExtraCostRow(tx.QueryRowContext(ctx, `INSERT INTO extra_cost_entries
		(cost_date, amount, category, notes, created_by, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, 'adjustment', $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`,
		original.CostDate, -original.Amount, reason, createdBy, id, idempotencyKey, service.ExtraCostRuleVersion))
	if errors.Is(err, sql.ErrNoRows) {
		entry, err = scanExtraCostRow(tx.QueryRowContext(ctx, `SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = $1`, idempotencyKey))
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &entry, nil
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
