package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func invoiceTestService(t *testing.T) (*adminServiceImpl, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = db.Close() })
	return &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}, mock
}

func validInvoiceInput() UserInvoiceInput {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	return UserInvoiceInput{Amount: 25.5, InvoiceDate: "2026-10-12", InvoiceNumber: "INV-1", Notes: "历史补录", OperationID: "invoice-operation-1", ExpectedUpdatedAt: &at}
}

func invoiceTestRows(status string, amount float64, reason string, updateTimes ...time.Time) *sqlmock.Rows {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	updatedAt := at
	if len(updateTimes) > 0 {
		updatedAt = updateTimes[0]
	}
	var voidedBy, voidedAt any
	if status == "voided" {
		voidedBy, voidedAt = int64(3), at
	}
	return sqlmock.NewRows(strings.Split(strings.ReplaceAll(strings.ReplaceAll(userInvoiceColumns, "\n", ""), "\t", ""), ",")).
		AddRow(int64(9), int64(7), amount, "2026-10-12", "INV-1", "历史补录", status,
			int64(3), int64(3), at, updatedAt, reason, voidedBy, voidedAt)
}

func expectInvoiceUser(mock sqlmock.Sqlmock, write bool, found bool) {
	query := `SELECT id FROM users WHERE id=\$1$`
	if write {
		query = `SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL FOR UPDATE`
	}
	rows := sqlmock.NewRows([]string{"id"})
	if found {
		rows.AddRow(7)
	}
	mock.ExpectQuery(query).WithArgs(int64(7)).WillReturnRows(rows)
}

type invoiceSnapshotMatches struct {
	status string
	amount float64
	reason string
}

func (m invoiceSnapshotMatches) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	var record UserInvoiceAnnotation
	return json.Unmarshal([]byte(text), &record) == nil && record.ID == 9 && record.UserID == 7 &&
		record.Amount == m.amount && record.Status == m.status && record.VoidReason == m.reason && record.UpdatedBy == 3
}

func TestUserInvoiceInputBoundaries(t *testing.T) {
	for _, amount := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1), 1e12, 0.000000001, 1.123456789} {
		input := validInvoiceInput()
		input.Amount = amount
		_, _, err := normalizeUserInvoiceInput(input, true)
		require.Error(t, err, "amount=%v", amount)
	}
	for _, amount := range []float64{0.00000001, 1.12345678, 999999999999} {
		input := validInvoiceInput()
		input.Amount = amount
		_, _, err := normalizeUserInvoiceInput(input, true)
		require.NoError(t, err, "amount=%v", amount)
	}
	for _, date := range []string{"2026-02-29", "2026-10-1", "0000-01-01", "2026-13-01", "2026-10-12T00:00:00Z"} {
		input := validInvoiceInput()
		input.InvoiceDate = date
		_, _, err := normalizeUserInvoiceInput(input, true)
		require.Error(t, err, date)
	}
	input := validInvoiceInput()
	input.InvoiceDate = "2024-02-29"
	input.InvoiceNumber = "  " + strings.Repeat("票", 100) + "  "
	input.Notes = strings.Repeat("注", 1000)
	_, _, err := normalizeUserInvoiceInput(input, true)
	require.NoError(t, err)
	input.InvoiceNumber += "票"
	_, _, err = normalizeUserInvoiceInput(input, true)
	require.Error(t, err)
	input = validInvoiceInput()
	input.Notes = strings.Repeat("注", 1001)
	_, _, err = normalizeUserInvoiceInput(input, true)
	require.Error(t, err)
	for _, operation := range []string{"", "short", "operation with spaces", strings.Repeat("a", 129)} {
		input = validInvoiceInput()
		input.OperationID = operation
		_, _, err = normalizeUserInvoiceInput(input, true)
		require.Error(t, err)
	}
}

func TestUserInvoiceJSONAmountRetainsOriginalPrecision(t *testing.T) {
	for _, number := range []string{"0.10000000000000001", "1.000000001", "1e999999999999999999", "100000000000.12345678", "null", `"25.5"`} {
		var input UserInvoiceInput
		err := json.Unmarshal([]byte(`{"amount":`+number+`}`), &input)
		require.Error(t, err, number)
	}
	for _, number := range []string{"0.1", "0.10000000000000000", "1e-8", "25.50", "1.12345678"} {
		var input UserInvoiceInput
		err := json.Unmarshal([]byte(`{"amount":`+number+`}`), &input)
		require.NoError(t, err, number)
	}
}

