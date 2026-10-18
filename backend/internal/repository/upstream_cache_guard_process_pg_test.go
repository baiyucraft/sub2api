//go:build cacheguardpg

package repository

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// This fixture forks the test binary: workers have independent engines, heaps,
// database pools and OS processes. It never uses an application/production DB.
func TestCacheGuardCrossProcessPostgres(t *testing.T) {
	dsn := os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_CACHE_GUARD_TEST_ISOLATED"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer admin.Close()
	var database string
	require.NoError(t, admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database))
	require.Contains(t, strings.ToLower(database), "test", "refuse a non-test database")
	var nonce [8]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "cache_guard_process_test_" + hex.EncodeToString(nonce[:])
	_, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.ExecContext(cleanup, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	}()
	scopedDSN := cacheGuardScopedDSN(dsn, schema)
	db, err := sql.Open("postgres", scopedDSN)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE accounts(id BIGINT PRIMARY KEY,upstream_key_id BIGINT,deleted_at TIMESTAMPTZ); INSERT INTO accounts(id,upstream_key_id) VALUES(1,1),(2,1),(3,1)`)
	require.NoError(t, err)
	for _, name := range []string{"296_upstream_cache_guard.sql", "297_upstream_cache_guard_account_scope.sql", "299_upstream_cache_guard_revisions.sql", "301_upstream_cache_guard_admissions.sql"} {
		text, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(text))
		require.NoError(t, err, name)
	}
	// Both workers wait for parent release after startup, then race the same
	// immutable request. Repeated completion must remain exactly once.
	runCacheGuardProcesses(t, ctx, scopedDSN, "same", "same")
	var count, natural int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (state_json->>'natural_new_count')::int FROM upstream_cache_guard_runtime WHERE state_kind='group' AND state_key='quota'`).Scan(&natural))
	require.Equal(t, 1, natural)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts WHERE evidence_id='process-same' AND processed=TRUE`).Scan(&count))
	require.Equal(t, 1, count)
	// Different account locks converge on the same session lock without a
	// group-wide deadlock, lost admission, or second natural-new identity.
	runCacheGuardProcesses(t, ctx, scopedDSN, "cross-left", "cross-right")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (state_json->>'natural_new_count')::int FROM upstream_cache_guard_runtime WHERE state_kind='group' AND state_key='quota'`).Scan(&natural))
	require.Equal(t, 2, natural)
	r := processCacheGuardRequest("cross-left")
	var raw []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT state_json FROM upstream_cache_guard_runtime WHERE group_id=1 AND state_kind='session' AND state_key=$1`, upstreamcacheguard.SessionKey(r)).Scan(&raw))
	// Validate both account-specific admissions in the one durable identity.
	var completed int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE group_id=1 AND session_key=$1 AND completed=TRUE`, upstreamcacheguard.SessionKey(r)).Scan(&completed))
	require.Equal(t, 2, completed)
	// A worker claims and exits before completion. Another process cannot
	// steal a live lease; after expiry it must recover and complete only once.
	store := NewUpstreamCacheGuardRepository(db)
	e := processCacheGuardEngine(store)
	leaseRequest := processCacheGuardRequest("lease")
	warmup := leaseRequest
	warmup.ID += "-warmup"
	warmupAdmission, err := e.Admit(ctx, warmup)
	require.NoError(t, err)
	require.NoError(t, e.Complete(ctx, processCacheGuardEvidence(warmup, warmupAdmission)))
	leaseRequest.StartedAt = time.Now().UTC()
	admission, err := e.Admit(ctx, leaseRequest)
	require.NoError(t, err)
	evidence := processCacheGuardEvidence(leaseRequest, admission)
	require.True(t, evidence.ValidSample(), "lease recovery must exercise a real eligible warm sample")
	var priorRevision int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT version FROM upstream_cache_guard_fact_versions WHERE account_id=3 AND upstream_key_id=1`).Scan(&priorRevision))
	require.NoError(t, store.Enqueue(ctx, evidence))
	runCacheGuardProcesses(t, ctx, scopedDSN, "claim-exit")
	runCacheGuardProcesses(t, ctx, scopedDSN, "claim-live")
	var remaining float64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT GREATEST(0,EXTRACT(EPOCH FROM (claim_until-CURRENT_TIMESTAMP))) FROM upstream_cache_guard_facts WHERE evidence_id=$1`, evidence.ID).Scan(&remaining))
	select {
	case <-time.After(time.Duration(remaining*float64(time.Second)) + 10*time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	runCacheGuardProcesses(t, ctx, scopedDSN, "claim-recover", "claim-recover")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts WHERE evidence_id=$1 AND processed=TRUE`, evidence.ID).Scan(&count))
	require.Equal(t, 1, count)
	var revision int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT version FROM upstream_cache_guard_fact_versions WHERE account_id=3 AND upstream_key_id=1`).Scan(&revision))
	require.Equal(t, priorRevision+2, revision, "one enqueue plus one processed transition; competing recovery cannot duplicate sampling")
	var validRequests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (state_json->>'valid_requests')::int FROM upstream_cache_guard_accounts WHERE account_id=3 AND upstream_key_id=1`).Scan(&validRequests))
	require.Equal(t, 1, validRequests, "recovered warm evidence contributes one eligible sample")
	t.Log("two independent processes: same admission/evidence idempotence, cross-account shared session, crashed lease owner and competing recovery PASS")
}

func runCacheGuardProcesses(t *testing.T, ctx context.Context, dsn string, actions ...string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	type child struct {
		cmd     *exec.Cmd
		input   io.WriteCloser
		scanner *bufio.Scanner
		stderr  bytes.Buffer
	}
	children := make([]*child, 0, len(actions))
	startedAt := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	for _, action := range actions {
		c := &child{cmd: exec.CommandContext(ctx, executable, "-test.run=^TestCacheGuardPostgresProcessWorker$", "-test.timeout=30s")}
		c.cmd.Env = append(os.Environ(), "SUB2API_CACHE_GUARD_WORKER_DSN="+dsn, "SUB2API_CACHE_GUARD_WORKER_ACTION="+action, "SUB2API_CACHE_GUARD_WORKER_STARTED="+startedAt)
		c.input, err = c.cmd.StdinPipe()
		require.NoError(t, err)
		output, err := c.cmd.StdoutPipe()
		require.NoError(t, err)
		c.scanner = bufio.NewScanner(output)
		c.cmd.Stderr = &c.stderr
		require.NoError(t, c.cmd.Start())
		children = append(children, c)
		t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	}
	for _, c := range children {
		require.True(t, c.scanner.Scan(), "worker startup failed")
		require.Equal(t, "CACHE_GUARD_READY", c.scanner.Text())
	}
	for _, c := range children {
		_, err := io.WriteString(c.input, "run\n")
		require.NoError(t, err)
		_ = c.input.Close()
	}
	for _, c := range children {
		var output strings.Builder
		for c.scanner.Scan() {
			output.WriteString(c.scanner.Text())
			output.WriteByte('\n')
		}
		require.NoError(t, c.scanner.Err())
		require.NoError(t, c.cmd.Wait(), "worker failed: %s %s", output.String(), c.stderr.String())
	}
}

func processCacheGuardRequest(action string) upstreamcacheguard.Request {
	account, digest := int64(1), "a"
	if strings.HasPrefix(action, "cross-") {
		digest = "b"
	}
	if action == "cross-right" {
		account = 2
	}
	if action == "lease" {
		account, digest = 3, "c"
	}
	startedAt := time.Now().UTC()
	if fixed, err := strconv.ParseInt(os.Getenv("SUB2API_CACHE_GUARD_WORKER_STARTED"), 10, 64); err == nil {
		startedAt = time.Unix(0, fixed).UTC()
	}
	return upstreamcacheguard.Request{ID: "process-" + action, LogicalSessionDigest: strings.Repeat(digest, 64), APIKeyID: 1, GroupID: 1, AccountID: account, UpstreamKeyID: 1, Model: "fixture", Protocol: "openai", StartedAt: startedAt}
}

func processCacheGuardEvidence(r upstreamcacheguard.Request, a upstreamcacheguard.Admission) upstreamcacheguard.Evidence {
	return upstreamcacheguard.Evidence{ID: r.ID, Request: r, Admission: a, CompletedAt: time.Now().UTC(), Success: true, RawUsage: upstreamcacheguard.RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}}
}

func processCacheGuardEngine(store upstreamcacheguard.Store) *upstreamcacheguard.Engine {
	e := upstreamcacheguard.NewEngine(store)
	s := upstreamcacheguard.DefaultSettings()
	s.Mode = upstreamcacheguard.Protect
	s.TrialPercent = 100
	if err := e.ApplySettings(s); err != nil {
		panic(err)
	}
	return e
}

func TestCacheGuardPostgresProcessWorker(t *testing.T) {
	action := os.Getenv("SUB2API_CACHE_GUARD_WORKER_ACTION")
	if action == "" {
		t.Skip("child process entrypoint")
	}
	db, err := sql.Open("postgres", os.Getenv("SUB2API_CACHE_GUARD_WORKER_DSN"))
	require.NoError(t, err)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	fmt.Println("CACHE_GUARD_READY")
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	require.NoError(t, err)
	store := NewUpstreamCacheGuardRepository(db)
	e := processCacheGuardEngine(store)
	if strings.HasPrefix(action, "claim-") {
		lease := 2 * time.Second
		pending, err := store.ClaimPending(ctx, action+"-"+strconv.Itoa(os.Getpid()), 1, lease)
		require.NoError(t, err)
		if action == "claim-live" {
			require.Empty(t, pending, "live lease belongs to the exited worker until its deadline")
			return
		}
		if action == "claim-exit" {
			require.Len(t, pending, 1)
			return
		}
		for _, v := range pending {
			require.NoError(t, e.Complete(ctx, v))
			require.NoError(t, e.Complete(ctx, v))
		}
		return
	}
	r := processCacheGuardRequest(action)
	var a upstreamcacheguard.Admission
	for attempt := 0; attempt < 5; attempt++ {
		a, err = e.Admit(ctx, r)
		if err == nil {
			break
		}
		if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "canceling statement") {
			break
		}
	}
	require.NoError(t, err)
	v := processCacheGuardEvidence(r, a)
	require.NoError(t, e.Complete(ctx, v))
	require.NoError(t, e.Complete(ctx, v))
}
