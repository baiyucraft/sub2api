package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type pluginRuntimeStateStore struct {
	db        *sql.DB
	encryptor service.SecretEncryptor
}

var _ service.PluginStateStore = (*pluginRuntimeStateStore)(nil)

func NewPluginRuntimeStateStore(db *sql.DB, encryptor service.SecretEncryptor) service.PluginStateStore {
	return &pluginRuntimeStateStore{db: db, encryptor: encryptor}
}

func (s *pluginRuntimeStateStore) StateGet(ctx context.Context, pluginKey, namespace, key string) (service.PluginStateRecord, error) {
	if err := validatePluginStateSlot(pluginKey, namespace, key); err != nil {
		return service.PluginStateRecord{}, err
	}
	record, err := s.scanState(s.db.QueryRowContext(ctx, `
		SELECT key, value_encrypted, version, deleted
		FROM sub2api_plugin_runtime_state
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3
	`, pluginKey, namespace, key))
	if errors.Is(err, sql.ErrNoRows) {
		return service.PluginStateRecord{Key: key}, nil
	}
	return record, err
}

func (s *pluginRuntimeStateStore) StateCAS(ctx context.Context, pluginKey, namespace, key string, mutation service.PluginStateMutation) (service.PluginStateRecord, error) {
	return s.mutate(ctx, pluginKey, namespace, key, mutation, false)
}

func (s *pluginRuntimeStateStore) StateDelete(ctx context.Context, pluginKey, namespace, key string, mutation service.PluginStateMutation) (service.PluginStateRecord, error) {
	return s.mutate(ctx, pluginKey, namespace, key, mutation, true)
}

func (s *pluginRuntimeStateStore) mutate(ctx context.Context, pluginKey, namespace, key string, mutation service.PluginStateMutation, deleting bool) (service.PluginStateRecord, error) {
	if err := validatePluginStateSlot(pluginKey, namespace, key); err != nil {
		return service.PluginStateRecord{}, err
	}
	if mutation.ExpectedVersion < 0 || (deleting && mutation.ExpectedVersion == 0) {
		return service.PluginStateRecord{}, service.ErrPluginStateConflict
	}
	var encrypted string
	if !deleting {
		if s.encryptor == nil {
			return service.PluginStateRecord{}, errors.New("plugin state encryptor unavailable")
		}
		var err error
		encrypted, err = s.encryptor.Encrypt(string(mutation.Value))
		if err != nil {
			return service.PluginStateRecord{}, fmt.Errorf("encrypt plugin state: %w", err)
		}
	}
	tx, err := s.lockSlot(ctx, pluginKey, namespace, key)
	if err != nil {
		return service.PluginStateRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkPluginStateLease(ctx, tx, pluginKey, namespace, key, mutation.Lease); err != nil {
		return service.PluginStateRecord{}, err
	}

	var row *sql.Row
	switch {
	case deleting:
		row = tx.QueryRowContext(ctx, `
			UPDATE sub2api_plugin_runtime_state
			SET value_encrypted = NULL, deleted = TRUE, version = version + 1, updated_at = clock_timestamp()
			WHERE plugin_key = $1 AND namespace = $2 AND key = $3 AND version = $4 AND NOT deleted
			RETURNING version
		`, pluginKey, namespace, key, mutation.ExpectedVersion)
	case mutation.ExpectedVersion == 0:
		row = tx.QueryRowContext(ctx, `
			INSERT INTO sub2api_plugin_runtime_state (plugin_key, namespace, key, value_encrypted, version)
			VALUES ($1, $2, $3, $4, 1)
			ON CONFLICT (plugin_key, namespace, key) DO NOTHING
			RETURNING version
		`, pluginKey, namespace, key, encrypted)
	default:
		row = tx.QueryRowContext(ctx, `
			UPDATE sub2api_plugin_runtime_state
			SET value_encrypted = $5, deleted = FALSE, version = version + 1, updated_at = clock_timestamp()
			WHERE plugin_key = $1 AND namespace = $2 AND key = $3 AND version = $4
			RETURNING version
		`, pluginKey, namespace, key, mutation.ExpectedVersion, encrypted)
	}
	record := service.PluginStateRecord{Key: key, Found: !deleting}
	if err := row.Scan(&record.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.PluginStateRecord{}, service.ErrPluginStateConflict
		}
		return service.PluginStateRecord{}, fmt.Errorf("mutate plugin state: %w", err)
	}
	// A state-table lock or trigger may have delayed the write beyond expiry.
	// The slot mutex still excludes any acquire/renew/release until commit.
	if err := checkPluginStateLease(ctx, tx, pluginKey, namespace, key, mutation.Lease); err != nil {
		return service.PluginStateRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.PluginStateRecord{}, fmt.Errorf("commit plugin state: %w", err)
	}
	if !deleting {
		record.Value = append([]byte{}, mutation.Value...)
	}
	return record, nil
}