func TestUserInvoiceJSONPreservesExpectedUpdatedAt(t *testing.T) {
	var input UserInvoiceInput
	err := json.Unmarshal([]byte(`{"amount":25.5,"expected_updated_at":"2026-10-12T00:00:00.123456Z"}`), &input)
	require.NoError(t, err)
	require.NotNil(t, input.ExpectedUpdatedAt)
	require.Equal(t, "2026-10-12T00:00:00.123456Z", input.ExpectedUpdatedAt.Format(time.RFC3339Nano))
	err = json.Unmarshal([]byte(`{"amount":25.5,"expected_updated_at":"invalid"}`), &input)
	require.Error(t, err)
}

func TestUserInvoiceListUsesAllActiveRecordsAndAllowsDeletedUser(t *testing.T) {
	svc, mock := invoiceTestService(t)
	mock.ExpectBegin()
	// Reads deliberately have no deleted_at predicate: historical annotations
	// remain available after soft deletion without making that user writable.
	expectInvoiceUser(mock, false, true)
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\),COALESCE\(SUM\(amount\) FILTER \(WHERE status='active'\),0\).*WHERE user_id=\$1`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, 75.5))
	mock.ExpectQuery(`(?s)FROM user_invoice_annotations\s+WHERE user_id=\$1 ORDER BY created_at DESC,id DESC LIMIT \$2 OFFSET \$3`).
		WithArgs(int64(7), 1, 1).WillReturnRows(invoiceTestRows("voided", 10, "误填"))
	mock.ExpectCommit()
	page, err := svc.ListUserInvoices(context.Background(), 7, 2, 1)
	require.NoError(t, err)
	require.Equal(t, 75.5, page.TotalInvoiced)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, 3, page.Pages)
	require.Len(t, page.Items, 1)
	require.Equal(t, "voided", page.Items[0].Status)
}

func TestUserInvoiceListRejectsLateSummaryReadError(t *testing.T) {
	svc, mock := invoiceTestService(t)
	mock.ExpectBegin()
	expectInvoiceUser(mock, false, true)
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\),COALESCE\(SUM\(amount\).*FROM user_invoice_annotations`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, 75.5).
		AddRow(0, 0).RowError(1, errors.New("summary read interrupted")))
	mock.ExpectRollback()
	page, err := svc.ListUserInvoices(context.Background(), 7, 1, 10)
	require.Error(t, err)
	require.Nil(t, page)
}

func expectCreateInvoice(mock sqlmock.Sqlmock, input UserInvoiceInput) {
	mock.ExpectBegin()
	expectInvoiceUser(mock, true, true)
	mock.ExpectQuery(`(?s)SELECT id,request_hash.*WHERE user_id=\$1 AND created_by=\$2 AND operation_id=\$3`).
		WithArgs(int64(7), int64(3), input.OperationID).WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}))
	mock.ExpectQuery(`(?s)INSERT INTO user_invoice_annotations.*RETURNING`).
		WithArgs(int64(7), "25.5", "2026-10-12", "INV-1", "历史补录", input.OperationID, sqlmock.AnyArg(), int64(3)).
		WillReturnRows(invoiceTestRows("active", 25.5, ""))
}

func TestUserInvoiceCreateAuditsAtomicallyWithoutBalanceWrite(t *testing.T) {
	svc, mock := invoiceTestService(t)
	input := validInvoiceInput()
	expectCreateInvoice(mock, input)
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WithArgs(int64(9), int64(7), "create", int64(3), nil,
		invoiceSnapshotMatches{status: "active", amount: 25.5}).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	record, err := svc.CreateUserInvoice(context.Background(), 7, 3, input)
	require.NoError(t, err)
	require.Equal(t, int64(9), record.ID)
	// The strict SQL mock also rejects any balance/recharge/entitlement write.
}

func TestUserInvoiceCreateAuditFailureRollsBack(t *testing.T) {
	svc, mock := invoiceTestService(t)
	input := validInvoiceInput()
	expectCreateInvoice(mock, input)
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	record, err := svc.CreateUserInvoice(context.Background(), 7, 3, input)
	require.Error(t, err)
	require.Nil(t, record)
}

func TestUserInvoiceCreateRetryIsIdempotentAndRejectsDifferentPayload(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same payload", true: "different payload"}[conflict], func(t *testing.T) {
			svc, mock := invoiceTestService(t)
			input := validInvoiceInput()
			hash := invoiceRequestHash(input, "25.5")
			if conflict {
				input.Amount = 30
			}
			mock.ExpectBegin()
			expectInvoiceUser(mock, true, true)
			mock.ExpectQuery(`SELECT id,request_hash`).WithArgs(int64(7), int64(3), input.OperationID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}).AddRow(9, hash))
			if conflict {
				mock.ExpectRollback()
			} else {
				mock.ExpectQuery(`(?s)FROM user_invoice_annotations WHERE user_id=\$1 AND id=\$2`).
					WithArgs(int64(7), int64(9)).WillReturnRows(invoiceTestRows("active", 25.5, ""))
				mock.ExpectCommit()
			}
			record, err := svc.CreateUserInvoice(context.Background(), 7, 3, input)
			if conflict {
				code, _ := infraerrors.ToHTTP(err)
				require.Equal(t, 409, code)
				require.Nil(t, record)
			} else {
				require.NoError(t, err)
				require.Equal(t, int64(9), record.ID)
			}
		})
	}
}

