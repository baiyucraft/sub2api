package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

type nativeAdminUITestRepo struct {
	PluginRepository
	installation *PluginInstallation
	artifact     []byte
	artifactRead int
}

func (r *nativeAdminUITestRepo) GetByID(context.Context, int64) (*PluginInstallation, error) {
	copy := *r.installation
	return &copy, nil
}

func (r *nativeAdminUITestRepo) GetArtifact(context.Context, int64) ([]byte, error) {
	r.artifactRead++
	return r.artifact, nil
}

func TestPluginManagerAdminUICachesByInstallationAndBinarySHA(t *testing.T) {
	definition := []byte(`{"schema_version":1,"title":"Native","description":"","poll_interval_seconds":2,"layout":[{"type":"text","title":"hello"}]}`)
	digest := sha256.Sum256(definition)
	manifest := testPluginManifest(nil)
	manifest.SchemaVersion = 2
	manifest.Requires.UIBridge = 0
	manifest.Requires.AdminUI = 1
	manifest.UI = PluginUIManifest{Type: PluginUITypeNative, Definition: "ui/admin-ui.json"}
	manifest.Files[manifest.UI.Definition] = hex.EncodeToString(digest[:])
	artifact := buildNativeAdminUIArtifact(t, definition)
	repo := &nativeAdminUITestRepo{
		installation: &PluginInstallation{ID: 7, BinarySHA256: "binary-sha", Manifest: manifest},
		artifact:     artifact,
	}
	manager := NewPluginManager(repo, nil, nil, PluginHostInfo{}, nil)

	first, err := manager.AdminUI(context.Background(), 7)
	require.NoError(t, err)
	first.Layout[0].Title = "mutated"
	second, err := manager.AdminUI(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "hello", second.Layout[0].Title)
	require.Equal(t, 1, repo.artifactRead)

	manager.invalidateAdminUICache(7)
	_, err = manager.AdminUI(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 2, repo.artifactRead)
}

func buildNativeAdminUIArtifact(t *testing.T, definition []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("ui/admin-ui.json")
	require.NoError(t, err)
	_, err = entry.Write(definition)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}