func (s *pluginRuntimeStateStore) StateList(ctx context.Context, pluginKey, namespace, keyPrefix, afterKey string, limit int) ([]service.PluginStateRecord, string, error) {
	if err := validatePluginStateSlot(pluginKey, namespace, "list"); err != nil {
		return nil, "", err
	}
	if err := validatePluginStateText(keyPrefix, 512, true); err != nil {
		return nil, "", fmt.Errorf("plugin state prefix: %w", err)
	}
	if err := validatePluginStateText(afterKey, 512, true); err != nil {
		return nil, "", fmt.Errorf("plugin state cursor: %w", err)
	}
	if limit <= 0 {
		limit = service.PluginStateDefaultListLimit
	}
	if limit > service.PluginStateMaxListLimit {
		limit = service.PluginStateMaxListLimit
	}
	// starts_with treats %, _ and backslash literally. C collation makes the
	// exclusive cursor and ordering agree even for non-ASCII keys.
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value_encrypted, version, deleted
		FROM sub2api_plugin_runtime_state
		WHERE plugin_key = $1 AND namespace = $2 AND NOT deleted
		  AND starts_with(key, $3) AND key > $4 COLLATE "C"
		ORDER BY key COLLATE "C" LIMIT $5
	`, pluginKey, namespace, keyPrefix, afterKey, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list plugin state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	records := make([]service.PluginStateRecord, 0, limit)
	next := ""
	for rows.Next() {
		if len(records) == limit {
			next = records[len(records)-1].Key
			break
		}
		record, err := s.scanState(rows)
		if err != nil {
			return nil, "", err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read plugin state list: %w", err)
	}
	return records, next, nil
}

func (s *pluginRuntimeStateStore) scanState(row interface{ Scan(...any) error }) (service.PluginStateRecord, error) {
	var record service.PluginStateRecord
	var encrypted sql.NullString
	var deleted bool
	if err := row.Scan(&record.Key, &encrypted, &record.Version, &deleted); err != nil {
		return service.PluginStateRecord{}, fmt.Errorf("read plugin state: %w", err)
	}
	if deleted {
		return record, nil
	}
	if s.encryptor == nil || !encrypted.Valid {
		return service.PluginStateRecord{}, errors.New("plugin state ciphertext or encryptor unavailable")
	}
	plain, err := s.encryptor.Decrypt(encrypted.String)
	if err != nil {
		return service.PluginStateRecord{}, fmt.Errorf("decrypt plugin state: %w", err)
	}
	record.Value, record.Found = []byte(plain), true
	return record, nil
}

func (s *pluginRuntimeStateStore) LeaseAcquire(ctx context.Context, pluginKey, namespace, key string, ttl time.Duration) (service.PluginLease, error) {
	if err := validatePluginLeaseTTL(ttl); err != nil {
		return service.PluginLease{}, err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return service.PluginLease{}, fmt.Errorf("generate plugin lease owner: %w", err)
	}
	tx, err := s.lockSlot(ctx, pluginKey, namespace, key)
	if err != nil {
		return service.PluginLease{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var lease service.PluginLease
	err = tx.QueryRowContext(ctx, `
		UPDATE sub2api_plugin_runtime_leases
		SET owner_token = $4, fence = fence + 1,
		    expires_at = clock_timestamp() + $5::bigint * interval '1 microsecond'
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3
		  AND (owner_token = '' OR expires_at <= clock_timestamp())
		RETURNING owner_token, fence, expires_at
	`, pluginKey, namespace, key, hex.EncodeToString(token[:]), ttl.Microseconds()).Scan(&lease.Owner, &lease.Fence, &lease.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return service.PluginLease{}, service.ErrPluginStateConflict
	}
	if err != nil {
		return service.PluginLease{}, fmt.Errorf("acquire plugin lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return service.PluginLease{}, fmt.Errorf("commit plugin lease: %w", err)
	}
	return lease, nil
}

func (s *pluginRuntimeStateStore) LeaseRenew(ctx context.Context, pluginKey, namespace, key string, lease service.PluginLease, ttl time.Duration) (service.PluginLease, error) {
	if err := validatePluginLeaseTTL(ttl); err != nil {
		return service.PluginLease{}, err
	}
	if lease.Owner == "" || lease.Fence <= 0 {
		return service.PluginLease{}, service.ErrPluginLeaseLost
	}
	tx, err := s.lockSlot(ctx, pluginKey, namespace, key)
	if err != nil {
		return service.PluginLease{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var renewed service.PluginLease
	err = tx.QueryRowContext(ctx, `
		UPDATE sub2api_plugin_runtime_leases
		SET expires_at = clock_timestamp() + $6::bigint * interval '1 microsecond'
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3
		  AND owner_token = $4 AND fence = $5 AND expires_at > clock_timestamp()
		RETURNING owner_token, fence, expires_at
	`, pluginKey, namespace, key, lease.Owner, lease.Fence, ttl.Microseconds()).Scan(&renewed.Owner, &renewed.Fence, &renewed.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return service.PluginLease{}, service.ErrPluginLeaseLost
	}
	if err != nil {
		return service.PluginLease{}, fmt.Errorf("renew plugin lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return service.PluginLease{}, fmt.Errorf("commit plugin lease renewal: %w", err)
	}
	return renewed, nil
}

func (s *pluginRuntimeStateStore) LeaseRelease(ctx context.Context, pluginKey, namespace, key string, lease service.PluginLease) error {
	if lease.Owner == "" || lease.Fence <= 0 {
		return service.ErrPluginLeaseLost
	}
	tx, err := s.lockSlot(ctx, pluginKey, namespace, key)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var fence int64
	err = tx.QueryRowContext(ctx, `
		UPDATE sub2api_plugin_runtime_leases
		SET owner_token = '', expires_at = '-infinity'
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3
		  AND owner_token = $4 AND fence = $5 AND expires_at > clock_timestamp()
		RETURNING fence
	`, pluginKey, namespace, key, lease.Owner, lease.Fence).Scan(&fence)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrPluginLeaseLost
	}
	if err != nil {
		return fmt.Errorf("release plugin lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin lease release: %w", err)
	}
	return nil
}

// All mutations, including lease-free CAS, use this permanent row mutex.
// An absent lease row must first be created so acquire cannot race a CAS on a
// previously unused slot. READ COMMITTED sees any holder that we waited for.
func (s *pluginRuntimeStateStore) lockSlot(ctx context.Context, pluginKey, namespace, key string) (*sql.Tx, error) {
	if err := validatePluginStateSlot(pluginKey, namespace, key); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin plugin slot transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sub2api_plugin_runtime_leases (plugin_key, namespace, key)
		VALUES ($1, $2, $3) ON CONFLICT (plugin_key, namespace, key) DO NOTHING
	`, pluginKey, namespace, key); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("ensure plugin slot mutex: %w", err)
	}
	var fence int64
	if err := tx.QueryRowContext(ctx, `
		SELECT fence FROM sub2api_plugin_runtime_leases
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3 FOR UPDATE
	`, pluginKey, namespace, key).Scan(&fence); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("lock plugin slot: %w", err)
	}
	return tx, nil
}