func TestUserInvoiceDeletedUserCannotBeWritten(t *testing.T) {
	svc, mock := invoiceTestService(t)
	mock.ExpectBegin()
	expectInvoiceUser(mock, true, false)
	mock.ExpectRollback()
	_, err := svc.CreateUserInvoice(context.Background(), 7, 3, validInvoiceInput())
	require.ErrorIs(t, err, ErrUserNotFound)
}

func expectInvoiceMutation(mock sqlmock.Sqlmock, status string, amount float64, reason string, updateTimes ...time.Time) {
	mock.ExpectBegin()
	expectInvoiceUser(mock, true, true)
	mock.ExpectQuery(`(?s)FROM user_invoice_annotations\s+WHERE user_id=\$1 AND id=\$2 FOR UPDATE`).
		WithArgs(int64(7), int64(9)).WillReturnRows(invoiceTestRows(status, amount, reason, updateTimes...))
}

func TestUserInvoiceUpdateAuditsBeforeAndAfter(t *testing.T) {
	svc, mock := invoiceTestService(t)
	expectInvoiceMutation(mock, "active", 20, "")
	mock.ExpectQuery(`(?s)UPDATE user_invoice_annotations SET amount=\$3.*WHERE user_id=\$1 AND id=\$2 AND status='active'`).
		WithArgs(int64(7), int64(9), "25.5", "2026-10-12", "INV-1", "历史补录", int64(3)).
		WillReturnRows(invoiceTestRows("active", 25.5, ""))
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WithArgs(int64(9), int64(7), "update", int64(3),
		invoiceSnapshotMatches{status: "active", amount: 20}, invoiceSnapshotMatches{status: "active", amount: 25.5}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, validInvoiceInput())
	require.NoError(t, err)
}

func TestUserInvoiceUpdateSamePayloadDoesNotAuditTwice(t *testing.T) {
	svc, mock := invoiceTestService(t)
	expectInvoiceMutation(mock, "active", 25.5, "")
	mock.ExpectCommit()
	input := validInvoiceInput()
	oldTime := input.ExpectedUpdatedAt.Add(-time.Minute)
	input.ExpectedUpdatedAt = &oldTime
	_, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, input)
	require.NoError(t, err)
}

func TestUserInvoiceUpdateRejectsStaleFormWithoutWrite(t *testing.T) {
	svc, mock := invoiceTestService(t)
	input := validInvoiceInput()
	// Another administrator changed this record after the form was opened.
	latest := input.ExpectedUpdatedAt.Add(time.Second)
	expectInvoiceMutation(mock, "active", 30, "", latest)
	mock.ExpectRollback()
	record, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, input)
	code, status := infraerrors.ToHTTP(err)
	require.Equal(t, 409, code)
	require.Equal(t, "USER_INVOICE_STALE", status.Reason)
	require.Nil(t, record)
}

func TestUserInvoiceUpdateLostResponseRetryCannotOverwriteLaterEdit(t *testing.T) {
	svc, mock := invoiceTestService(t)
	input := validInvoiceInput()
	atA := input.ExpectedUpdatedAt.Add(time.Second)
	atB := atA.Add(time.Second)
	// A changes 20 -> 25.5, commits, then loses the response.
	expectInvoiceMutation(mock, "active", 20, "")
	mock.ExpectQuery(`(?s)UPDATE user_invoice_annotations SET amount=\$3.*WHERE user_id=\$1 AND id=\$2 AND status='active'`).
		WithArgs(int64(7), int64(9), "25.5", "2026-10-12", "INV-1", "历史补录", int64(3)).
		WillReturnRows(invoiceTestRows("active", 25.5, "", atA))
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WithArgs(int64(9), int64(7), "update", int64(3),
		invoiceSnapshotMatches{status: "active", amount: 20}, invoiceSnapshotMatches{status: "active", amount: 25.5}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, input)
	require.NoError(t, err)
	// B sees A's committed version and changes 25.5 -> 30.
	inputB := validInvoiceInput()
	inputB.Amount, inputB.ExpectedUpdatedAt = 30, &atA
	expectInvoiceMutation(mock, "active", 25.5, "", atA)
	mock.ExpectQuery(`(?s)UPDATE user_invoice_annotations SET amount=\$3.*WHERE user_id=\$1 AND id=\$2 AND status='active'`).
		WithArgs(int64(7), int64(9), "30", "2026-10-12", "INV-1", "历史补录", int64(3)).
		WillReturnRows(invoiceTestRows("active", 30, "", atB))
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WithArgs(int64(9), int64(7), "update", int64(3),
		invoiceSnapshotMatches{status: "active", amount: 25.5}, invoiceSnapshotMatches{status: "active", amount: 30}).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	_, err = svc.UpdateUserInvoice(context.Background(), 7, 9, 3, inputB)
	require.NoError(t, err)
	// A's original request retries and must leave B's newer version intact.
	expectInvoiceMutation(mock, "active", 30, "", atB)
	mock.ExpectRollback()
	_, err = svc.UpdateUserInvoice(context.Background(), 7, 9, 3, input)
	code, status := infraerrors.ToHTTP(err)
	require.Equal(t, 409, code)
	require.Equal(t, "USER_INVOICE_STALE", status.Reason)
}

