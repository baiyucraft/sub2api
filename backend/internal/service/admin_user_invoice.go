package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// UserInvoiceAdminService is an optional, additive capability of AdminService.
// Invoice annotations are independent of all funds and entitlement ledgers.
type UserInvoiceAdminService interface {
	ListUserInvoices(context.Context, int64, int, int) (*UserInvoicePage, error)
	CreateUserInvoice(context.Context, int64, int64, UserInvoiceInput) (*UserInvoiceAnnotation, error)
	UpdateUserInvoice(context.Context, int64, int64, int64, UserInvoiceInput) (*UserInvoiceAnnotation, error)
	VoidUserInvoice(context.Context, int64, int64, int64, string) (*UserInvoiceAnnotation, error)
}

var _ UserInvoiceAdminService = (*adminServiceImpl)(nil)

type UserInvoiceInput struct {
	Amount            float64    `json:"amount"`
	InvoiceDate       string     `json:"invoice_date"`
	InvoiceNumber     string     `json:"invoice_number"`
	Notes             string     `json:"notes"`
	OperationID       string     `json:"operation_id"`
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
}

// UnmarshalJSON validates the original JSON number before float64 conversion.
// Otherwise a ninth decimal such as 0.10000000000000001 can silently round to
// 0.1 and evade the amount precision contract.
func (input *UserInvoiceInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Amount            json.RawMessage `json:"amount"`
		InvoiceDate       string          `json:"invoice_date"`
		InvoiceNumber     string          `json:"invoice_number"`
		Notes             string          `json:"notes"`
		OperationID       string          `json:"operation_id"`
		ExpectedUpdatedAt *time.Time      `json:"expected_updated_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	number := strings.TrimSpace(string(wire.Amount))
	if number == "" || len(number) > 128 || number[0] == '"' {
		return invalidInvoice("Amount must be a number")
	}
	// Bound exponent work before big.Rat parses an attacker-controlled number.
	if at := strings.IndexAny(number, "eE"); at >= 0 {
		exponent, err := strconv.ParseInt(number[at+1:], 10, 16)
		if err != nil || exponent < -128 || exponent > 128 {
			return invalidInvoice("Amount is out of range")
		}
	}
	exact, ok := new(big.Rat).SetString(number)
	if !ok || exact.Sign() <= 0 || exact.Cmp(new(big.Rat).SetInt64(1000000000000)) >= 0 {
		return invalidInvoice("Amount must be positive and less than 1000000000000")
	}
	if !new(big.Rat).Mul(exact, new(big.Rat).SetInt64(100000000)).IsInt() {
		return invalidInvoice("Amount may have at most 8 decimal places")
	}
	amount, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return invalidInvoice("Amount is out of range")
	}
	roundtrip, ok := new(big.Rat).SetString(strconv.FormatFloat(amount, 'f', -1, 64))
	if !ok || exact.Cmp(roundtrip) != 0 {
		return invalidInvoice("Amount exceeds the supported numeric precision")
	}
	*input = UserInvoiceInput{Amount: amount, InvoiceDate: wire.InvoiceDate,
		InvoiceNumber: wire.InvoiceNumber, Notes: wire.Notes, OperationID: wire.OperationID,
		ExpectedUpdatedAt: wire.ExpectedUpdatedAt}
	return nil
}

type UserInvoiceAnnotation struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	Amount        float64    `json:"amount"`
	InvoiceDate   string     `json:"invoice_date"`
	InvoiceNumber string     `json:"invoice_number"`
	Notes         string     `json:"notes"`
	Status        string     `json:"status"`
	CreatedBy     int64      `json:"created_by"`
	UpdatedBy     int64      `json:"updated_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	VoidReason    string     `json:"void_reason"`
	VoidedBy      *int64     `json:"voided_by"`
	VoidedAt      *time.Time `json:"voided_at"`
}

type UserInvoicePage struct {
	Items         []UserInvoiceAnnotation `json:"items"`
	Total         int64                   `json:"total"`
	Page          int                     `json:"page"`
	PageSize      int                     `json:"page_size"`
	Pages         int                     `json:"pages"`
	TotalInvoiced float64                 `json:"total_invoiced"`
}

const userInvoiceColumns = `id,user_id,amount,invoice_date::text,invoice_number,notes,status,
	created_by,updated_by,created_at,updated_at,void_reason,voided_by,voided_at`

var invoiceOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func invalidInvoice(message string) error {
	return infraerrors.BadRequest("INVALID_USER_INVOICE", message)
}

func invoiceDBError(err error) error {
	return infraerrors.InternalServer("USER_INVOICE_STORE_FAILED", "Unable to save or load invoice annotations").WithCause(err)
}

