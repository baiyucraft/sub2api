package repository

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const pluginStateTestWhere = `WHERE plugin_key = $1 AND namespace = $2 AND key = $3`

func newPluginStateTestStore(t *testing.T) (*pluginRuntimeStateStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return NewPluginRuntimeStateStore(db, &AESEncryptor{key: bytes.Repeat([]byte{7}, 32)}).(*pluginRuntimeStateStore), mock
}

func expectPluginSlotLock(mock sqlmock.Sqlmock, pluginKey, namespace, key string) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO sub2api_plugin_runtime_leases (plugin_key, namespace, key) VALUES ($1, $2, $3) ON CONFLICT (plugin_key, namespace, key) DO NOTHING`)).
		WithArgs(pluginKey, namespace, key).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT fence FROM sub2api_plugin_runtime_leases `+pluginStateTestWhere+` FOR UPDATE`)).
		WithArgs(pluginKey, namespace, key).WillReturnRows(sqlmock.NewRows([]string{"fence"}).AddRow(1))
}

func expectPluginLeaseCheck(mock sqlmock.Sqlmock, pluginKey, namespace, key string, lease *service.PluginLease, active, matches bool) {
	owner, fence := "", int64(0)
	if lease != nil {
		owner, fence = lease.Owner, lease.Fence
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT owner_token <> '' AND expires_at > clock_timestamp(), owner_token = $4 AND fence = $5 FROM sub2api_plugin_runtime_leases `+pluginStateTestWhere)).
		WithArgs(pluginKey, namespace, key, owner, fence).
		WillReturnRows(sqlmock.NewRows([]string{"active", "matches"}).AddRow(active, matches))
}

type pluginStateEncryptedArg struct {
	encryptor service.SecretEncryptor
	plain     []byte
}

func (a pluginStateEncryptedArg) Match(value driver.Value) bool {
	ciphertext, ok := value.(string)
	if !ok || ciphertext == string(a.plain) {
		return false
	}
	plain, err := a.encryptor.Decrypt(ciphertext)
	return err == nil && bytes.Equal([]byte(plain), a.plain)
}

type pluginLeaseOwnerArg struct{ seen *string }

func (a pluginLeaseOwnerArg) Match(value driver.Value) bool {
	owner, ok := value.(string)
	if !ok {
		return false
	}
	decoded, err := hex.DecodeString(owner)
	if err != nil || len(decoded) != 32 {
		return false
	}
	*a.seen = owner
	return true
}

func TestPluginRuntimeStateGet(t *testing.T) {
	for _, kind := range []string{"binary", "empty", "tombstone", "missing", "corrupt", "query_error"} {
		t.Run(kind, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			value := []byte{0, 0xff, 's', 'e', 'c', 'r', 'e', 't'}
			if kind == "empty" {
				value = []byte{}
			}
			ciphertext, err := store.encryptor.Encrypt(string(value))
			require.NoError(t, err)
			rows := sqlmock.NewRows([]string{"key", "value_encrypted", "version", "deleted"})
			switch kind {
			case "tombstone":
				rows.AddRow("key", nil, 4, true)
				store.encryptor = nil
			case "binary", "empty":
				rows.AddRow("key", ciphertext, 3, false)
			case "corrupt":
				rows.AddRow("key", "not ciphertext", 3, false)
			}
			query := mock.ExpectQuery(`SELECT key, value_encrypted, version, deleted FROM sub2api_plugin_runtime_state `+regexp.QuoteMeta(pluginStateTestWhere)).
				WithArgs("plugin", "namespace", "key")
			if kind == "query_error" {
				query.WillReturnError(errors.New("read failed"))
			} else {
				query.WillReturnRows(rows)
			}
			record, err := store.StateGet(context.Background(), "plugin", "namespace", "key")
			if kind == "corrupt" || kind == "query_error" {
				require.Error(t, err)
				require.Empty(t, record)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "key", record.Key)
			switch kind {
			case "binary", "empty":
				require.True(t, record.Found)
				require.Equal(t, value, record.Value)
				require.EqualValues(t, 3, record.Version)
			case "tombstone":
				require.False(t, record.Found)
				require.Nil(t, record.Value)
				require.EqualValues(t, 4, record.Version)
			case "missing":
				require.False(t, record.Found)
				require.Zero(t, record.Version)
			}
		})
	}
}

func TestPluginRuntimeStateCASAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected int64
		deleting bool
		lease    *service.PluginLease
	}{
		{"create", 0, false, nil},
		{"update", 8, false, nil},
		{"restore_tombstone", 10, false, nil},
		{"delete", 9, true, nil},
		{"fenced_update", 11, false, &service.PluginLease{Owner: "opaque-owner", Fence: 4}},
		{"fenced_delete", 12, true, &service.PluginLease{Owner: "opaque-owner", Fence: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			value := []byte{0, 0xff, 'v'}
			expectPluginSlotLock(mock, "p", "ns", "k")
			expectPluginLeaseCheck(mock, "p", "ns", "k", tc.lease, tc.lease != nil, true)
			var query *sqlmock.ExpectedQuery
			switch {
			case tc.deleting:
				query = mock.ExpectQuery(`UPDATE sub2api_plugin_runtime_state SET value_encrypted = NULL, deleted = TRUE, version = version \+ 1, updated_at = clock_timestamp\(\) `+regexp.QuoteMeta(pluginStateTestWhere+` AND version = $4 AND NOT deleted RETURNING version`)).
					WithArgs("p", "ns", "k", tc.expected)
			case tc.expected == 0:
				query = mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO sub2api_plugin_runtime_state (plugin_key, namespace, key, value_encrypted, version) VALUES ($1, $2, $3, $4, 1) ON CONFLICT (plugin_key, namespace, key) DO NOTHING RETURNING version`)).
					WithArgs("p", "ns", "k", pluginStateEncryptedArg{store.encryptor, value})
			default:
				query = mock.ExpectQuery(regexp.QuoteMeta(`UPDATE sub2api_plugin_runtime_state SET value_encrypted = $5, deleted = FALSE, version = version + 1, updated_at = clock_timestamp() `+pluginStateTestWhere+` AND version = $4 RETURNING version`)).
					WithArgs("p", "ns", "k", tc.expected, pluginStateEncryptedArg{store.encryptor, value})
			}
			query.WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(tc.expected + 1))
			expectPluginLeaseCheck(mock, "p", "ns", "k", tc.lease, tc.lease != nil, true)
			mock.ExpectCommit()
			mutation := service.PluginStateMutation{Value: value, ExpectedVersion: tc.expected, Lease: tc.lease}
			var record service.PluginStateRecord
			var err error
			if tc.deleting {
				record, err = store.StateDelete(context.Background(), "p", "ns", "k", mutation)
			} else {
				record, err = store.StateCAS(context.Background(), "p", "ns", "k", mutation)
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected+1, record.Version)
			require.Equal(t, !tc.deleting, record.Found)
			if tc.deleting {
				require.Nil(t, record.Value)
			} else {
				require.Equal(t, value, record.Value)
				value[0] = 100
				require.Zero(t, record.Value[0], "returned values must not alias caller memory")
			}
		})
	}
}