func TestUserInvoiceUpdateRequiresExpectedUpdatedAt(t *testing.T) {
	svc := &adminServiceImpl{}
	zero := time.Time{}
	invalidYear := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, expected := range []*time.Time{nil, &zero, &invalidYear} {
		input := validInvoiceInput()
		input.ExpectedUpdatedAt = expected
		_, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, input)
		code, _ := infraerrors.ToHTTP(err)
		require.Equal(t, 400, code)
	}
}

func TestUserInvoiceUpdateRejectsVoidedRecord(t *testing.T) {
	svc, mock := invoiceTestService(t)
	expectInvoiceMutation(mock, "voided", 25.5, "误填")
	mock.ExpectRollback()
	_, err := svc.UpdateUserInvoice(context.Background(), 7, 9, 3, validInvoiceInput())
	code, _ := infraerrors.ToHTTP(err)
	require.Equal(t, 409, code)
}

func TestUserInvoiceCannotMutateAnotherUsersRecord(t *testing.T) {
	svc, mock := invoiceTestService(t)
	mock.ExpectBegin()
	expectInvoiceUser(mock, true, true)
	mock.ExpectQuery(`(?s)FROM user_invoice_annotations\s+WHERE user_id=\$1 AND id=\$2 FOR UPDATE`).
		WithArgs(int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	_, err := svc.VoidUserInvoice(context.Background(), 7, 9, 3, "误填")
	code, _ := infraerrors.ToHTTP(err)
	require.Equal(t, 404, code)
}

func TestUserInvoiceVoidAuditsBeforeAndAfter(t *testing.T) {
	svc, mock := invoiceTestService(t)
	expectInvoiceMutation(mock, "active", 25.5, "")
	mock.ExpectQuery(`(?s)UPDATE user_invoice_annotations SET status='voided'.*WHERE user_id=\$1 AND id=\$2 AND status='active'`).
		WithArgs(int64(7), int64(9), "误填", int64(3)).WillReturnRows(invoiceTestRows("voided", 25.5, "误填"))
	mock.ExpectExec(`INSERT INTO user_invoice_annotation_events`).WithArgs(int64(9), int64(7), "void", int64(3),
		invoiceSnapshotMatches{status: "active", amount: 25.5}, invoiceSnapshotMatches{status: "voided", amount: 25.5, reason: "误填"}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := svc.VoidUserInvoice(context.Background(), 7, 9, 3, "  误填  ")
	require.NoError(t, err)
}

func TestUserInvoiceVoidRetryAndConflictingReason(t *testing.T) {
	for _, reason := range []string{"误填", "另一个理由"} {
		t.Run(reason, func(t *testing.T) {
			svc, mock := invoiceTestService(t)
			expectInvoiceMutation(mock, "voided", 25.5, "误填")
			if reason == "误填" {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			_, err := svc.VoidUserInvoice(context.Background(), 7, 9, 3, reason)
			if reason == "误填" {
				require.NoError(t, err)
			} else {
				code, _ := infraerrors.ToHTTP(err)
				require.Equal(t, 409, code)
			}
		})
	}
}

func TestUserInvoiceRejectsMissingActorAndInvalidVoidReason(t *testing.T) {
	svc := &adminServiceImpl{}
	_, err := svc.CreateUserInvoice(context.Background(), 7, 0, validInvoiceInput())
	code, _ := infraerrors.ToHTTP(err)
	require.Equal(t, 403, code)
	for _, reason := range []string{"", "  ", strings.Repeat("a", 501), "a\x00b"} {
		_, err := svc.VoidUserInvoice(context.Background(), 7, 9, 3, reason)
		code, _ := infraerrors.ToHTTP(err)
		require.Equal(t, 400, code)
	}
}