func normalizeUserInvoiceInput(input UserInvoiceInput, create bool) (UserInvoiceInput, string, error) {
	if math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) || input.Amount <= 0 || input.Amount >= 1e12 {
		return input, "", invalidInvoice("Amount must be positive and less than 1000000000000")
	}
	amount := strconv.FormatFloat(input.Amount, 'f', -1, 64)
	if point := strings.IndexByte(amount, '.'); point >= 0 && len(amount)-point-1 > 8 {
		return input, "", invalidInvoice("Amount may have at most 8 decimal places")
	}
	date, err := time.Parse("2006-01-02", input.InvoiceDate)
	if err != nil || len(input.InvoiceDate) != 10 || date.Year() < 1 || date.Format("2006-01-02") != input.InvoiceDate {
		return input, "", invalidInvoice("Invoice date must be a valid YYYY-MM-DD date")
	}
	input.InvoiceNumber = strings.TrimSpace(input.InvoiceNumber)
	input.Notes = strings.TrimSpace(input.Notes)
	if !utf8.ValidString(input.InvoiceNumber) || utf8.RuneCountInString(input.InvoiceNumber) > 100 || strings.ContainsRune(input.InvoiceNumber, 0) {
		return input, "", invalidInvoice("Invoice number may have at most 100 characters")
	}
	if !utf8.ValidString(input.Notes) || utf8.RuneCountInString(input.Notes) > 1000 || strings.ContainsRune(input.Notes, 0) {
		return input, "", invalidInvoice("Notes may have at most 1000 characters")
	}
	if create && !invoiceOperationIDPattern.MatchString(input.OperationID) {
		return input, "", invalidInvoice("operation_id must contain 8 to 128 ASCII letters, digits, hyphens or underscores")
	}
	if !create && (input.ExpectedUpdatedAt == nil || input.ExpectedUpdatedAt.IsZero() || input.ExpectedUpdatedAt.Year() < 1 || input.ExpectedUpdatedAt.Year() > 9999) {
		return input, "", invalidInvoice("expected_updated_at must contain the original record update time")
	}
	return input, amount, nil
}

func invoiceRequestHash(input UserInvoiceInput, amount string) string {
	// JSON array framing prevents ambiguous concatenation; operation ID and actor
	// are scoped separately by the unique constraint, and cannot alter the payload.
	data, _ := json.Marshal([]string{amount, input.InvoiceDate, input.InvoiceNumber, input.Notes})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func scanUserInvoice(rows *sql.Rows) (*UserInvoiceAnnotation, error) {
	record := &UserInvoiceAnnotation{}
	err := rows.Scan(&record.ID, &record.UserID, &record.Amount, &record.InvoiceDate,
		&record.InvoiceNumber, &record.Notes, &record.Status, &record.CreatedBy, &record.UpdatedBy,
		&record.CreatedAt, &record.UpdatedAt, &record.VoidReason, &record.VoidedBy, &record.VoidedAt)
	return record, err
}

func queryUserInvoice(ctx context.Context, tx *dbent.Tx, query string, args ...any) (*UserInvoiceAnnotation, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return nil, invoiceDBError(err)
		}
		return nil, infraerrors.NotFound("USER_INVOICE_NOT_FOUND", "Invoice annotation not found")
	}
	record, err := scanUserInvoice(rows)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	if rows.Next() {
		return nil, invoiceDBError(errors.New("unexpected duplicate invoice record"))
	}
	if err = rows.Err(); err != nil {
		return nil, invoiceDBError(err)
	}
	return record, nil
}

func (s *adminServiceImpl) beginInvoiceTransaction(ctx context.Context, readOnly bool) (*dbent.Tx, error) {
	if s == nil || s.entClient == nil {
		return nil, invoiceDBError(errors.New("invoice store is unavailable"))
	}
	opts := &sql.TxOptions{}
	if readOnly {
		opts.Isolation, opts.ReadOnly = sql.LevelRepeatableRead, true
	}
	tx, err := s.entClient.BeginTx(ctx, opts)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	return tx, nil
}

