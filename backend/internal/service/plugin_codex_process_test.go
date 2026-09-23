package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
)

// The opt-in binary is built from the independent module, not linked into the
// host. No real account, credential or outbound request is used by this smoke.
func TestPluginCodexStateRealProcessHandshakeAndDisabledDraft(t *testing.T) {
	path := os.Getenv("SUB2API_TEST_CODEX_STATE_BINARY")
	if path == "" {
		t.Skip("independent native plugin binary not supplied")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	installation := &PluginInstallation{ID: 17, PluginKey: "baiyu.codex-state", Version: "0.1.0", BinaryPath: path, BinarySHA256: hex.EncodeToString(sum[:]), Manifest: PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: append([]string(nil), pluginv1.HostFeatures...)}}}
	host := newPluginHostServiceServer(installation.PluginKey, newFakePluginKVStore(), &fakeAccountDirectory{})
	store := &pluginHostStateTestStore{}
	host.stateStore = store
	host.allowSetupToken = true
	runtime, err := startPluginRuntime(context.Background(), installation, 15*time.Second, filepath.Join(t.TempDir(), "runtime"), host)
	require.NoError(t, err)
	defer runtime.kill()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config := []byte(`{"version":1,"enabled":false,"harvest_proxy_url":"","dial_proxy_url":"","accounts":[]}`)
	canonical, scope, err := runtime.validateScopedConfig(ctx, config)
	require.NoError(t, err)
	require.Empty(t, scope)
	runtime.staged.Store(true)
	legacyCanonical, legacyScope, err := runtime.validateScopedConfig(ctx, []byte(`{}`))
	require.NoError(t, err)
	require.Empty(t, legacyScope)
	require.JSONEq(t, string(config), string(legacyCanonical))
	runtime.staged.Store(false)
	require.NoError(t, runtime.applyScopedConfig(ctx, canonical, 1, false))
	require.NoError(t, runtime.checkHealth(ctx))
	response, err := runtime.api.AdmitBatch(ctx, &pluginv1.AdmitBatchRequest{ConfigRevision: 1, Candidates: []*pluginv1.AdmissionCandidate{{AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "fixture"}}})
	require.NoError(t, err)
	require.Len(t, response.Decisions, 1)
	require.False(t, response.Decisions[0].Allowed)
	require.Empty(t, store.calls, "draft validation and health must not harvest or mutate state")
}