func TestPluginRuntimeStateLeaseExclusion(t *testing.T) {
	lease := &service.PluginLease{Owner: "holder", Fence: 4, ExpiresAt: time.Now().Add(time.Hour)}
	for _, deleting := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			lease   *service.PluginLease
			active  bool
			matches bool
			want    error
		}{
			{"lease_free_during_active_lease", nil, true, false, service.ErrPluginStateConflict},
			{"stale_owner_or_fence", lease, true, false, service.ErrPluginLeaseLost},
			{"expired_despite_client_expiry", lease, false, true, service.ErrPluginLeaseLost},
			{"released", lease, false, false, service.ErrPluginLeaseLost},
		} {
			t.Run(tc.name+map[bool]string{false: "/cas", true: "/delete"}[deleting], func(t *testing.T) {
				store, mock := newPluginStateTestStore(t)
				expectPluginSlotLock(mock, "p", "n", "k")
				expectPluginLeaseCheck(mock, "p", "n", "k", tc.lease, tc.active, tc.matches)
				mock.ExpectRollback()
				mutation := service.PluginStateMutation{Value: []byte("secret"), ExpectedVersion: 1, Lease: tc.lease}
				var err error
				if deleting {
					_, err = store.StateDelete(context.Background(), "p", "n", "k", mutation)
				} else {
					_, err = store.StateCAS(context.Background(), "p", "n", "k", mutation)
				}
				require.ErrorIs(t, err, tc.want)
			})
		}
	}
}

