//go:build integration

package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newPluginStatePostgresFixture(t *testing.T) (context.Context, service.PluginStateStore, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	var suffix [16]byte
	_, err := rand.Read(suffix[:])
	require.NoError(t, err)
	pluginKey := "test.state." + hex.EncodeToString(suffix[:])
	var revision int64
	var scope []byte
	err = integrationDB.QueryRowContext(ctx, `
		INSERT INTO sub2api_plugin_installations
		(plugin_key, name, version, artifact_path, install_path, binary_path, binary_sha256)
		VALUES ($1, 'state test', '1', '', '', '', '')
		RETURNING config_revision, managed_scope
	`, pluginKey).Scan(&revision, &scope)
	require.NoError(t, err)
	require.Zero(t, revision)
	require.Nil(t, scope, "legacy NULL scope must not become an empty array")
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := integrationDB.ExecContext(cleanup, `DELETE FROM sub2api_plugin_installations WHERE plugin_key = $1`, pluginKey)
		require.NoError(t, err)
		cancel()
	})
	store := NewPluginRuntimeStateStore(integrationDB, &AESEncryptor{key: bytes.Repeat([]byte{4}, 32)})
	return ctx, store, pluginKey
}

func TestPluginRuntimeStatePostgresLifecycle(t *testing.T) {
	ctx, store, plugin := newPluginStatePostgresFixture(t)
	_, other, otherPlugin := newPluginStatePostgresFixture(t)
	value := []byte{0, 0xff, 's', 'e', 'c', 'r', 'e', 't'}
	record, err := store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{Value: value})
	require.NoError(t, err)
	require.EqualValues(t, 1, record.Version)
	got, err := store.StateGet(ctx, plugin, "n", "k")
	require.NoError(t, err)
	require.Equal(t, record, got)
	var encrypted string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value_encrypted FROM sub2api_plugin_runtime_state WHERE plugin_key=$1 AND namespace='n' AND key='k'`, plugin).Scan(&encrypted))
	require.NotEqual(t, string(value), encrypted)

	_, err = other.StateCAS(ctx, otherPlugin, "n", "k", service.PluginStateMutation{Value: []byte("other plugin")})
	require.NoError(t, err)
	_, err = store.StateCAS(ctx, plugin, "other", "k", service.PluginStateMutation{Value: []byte("other namespace")})
	require.NoError(t, err)
	record, err = store.StateDelete(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, record.Version)
	require.False(t, record.Found)
	got, err = store.StateGet(ctx, plugin, "n", "k")
	require.NoError(t, err)
	require.Equal(t, record, got)
	for _, staleVersion := range []int64{0, 1} {
		_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: staleVersion})
		require.ErrorIs(t, err, service.ErrPluginStateConflict)
	}
	record, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 2, Value: value})
	require.NoError(t, err)
	require.EqualValues(t, 3, record.Version)

	first, err := store.LeaseAcquire(ctx, plugin, "n", "k", time.Minute)
	require.NoError(t, err)
	_, err = store.LeaseAcquire(ctx, plugin, "n", "k", time.Minute)
	require.ErrorIs(t, err, service.ErrPluginStateConflict)
	_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 3})
	require.ErrorIs(t, err, service.ErrPluginStateConflict)
	_, err = store.StateDelete(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 3})
	require.ErrorIs(t, err, service.ErrPluginStateConflict)
	_, err = other.StateCAS(ctx, otherPlugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 1, Value: value})
	require.NoError(t, err, "leases must not lock another plugin")
	_, err = store.StateCAS(ctx, plugin, "other", "k", service.PluginStateMutation{ExpectedVersion: 1, Value: value})
	require.NoError(t, err, "leases must not lock another namespace")
	for _, invalid := range []service.PluginLease{
		{Owner: "different-owner", Fence: first.Fence},
		{Owner: first.Owner, Fence: first.Fence + 1},
	} {
		_, err = store.LeaseRenew(ctx, plugin, "n", "k", invalid, time.Minute)
		require.ErrorIs(t, err, service.ErrPluginLeaseLost)
		require.ErrorIs(t, store.LeaseRelease(ctx, plugin, "n", "k", invalid), service.ErrPluginLeaseLost)
		_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 3, Lease: &invalid})
		require.ErrorIs(t, err, service.ErrPluginLeaseLost)
	}
	first.ExpiresAt = time.Unix(0, 0)
	renewed, err := store.LeaseRenew(ctx, plugin, "n", "k", first, 2*time.Minute)
	require.NoError(t, err, "caller clock does not determine lease validity")
	require.Equal(t, first.Owner, renewed.Owner)
	require.Equal(t, first.Fence, renewed.Fence)
	_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 3, Lease: &first, Value: value})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE sub2api_plugin_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE plugin_key=$1 AND namespace='n' AND key='k'`, plugin)
	require.NoError(t, err)
	_, err = store.LeaseRenew(ctx, plugin, "n", "k", first, time.Minute)
	require.ErrorIs(t, err, service.ErrPluginLeaseLost)
	require.ErrorIs(t, store.LeaseRelease(ctx, plugin, "n", "k", first), service.ErrPluginLeaseLost)
	_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 4, Lease: &first})
	require.ErrorIs(t, err, service.ErrPluginLeaseLost)
	second, err := store.LeaseAcquire(ctx, plugin, "n", "k", time.Minute)
	require.NoError(t, err)
	require.Equal(t, first.Fence+1, second.Fence)
	require.NotEqual(t, first.Owner, second.Owner)
	_, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 4, Lease: &first})
	require.ErrorIs(t, err, service.ErrPluginLeaseLost)
	require.NoError(t, store.LeaseRelease(ctx, plugin, "n", "k", second))
	require.ErrorIs(t, store.LeaseRelease(ctx, plugin, "n", "k", second), service.ErrPluginLeaseLost)
	record, err = store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: 4, Value: value})
	require.NoError(t, err)
	require.EqualValues(t, 5, record.Version)
	third, err := store.LeaseAcquire(ctx, plugin, "n", "k", time.Minute)
	require.NoError(t, err)
	require.Equal(t, second.Fence+1, third.Fence)
}