func checkPluginStateLease(ctx context.Context, tx *sql.Tx, pluginKey, namespace, key string, lease *service.PluginLease) error {
	owner, fence := "", int64(0)
	if lease != nil {
		if lease.Owner == "" || lease.Fence <= 0 {
			return service.ErrPluginLeaseLost
		}
		owner, fence = lease.Owner, lease.Fence
	}
	var active, matches bool
	err := tx.QueryRowContext(ctx, `
		SELECT owner_token <> '' AND expires_at > clock_timestamp(), owner_token = $4 AND fence = $5
		FROM sub2api_plugin_runtime_leases
		WHERE plugin_key = $1 AND namespace = $2 AND key = $3
	`, pluginKey, namespace, key, owner, fence).Scan(&active, &matches)
	if err != nil {
		return fmt.Errorf("check plugin lease: %w", err)
	}
	if lease != nil && (!active || !matches) {
		return service.ErrPluginLeaseLost
	}
	if lease == nil && active {
		return service.ErrPluginStateConflict
	}
	return nil
}

func validatePluginStateSlot(pluginKey, namespace, key string) error {
	for _, part := range []struct {
		name  string
		value string
		max   int
	}{
		{"plugin key", pluginKey, 160},
		{"namespace", namespace, 128},
		{"key", key, 512},
	} {
		if err := validatePluginStateText(part.value, part.max, false); err != nil {
			return fmt.Errorf("plugin state %s: %w", part.name, err)
		}
	}
	return nil
}

func validatePluginStateText(value string, max int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > max {
		return errors.New("invalid text or length")
	}
	return nil
}

func validatePluginLeaseTTL(ttl time.Duration) error {
	if ttl < time.Microsecond {
		return errors.New("plugin lease TTL must be at least one microsecond")
	}
	return nil
}
