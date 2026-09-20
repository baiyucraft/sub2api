package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func extraCostRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "cost_date", "amount", "category", "notes", "created_by", "created_at",
		"reversal_of", "idempotency_key", "rule_version",
	})
}

func TestExtraCostRepositoryCreateWritesOccurrenceTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	occurredAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.FixedZone("CST", 8*60*60))
	entry := service.ExtraCostEntry{
		CostDate: "2026-09-18", Amount: 3.5, Category: service.ExtraCostCategoryAccount,
		Notes: "purchase", CreatedAt: occurredAt, IdempotencyKey: "create-key", RuleVersion: service.ExtraCostRuleVersion,
	}

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO extra_cost_entries
		(cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`)).
		WithArgs(entry.CostDate, entry.Amount, entry.Category, entry.Notes, nil, occurredAt, nil, entry.IdempotencyKey, entry.RuleVersion).
		WillReturnRows(extraCostRows().AddRow(1, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), []byte("3.50000000"), entry.Category, entry.Notes, nil, occurredAt, nil, entry.IdempotencyKey, entry.RuleVersion))

	created, err := repo.Create(context.Background(), entry)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created.CreatedAt.Equal(occurredAt) {
		t.Fatalf("CreatedAt = %v, want %v", created.CreatedAt, occurredAt)
	}
	if created.CostDate != entry.CostDate {
		t.Fatalf("CostDate = %q, want %q", created.CostDate, entry.CostDate)
	}
	if created.Amount != entry.Amount {
		t.Fatalf("Amount = %v, want %v", created.Amount, entry.Amount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryCreateReplaysMatchingRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	createdBy := int64(7)
	entry := service.ExtraCostEntry{Amount: 3.5, Category: service.ExtraCostCategoryAccount, Notes: "purchase", CreatedBy: &createdBy, IdempotencyKey: "create-key"}
	existingAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)

	mock.ExpectQuery("INSERT INTO extra_cost_entries").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(entry.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(1, "2026-09-18", entry.Amount, entry.Category, entry.Notes, createdBy, existingAt, nil, entry.IdempotencyKey, service.ExtraCostRuleVersion))

	created, err := repo.Create(context.Background(), entry)
	if err != nil {
		t.Fatalf("Create() replay error = %v", err)
	}
	if created.ID != 1 {
		t.Fatalf("Create() replay ID = %d, want 1", created.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryCreateReplaysAmountAtDatabaseScale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	entry := service.ExtraCostEntry{Amount: 1.000000009, Category: service.ExtraCostCategoryAccount, IdempotencyKey: "precision-key"}
	existingAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)

	mock.ExpectQuery("INSERT INTO extra_cost_entries").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(entry.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(1, "2026-09-18", 1.00000001, entry.Category, "", nil, existingAt, nil, entry.IdempotencyKey, service.ExtraCostRuleVersion))

	if _, err := repo.Create(context.Background(), entry); err != nil {
		t.Fatalf("Create() precision replay error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryCreateRejectsChangedRequestForSameIdempotencyKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	entry := service.ExtraCostEntry{Amount: 9, Category: service.ExtraCostCategoryAccount, Notes: "changed", IdempotencyKey: "create-key"}
	existingAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)

	mock.ExpectQuery("INSERT INTO extra_cost_entries").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(entry.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(1, "2026-09-18", 3.5, entry.Category, "original", nil, existingAt, nil, entry.IdempotencyKey, service.ExtraCostRuleVersion))

	_, err = repo.Create(context.Background(), entry)
	if err != service.ErrExtraCostIdempotencyConflict {
		t.Fatalf("Create() error = %v, want idempotency conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryListOrdersByOccurrenceTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}

	createdAt := time.Date(2026, 9, 20, 14, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM extra_cost_entries WHERE 1=1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`ORDER BY created_at DESC, id DESC LIMIT \$1 OFFSET \$2`).
		WithArgs(20, 0).
		WillReturnRows(extraCostRows().AddRow(
			37,
			time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
			[]byte("12.34000000"),
			service.ExtraCostCategoryAccount,
			"purchase",
			nil,
			createdAt,
			nil,
			"list-key",
			service.ExtraCostRuleVersion,
		))

	items, total, err := repo.List(context.Background(), service.ExtraCostFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("List() returned total=%d items=%d, want 1 and 1", total, len(items))
	}
	if items[0].CostDate != "2026-09-20" || items[0].Amount != 12.34 || !items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("List() item = %+v, want normalized PostgreSQL values", items[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryReverseWritesReversalOccurrenceTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	originalCreatedAt := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	reversalAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.FixedZone("CST", 8*60*60))
	adjustment := service.ExtraCostEntry{
		CostDate: "2026-09-18", Notes: "correct entry", CreatedAt: reversalAt,
		IdempotencyKey: "reverse-key", RuleVersion: service.ExtraCostRuleVersion,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(extraCostRows().AddRow(42, "2026-08-01", 8.0, service.ExtraCostCategoryAccount, "purchase", nil, originalCreatedAt, nil, "original-key", service.ExtraCostRuleVersion))
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = \$1`).
		WithArgs(adjustment.IdempotencyKey).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM extra_cost_entries WHERE reversal_of = \$1 LIMIT 1`).
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO extra_cost_entries
		(cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version)
		VALUES ($1, $2, 'adjustment', $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
		RETURNING id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version`)).
		WithArgs(adjustment.CostDate, -8.0, adjustment.Notes, nil, reversalAt, int64(42), adjustment.IdempotencyKey, adjustment.RuleVersion).
		WillReturnRows(extraCostRows().AddRow(43, adjustment.CostDate, -8.0, service.ExtraCostCategoryAdjust, adjustment.Notes, nil, reversalAt, 42, adjustment.IdempotencyKey, adjustment.RuleVersion))
	mock.ExpectCommit()

	created, err := repo.Reverse(context.Background(), 42, adjustment)
	if err != nil {
		t.Fatalf("Reverse() error = %v", err)
	}
	if created.CostDate != adjustment.CostDate || !created.CreatedAt.Equal(reversalAt) {
		t.Fatalf("created adjustment = %+v, want date %s at %v", created, adjustment.CostDate, reversalAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryReverseReplaysMatchingIdempotencyKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	originalCreatedAt := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	reversalAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)
	adjustment := service.ExtraCostEntry{CostDate: "2026-09-18", Notes: "correct entry", CreatedAt: reversalAt, IdempotencyKey: "reverse-key"}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(extraCostRows().AddRow(42, "2026-08-01", 8.0, service.ExtraCostCategoryAccount, "purchase", nil, originalCreatedAt, nil, "original-key", service.ExtraCostRuleVersion))
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = \$1`).
		WithArgs(adjustment.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(43, adjustment.CostDate, -8.0, service.ExtraCostCategoryAdjust, "correct entry", nil, reversalAt, 42, adjustment.IdempotencyKey, service.ExtraCostRuleVersion))
	mock.ExpectRollback()

	created, err := repo.Reverse(context.Background(), 42, adjustment)
	if err != nil {
		t.Fatalf("Reverse() replay error = %v", err)
	}
	if created.ID != 43 || created.ReversalOf == nil || *created.ReversalOf != 42 {
		t.Fatalf("replayed entry = %+v, want matching reversal", created)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryReverseRejectsIdempotencyKeyFromAnotherOperation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	createdAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)
	adjustment := service.ExtraCostEntry{CostDate: "2026-09-18", CreatedAt: createdAt, IdempotencyKey: "shared-key"}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(extraCostRows().AddRow(42, "2026-08-01", 8.0, service.ExtraCostCategoryAccount, "purchase", nil, createdAt, nil, "original-key", service.ExtraCostRuleVersion))
	mock.ExpectQuery(`SELECT id, cost_date, amount, category, notes, created_by, created_at, reversal_of, idempotency_key, rule_version FROM extra_cost_entries WHERE idempotency_key = \$1`).
		WithArgs(adjustment.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(99, "2026-09-18", 2.0, service.ExtraCostCategoryAccount, "other create", nil, createdAt, nil, adjustment.IdempotencyKey, service.ExtraCostRuleVersion))
	mock.ExpectRollback()

	_, err = repo.Reverse(context.Background(), 42, adjustment)
	if err != service.ErrExtraCostIdempotencyConflict {
		t.Fatalf("Reverse() error = %v, want idempotency conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryReverseRejectsChangedReasonForSameIdempotencyKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	createdAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)
	adjustment := service.ExtraCostEntry{Notes: "changed reason", IdempotencyKey: "reverse-key"}

	mock.ExpectBegin()
	mock.ExpectQuery(`WHERE id = \$1 FOR UPDATE`).WithArgs(int64(42)).
		WillReturnRows(extraCostRows().AddRow(42, "2026-08-01", 8.0, service.ExtraCostCategoryAccount, "purchase", nil, createdAt, nil, "original-key", service.ExtraCostRuleVersion))
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(adjustment.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(43, "2026-09-18", -8.0, service.ExtraCostCategoryAdjust, "original reason", nil, createdAt, 42, adjustment.IdempotencyKey, service.ExtraCostRuleVersion))
	mock.ExpectRollback()

	_, err = repo.Reverse(context.Background(), 42, adjustment)
	if err != service.ErrExtraCostIdempotencyConflict {
		t.Fatalf("Reverse() error = %v, want idempotency conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCostRepositoryReverseRejectsConcurrentConflictForAnotherOriginal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &extraCostRepository{db: db}
	createdAt := time.Date(2026, 9, 18, 9, 10, 11, 0, time.UTC)
	adjustment := service.ExtraCostEntry{CostDate: "2026-09-18", Notes: "correct entry", CreatedAt: createdAt, IdempotencyKey: "shared-key"}

	mock.ExpectBegin()
	mock.ExpectQuery(`WHERE id = \$1 FOR UPDATE`).WithArgs(int64(42)).
		WillReturnRows(extraCostRows().AddRow(42, "2026-08-01", 8.0, service.ExtraCostCategoryAccount, "purchase", nil, createdAt, nil, "original-key", service.ExtraCostRuleVersion))
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(adjustment.IdempotencyKey).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`WHERE reversal_of = \$1 LIMIT 1`).WithArgs(int64(42)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO extra_cost_entries").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`WHERE idempotency_key = \$1`).WithArgs(adjustment.IdempotencyKey).
		WillReturnRows(extraCostRows().AddRow(99, "2026-09-18", -4.0, service.ExtraCostCategoryAdjust, adjustment.Notes, nil, createdAt, 77, adjustment.IdempotencyKey, service.ExtraCostRuleVersion))
	mock.ExpectRollback()

	_, err = repo.Reverse(context.Background(), 42, adjustment)
	if err != service.ErrExtraCostIdempotencyConflict {
		t.Fatalf("Reverse() error = %v, want idempotency conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