func TestPluginRuntimeStatePostgresList(t *testing.T) {
	ctx, store, plugin := newPluginStatePostgresFixture(t)
	for _, key := range []string{"a%_1", "a%_2", "a%_3", "a%_deleted", "abc"} {
		_, err := store.StateCAS(ctx, plugin, "n", key, service.PluginStateMutation{Value: []byte(key)})
		require.NoError(t, err)
	}
	_, err := store.StateDelete(ctx, plugin, "n", "a%_deleted", service.PluginStateMutation{ExpectedVersion: 1})
	require.NoError(t, err)
	page, next, err := store.StateList(ctx, plugin, "n", "a%_", "", 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, "a%_1", page[0].Key)
	require.Equal(t, "a%_2", page[1].Key)
	require.Equal(t, "a%_2", next)
	page, next, err = store.StateList(ctx, plugin, "n", "a%_", next, 2)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "a%_3", page[0].Key)
	require.Empty(t, next)
}

func TestPluginRuntimeStatePostgresConcurrentCAS(t *testing.T) {
	ctx, store, plugin := newPluginStatePostgresFixture(t)
	for _, version := range []int64{0, 1} {
		const writers = 8
		start := make(chan struct{})
		results := make(chan error, writers)
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{ExpectedVersion: version, Value: []byte("value")})
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else {
				require.ErrorIs(t, err, service.ErrPluginStateConflict)
			}
		}
		require.Equal(t, 1, wins)
		record, err := store.StateGet(ctx, plugin, "n", "k")
		require.NoError(t, err)
		require.Equal(t, version+1, record.Version)
	}
}

func TestPluginRuntimeStatePostgresConcurrentLeaseAcquire(t *testing.T) {
	ctx, store, plugin := newPluginStatePostgresFixture(t)
	const contenders = 8
	type outcome struct {
		lease service.PluginLease
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			<-start
			lease, err := store.LeaseAcquire(ctx, plugin, "n", "k", time.Minute)
			results <- outcome{lease, err}
		}()
	}
	close(start)
	wins := 0
	for i := 0; i < contenders; i++ {
		result := <-results
		if result.err == nil {
			wins++
			require.EqualValues(t, 1, result.lease.Fence)
		} else {
			require.ErrorIs(t, result.err, service.ErrPluginStateConflict)
		}
	}
	require.Equal(t, 1, wins)
}

func TestPluginRuntimeStatePostgresExpiryWhileWaitingForStateLock(t *testing.T) {
	ctx, store, plugin := newPluginStatePostgresFixture(t)
	_, err := store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{Value: []byte("before")})
	require.NoError(t, err)
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	var version int64
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT version FROM sub2api_plugin_runtime_state WHERE plugin_key=$1 AND namespace='n' AND key='k' FOR UPDATE`, plugin).Scan(&version))
	var blockerPID int
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	lease, err := store.LeaseAcquire(ctx, plugin, "n", "k", 2*time.Second)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := store.StateCAS(ctx, plugin, "n", "k", service.PluginStateMutation{Value: []byte("after"), ExpectedVersion: 1, Lease: &lease})
		done <- err
	}()
	var waiting bool
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting) == nil && waiting
	}, time.Second, 10*time.Millisecond, "writer must pass its initial lease check and block on the state row")
	// Wait on DB time, not the test runner's clock, before unblocking the write.
	var expired bool
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(ctx, `SELECT expires_at <= clock_timestamp() FROM sub2api_plugin_runtime_leases WHERE plugin_key=$1 AND namespace='n' AND key='k'`, plugin).Scan(&expired) == nil && expired
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, blocker.Commit())
	select {
	case err := <-done:
		require.True(t, errors.Is(err, service.ErrPluginLeaseLost), "got %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	record, err := store.StateGet(ctx, plugin, "n", "k")
	require.NoError(t, err)
	require.EqualValues(t, 1, record.Version)
	require.Equal(t, []byte("before"), record.Value)
}