func checkInvoiceUser(ctx context.Context, tx *dbent.Tx, userID int64, write bool) error {
	if userID <= 0 {
		return invalidInvoice("Invalid user ID")
	}
	query := `SELECT id FROM users WHERE id=$1`
	if write {
		// The user lock serializes creates, retries, edits and voids, and prevents
		// committing a new annotation concurrently with the user's soft deletion.
		query += ` AND deleted_at IS NULL FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query, userID)
	if err != nil {
		return invoiceDBError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return invoiceDBError(err)
		}
		return ErrUserNotFound
	}
	var found int64
	if err = rows.Scan(&found); err != nil {
		return invoiceDBError(err)
	}
	if rows.Next() {
		return invoiceDBError(errors.New("unexpected duplicate user record"))
	}
	if err = rows.Err(); err != nil {
		return invoiceDBError(err)
	}
	return nil
}

func (s *adminServiceImpl) ListUserInvoices(ctx context.Context, userID int64, page, pageSize int) (*UserInvoicePage, error) {
	if page < 1 || page > 1000000 || pageSize < 1 || pageSize > 100 {
		return nil, invalidInvoice("Invalid invoice pagination")
	}
	tx, err := s.beginInvoiceTransaction(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkInvoiceUser(ctx, tx, userID, false); err != nil {
		return nil, err
	}
	out := &UserInvoicePage{Items: []UserInvoiceAnnotation{}, Page: page, PageSize: pageSize}
	rows, err := tx.QueryContext(ctx, `SELECT COUNT(*),COALESCE(SUM(amount) FILTER (WHERE status='active'),0)
		FROM user_invoice_annotations WHERE user_id=$1`, userID)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	if !rows.Next() {
		err = rows.Err()
		_ = rows.Close()
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, invoiceDBError(err)
	}
	err = rows.Scan(&out.Total, &out.TotalInvoiced)
	if err == nil && rows.Next() {
		err = errors.New("unexpected duplicate invoice summary")
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, invoiceDBError(err)
	}
	if rowsErr != nil {
		return nil, invoiceDBError(rowsErr)
	}
	if closeErr != nil {
		return nil, invoiceDBError(closeErr)
	}
	out.Pages = max(1, int((out.Total+int64(pageSize)-1)/int64(pageSize)))
	rows, err = tx.QueryContext(ctx, `SELECT `+userInvoiceColumns+` FROM user_invoice_annotations
		WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	for rows.Next() {
		record, scanErr := scanUserInvoice(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, invoiceDBError(scanErr)
		}
		out.Items = append(out.Items, *record)
	}
	err, closeErr = rows.Err(), rows.Close()
	if err != nil {
		return nil, invoiceDBError(err)
	}
	if closeErr != nil {
		return nil, invoiceDBError(closeErr)
	}
	if err = tx.Commit(); err != nil {
		return nil, invoiceDBError(err)
	}
	return out, nil
}

func appendInvoiceAudit(ctx context.Context, tx *dbent.Tx, actorID int64, action string, before, after *UserInvoiceAnnotation) error {
	var beforeJSON any
	if before != nil {
		data, err := json.Marshal(before)
		if err != nil {
			return invoiceDBError(err)
		}
		beforeJSON = string(data)
	}
	data, err := json.Marshal(after)
	if err != nil {
		return invoiceDBError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_invoice_annotation_events
		(invoice_id,user_id,action,actor_id,before_value,after_value) VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb)`,
		after.ID, after.UserID, action, actorID, beforeJSON, string(data))
	if err != nil {
		return invoiceDBError(err)
	}
	return nil
}

func commitUserInvoice(tx *dbent.Tx, record *UserInvoiceAnnotation) (*UserInvoiceAnnotation, error) {
	if err := tx.Commit(); err != nil {
		return nil, invoiceDBError(err)
	}
	return record, nil
}

func (s *adminServiceImpl) CreateUserInvoice(ctx context.Context, userID, actorID int64, input UserInvoiceInput) (*UserInvoiceAnnotation, error) {
	input, amount, err := normalizeUserInvoiceInput(input, true)
	if err != nil {
		return nil, err
	}
	if actorID <= 0 {
		return nil, infraerrors.Forbidden("USER_INVOICE_ACTOR_REQUIRED", "Authenticated administrator required")
	}
	tx, err := s.beginInvoiceTransaction(ctx, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkInvoiceUser(ctx, tx, userID, true); err != nil {
		return nil, err
	}
	hash := invoiceRequestHash(input, amount)
	rows, err := tx.QueryContext(ctx, `SELECT id,request_hash FROM user_invoice_annotations
		WHERE user_id=$1 AND created_by=$2 AND operation_id=$3`, userID, actorID, input.OperationID)
	if err != nil {
		return nil, invoiceDBError(err)
	}
	var existingID int64
	var existingHash string
	if rows.Next() {
		err = rows.Scan(&existingID, &existingHash)
	}
	rowsErr, closeErr := rows.Err(), rows.Close()
	if err != nil {
		return nil, invoiceDBError(err)
	}
	if rowsErr != nil {
		return nil, invoiceDBError(rowsErr)
	}
	if closeErr != nil {
		return nil, invoiceDBError(closeErr)
	}
	if existingID > 0 {
		if existingHash != hash {
			return nil, infraerrors.Conflict("USER_INVOICE_OPERATION_CONFLICT", "operation_id was already used with different invoice details")
		}
		record, err := queryUserInvoice(ctx, tx, `SELECT `+userInvoiceColumns+` FROM user_invoice_annotations WHERE user_id=$1 AND id=$2`, userID, existingID)
		if err != nil {
			return nil, err
		}
		return commitUserInvoice(tx, record)
	}
	record, err := queryUserInvoice(ctx, tx, `INSERT INTO user_invoice_annotations
		(user_id,amount,invoice_date,invoice_number,notes,operation_id,request_hash,created_by,updated_by)
		VALUES ($1,$2,$3::date,$4,$5,$6,$7,$8,$8) RETURNING `+userInvoiceColumns,
		userID, amount, input.InvoiceDate, input.InvoiceNumber, input.Notes, input.OperationID, hash, actorID)
	if err != nil {
		return nil, err
	}
	if err = appendInvoiceAudit(ctx, tx, actorID, "create", nil, record); err != nil {
		return nil, err
	}
	return commitUserInvoice(tx, record)
}

func (s *adminServiceImpl) UpdateUserInvoice(ctx context.Context, userID, invoiceID, actorID int64, input UserInvoiceInput) (*UserInvoiceAnnotation, error) {
	input, amount, err := normalizeUserInvoiceInput(input, false)
	if err != nil {
		return nil, err
	}
	return s.mutateUserInvoice(ctx, userID, invoiceID, actorID, func(tx *dbent.Tx, before *UserInvoiceAnnotation) (*UserInvoiceAnnotation, error) {
		if before.Status != "active" {
			return nil, infraerrors.Conflict("USER_INVOICE_VOIDED", "Voided invoice annotations cannot be edited")
		}
		if before.Amount == input.Amount && before.InvoiceDate == input.InvoiceDate && before.InvoiceNumber == input.InvoiceNumber && before.Notes == input.Notes {
			return before, nil
		}
		if !before.UpdatedAt.Equal(*input.ExpectedUpdatedAt) {
			return nil, infraerrors.Conflict("USER_INVOICE_STALE", "Invoice annotation changed; reload it before editing")
		}
		after, err := queryUserInvoice(ctx, tx, `UPDATE user_invoice_annotations SET amount=$3,invoice_date=$4::date,
			invoice_number=$5,notes=$6,updated_by=$7,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond')
			WHERE user_id=$1 AND id=$2 AND status='active' RETURNING `+userInvoiceColumns,
			userID, invoiceID, amount, input.InvoiceDate, input.InvoiceNumber, input.Notes, actorID)
		if err != nil {
			return nil, err
		}
		if err = appendInvoiceAudit(ctx, tx, actorID, "update", before, after); err != nil {
			return nil, err
		}
		return after, nil
	})
}

func (s *adminServiceImpl) VoidUserInvoice(ctx context.Context, userID, invoiceID, actorID int64, reason string) (*UserInvoiceAnnotation, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 500 || strings.ContainsRune(reason, 0) {
		return nil, invalidInvoice("Void reason must contain 1 to 500 characters")
	}
	return s.mutateUserInvoice(ctx, userID, invoiceID, actorID, func(tx *dbent.Tx, before *UserInvoiceAnnotation) (*UserInvoiceAnnotation, error) {
		if before.Status == "voided" {
			if before.VoidReason == reason {
				return before, nil
			}
			return nil, infraerrors.Conflict("USER_INVOICE_VOIDED", "Invoice annotation was already voided")
		}
		after, err := queryUserInvoice(ctx, tx, `UPDATE user_invoice_annotations SET status='voided',void_reason=$3,
			voided_by=$4,voided_at=clock_timestamp(),updated_by=$4,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond')
			WHERE user_id=$1 AND id=$2 AND status='active' RETURNING `+userInvoiceColumns, userID, invoiceID, reason, actorID)
		if err != nil {
			return nil, err
		}
		if err = appendInvoiceAudit(ctx, tx, actorID, "void", before, after); err != nil {
			return nil, err
		}
		return after, nil
	})
}

func (s *adminServiceImpl) mutateUserInvoice(ctx context.Context, userID, invoiceID, actorID int64,
	mutate func(*dbent.Tx, *UserInvoiceAnnotation) (*UserInvoiceAnnotation, error)) (*UserInvoiceAnnotation, error) {
	if invoiceID <= 0 {
		return nil, invalidInvoice("Invalid invoice ID")
	}
	if actorID <= 0 {
		return nil, infraerrors.Forbidden("USER_INVOICE_ACTOR_REQUIRED", "Authenticated administrator required")
	}
	tx, err := s.beginInvoiceTransaction(ctx, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkInvoiceUser(ctx, tx, userID, true); err != nil {
		return nil, err
	}
	before, err := queryUserInvoice(ctx, tx, `SELECT `+userInvoiceColumns+` FROM user_invoice_annotations
		WHERE user_id=$1 AND id=$2 FOR UPDATE`, userID, invoiceID)
	if err != nil {
		return nil, err
	}
	after, err := mutate(tx, before)
	if err != nil {
		return nil, err
	}
	return commitUserInvoice(tx, after)
}