func TestPluginRuntimeStateMutationRollback(t *testing.T) {
	for _, kind := range []string{"create_conflict", "version_conflict", "delete_conflict", "query_error", "expired_after_write", "commit_error"} {
		t.Run(kind, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			lease := &service.PluginLease{Owner: "owner", Fence: 1}
			mutation := service.PluginStateMutation{ExpectedVersion: 3, Value: []byte("secret"), Lease: lease}
			if kind == "create_conflict" {
				mutation.ExpectedVersion = 0
			}
			expectPluginSlotLock(mock, "p", "n", "k")
			expectPluginLeaseCheck(mock, "p", "n", "k", lease, true, true)
			query := mock.ExpectQuery(`(?:UPDATE|INSERT INTO) sub2api_plugin_runtime_state`)
			want := service.ErrPluginStateConflict
			switch kind {
			case "query_error":
				want = errors.New("write failure")
				query.WillReturnError(want)
			case "expired_after_write", "commit_error":
				query.WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(4))
				expectPluginLeaseCheck(mock, "p", "n", "k", lease, kind != "expired_after_write", true)
				want = service.ErrPluginLeaseLost
			default:
				query.WillReturnError(sql.ErrNoRows)
			}
			if kind == "commit_error" {
				want = errors.New("commit failure")
				mock.ExpectCommit().WillReturnError(want)
			} else {
				mock.ExpectRollback()
			}
			var record service.PluginStateRecord
			var err error
			if kind == "delete_conflict" {
				record, err = store.StateDelete(context.Background(), "p", "n", "k", mutation)
			} else {
				record, err = store.StateCAS(context.Background(), "p", "n", "k", mutation)
			}
			require.ErrorIs(t, err, want)
			require.Empty(t, record)
		})
	}
}

func TestPluginRuntimeStateList(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limit      int
		queryLimit int
		count      int
		wantNext   string
	}{
		{"empty", 2, 3, 0, ""},
		{"last_page", 2, 3, 2, ""},
		{"next_page", 2, 3, 3, "prefix_b"},
		{"default", 0, service.PluginStateDefaultListLimit + 1, 1, ""},
		{"negative_default", -1, service.PluginStateDefaultListLimit + 1, 1, ""},
		{"capped", 1000000, service.PluginStateMaxListLimit + 1, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			ciphertext, err := store.encryptor.Encrypt("value")
			require.NoError(t, err)
			rows := sqlmock.NewRows([]string{"key", "value_encrypted", "version", "deleted"})
			for i := 0; i < tc.count; i++ {
				rows.AddRow("prefix_"+string(rune('a'+i)), ciphertext, i+1, false)
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT key, value_encrypted, version, deleted FROM sub2api_plugin_runtime_state WHERE plugin_key = $1 AND namespace = $2 AND NOT deleted AND starts_with(key, $3) AND key > $4 COLLATE "C" ORDER BY key COLLATE "C" LIMIT $5`)).
				WithArgs("p", "n", `literal%_\`, "previous", tc.queryLimit).WillReturnRows(rows).RowsWillBeClosed()
			records, next, err := store.StateList(context.Background(), "p", "n", `literal%_\`, "previous", tc.limit)
			require.NoError(t, err)
			require.Len(t, records, min(tc.count, tc.queryLimit-1))
			require.Equal(t, tc.wantNext, next)
			for _, record := range records {
				require.Equal(t, []byte("value"), record.Value)
				require.True(t, record.Found)
			}
		})
	}
}

func TestPluginRuntimeStateLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	t.Run("acquire_generates_independent_owner_tokens", func(t *testing.T) {
		store, mock := newPluginStateTestStore(t)
		var owners [2]string
		for i := range owners {
			expectPluginSlotLock(mock, "p", "n", "k")
			expires := time.Date(2040, 1, 1, 0, 0, i, 0, time.UTC)
			mock.ExpectQuery(regexp.QuoteMeta(`UPDATE sub2api_plugin_runtime_leases SET owner_token = $4, fence = fence + 1, expires_at = clock_timestamp() + $5::bigint * interval '1 microsecond' `+pluginStateTestWhere+` AND (owner_token = '' OR expires_at <= clock_timestamp()) RETURNING owner_token, fence, expires_at`)).
				WithArgs("p", "n", "k", pluginLeaseOwnerArg{&owners[i]}, int64(1500000)).
				WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fence", "expires_at"}).AddRow("db-owner", i+1, expires))
			mock.ExpectCommit()
			lease, err := store.LeaseAcquire(ctx, "p", "n", "k", 1500*time.Millisecond)
			require.NoError(t, err)
			require.EqualValues(t, i+1, lease.Fence)
			require.Equal(t, expires, lease.ExpiresAt, "expiry comes from DB, not the local clock")
		}
		require.NotEqual(t, owners[0], owners[1])
	})
	for _, operation := range []string{"acquire", "renew", "release"} {
		for _, success := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/conflict_or_lost", true: "/success"}[success], func(t *testing.T) {
				store, mock := newPluginStateTestStore(t)
				lease := service.PluginLease{Owner: "owner", Fence: 8, ExpiresAt: time.Unix(0, 0)}
				expires := time.Now().Add(time.Minute)
				expectPluginSlotLock(mock, "p", "n", "k")
				var query *sqlmock.ExpectedQuery
				if operation == "acquire" {
					query = mock.ExpectQuery(`UPDATE sub2api_plugin_runtime_leases SET owner_token`).WithArgs("p", "n", "k", sqlmock.AnyArg(), int64(60000000))
				} else if operation == "renew" {
					query = mock.ExpectQuery(regexp.QuoteMeta(`UPDATE sub2api_plugin_runtime_leases SET expires_at = clock_timestamp() + $6::bigint * interval '1 microsecond' `+pluginStateTestWhere+` AND owner_token = $4 AND fence = $5 AND expires_at > clock_timestamp() RETURNING owner_token, fence, expires_at`)).
						WithArgs("p", "n", "k", lease.Owner, lease.Fence, int64(60000000))
				} else {
					query = mock.ExpectQuery(regexp.QuoteMeta(`UPDATE sub2api_plugin_runtime_leases SET owner_token = '', expires_at = '-infinity' `+pluginStateTestWhere+` AND owner_token = $4 AND fence = $5 AND expires_at > clock_timestamp() RETURNING fence`)).
						WithArgs("p", "n", "k", lease.Owner, lease.Fence)
				}
				if success {
					if operation == "release" {
						query.WillReturnRows(sqlmock.NewRows([]string{"fence"}).AddRow(8))
					} else {
						query.WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fence", "expires_at"}).AddRow("owner", 8, expires))
					}
					mock.ExpectCommit()
				} else {
					query.WillReturnError(sql.ErrNoRows)
					mock.ExpectRollback()
				}
				var result service.PluginLease
				var err error
				switch operation {
				case "acquire":
					result, err = store.LeaseAcquire(ctx, "p", "n", "k", time.Minute)
				case "renew":
					result, err = store.LeaseRenew(ctx, "p", "n", "k", lease, time.Minute)
				case "release":
					err = store.LeaseRelease(ctx, "p", "n", "k", lease)
				}
				if success {
					require.NoError(t, err)
					if operation != "release" {
						require.Equal(t, expires, result.ExpiresAt)
						require.Equal(t, lease.Fence, result.Fence)
					}
				} else if operation == "acquire" {
					require.ErrorIs(t, err, service.ErrPluginStateConflict)
				} else {
					require.ErrorIs(t, err, service.ErrPluginLeaseLost)
				}
			})
		}
	}
}

type pluginStateFailEncryptor struct{}

func (pluginStateFailEncryptor) Encrypt(string) (string, error) {
	return "", errors.New("encryption unavailable")
}
func (pluginStateFailEncryptor) Decrypt(string) (string, error) {
	return "", errors.New("decryption unavailable")
}

func TestPluginRuntimeStateValidationAndEncryptionFailClosed(t *testing.T) {
	ctx := context.Background()
	store, _ := newPluginStateTestStore(t)
	for _, key := range []string{"", "a\x00b", string([]byte{0xff}), strings.Repeat("k", 513)} {
		_, err := store.StateGet(ctx, "p", "n", key)
		require.Error(t, err)
		_, err = store.StateCAS(ctx, "p", "n", key, service.PluginStateMutation{})
		require.Error(t, err)
		_, err = store.LeaseAcquire(ctx, "p", "n", key, time.Second)
		require.Error(t, err)
	}
	for _, ttl := range []time.Duration{-time.Second, 0, time.Nanosecond} {
		_, err := store.LeaseAcquire(ctx, "p", "n", "k", ttl)
		require.Error(t, err)
		_, err = store.LeaseRenew(ctx, "p", "n", "k", service.PluginLease{Owner: "o", Fence: 1}, ttl)
		require.Error(t, err)
	}
	_, err := store.StateDelete(ctx, "p", "n", "k", service.PluginStateMutation{})
	require.ErrorIs(t, err, service.ErrPluginStateConflict)
	_, err = store.StateCAS(ctx, "p", "n", "k", service.PluginStateMutation{ExpectedVersion: -1})
	require.ErrorIs(t, err, service.ErrPluginStateConflict)
	_, err = store.LeaseRenew(ctx, "p", "n", "k", service.PluginLease{}, time.Second)
	require.ErrorIs(t, err, service.ErrPluginLeaseLost)
	require.ErrorIs(t, store.LeaseRelease(ctx, "p", "n", "k", service.PluginLease{}), service.ErrPluginLeaseLost)
	for _, encryptor := range []service.SecretEncryptor{nil, pluginStateFailEncryptor{}} {
		store.encryptor = encryptor
		_, err = store.StateCAS(ctx, "p", "n", "k", service.PluginStateMutation{Value: []byte("secret")})
		require.Error(t, err)
	}
}

func TestPluginRuntimeStateSlotFailureRollsBack(t *testing.T) {
	for _, phase := range []string{"begin", "insert", "lock", "check"} {
		t.Run(phase, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			failure := errors.New("database failure")
			begin := mock.ExpectBegin()
			if phase == "begin" {
				begin.WillReturnError(failure)
			} else {
				insert := mock.ExpectExec(`INSERT INTO sub2api_plugin_runtime_leases`).WithArgs("p", "n", "k")
				if phase == "insert" {
					insert.WillReturnError(failure)
				} else {
					insert.WillReturnResult(sqlmock.NewResult(0, 0))
					lock := mock.ExpectQuery(`SELECT fence FROM sub2api_plugin_runtime_leases`).WithArgs("p", "n", "k")
					if phase == "lock" {
						lock.WillReturnError(failure)
					} else {
						lock.WillReturnRows(sqlmock.NewRows([]string{"fence"}).AddRow(0))
						mock.ExpectQuery(`SELECT owner_token`).WillReturnError(failure)
					}
				}
				mock.ExpectRollback()
			}
			_, err := store.StateCAS(context.Background(), "p", "n", "k", service.PluginStateMutation{})
			require.ErrorIs(t, err, failure)
		})
	}
}

func TestPluginRuntimeStateListErrors(t *testing.T) {
	for _, phase := range []string{"query", "rows", "decrypt", "scan"} {
		t.Run(phase, func(t *testing.T) {
			store, mock := newPluginStateTestStore(t)
			failure := errors.New("list failure")
			query := mock.ExpectQuery(`SELECT key, value_encrypted, version, deleted`)
			if phase == "query" {
				query.WillReturnError(failure)
			} else {
				rows := sqlmock.NewRows([]string{"key", "value_encrypted", "version", "deleted"})
				if phase == "scan" {
					rows.AddRow("k", "bad", "invalid-version", false)
				} else {
					rows.AddRow("k", "bad", 1, false)
				}
				if phase == "rows" {
					rows.RowError(0, failure)
				}
				query.WillReturnRows(rows).RowsWillBeClosed()
			}
			records, next, err := store.StateList(context.Background(), "p", "n", "", "", 2)
			require.Error(t, err)
			require.Nil(t, records, "do not return partial decrypted results")
			require.Empty(t, next)
		})
	}
}
