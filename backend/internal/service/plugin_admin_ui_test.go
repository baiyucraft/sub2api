package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNativePluginAdminUIAcceptsContract(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.ConfigSecrets = []string{"harvest_proxy_url"}
	raw := []byte(`{
      "schema_version": 1,
      "title": "Codex STATE",
      "description": "管理页面",
      "poll_interval_seconds": 5,
      "layout": [
        {"type":"section","id":"main","children":[
          {"type":"text_input","id":"proxy","write":"/config/proxy_url"},
          {"type":"text","bind":"/status/message","condition":{"op":"truthy","path":"/status/healthy"}},
          {"type":"button","action":"refresh","payload":{"model":"/local/model"}}
        ]}
      ]
    }`)
	definition, err := ParseNativePluginAdminUI(raw, manifest)
	require.NoError(t, err)
	require.Equal(t, "Codex STATE", definition.Title)
	require.Len(t, definition.Layout, 1)
}

func TestParseNativePluginAdminUIRejectsUnsafeBindingsAndUnknownNodes(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.ConfigSecrets = []string{"secret"}
	for name, raw := range map[string]string{
		"prototype":       `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text","bind":"/status/prototype"}]}`,
		"host secrets":    `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text","bind":"/config/_host_secrets"}]}`,
		"declared secret": `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text","bind":"/config/secret"}]}`,
		"write resources": `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text_input","write":"/resources/value"}]}`,
		"unknown node":    `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"script"}]}`,
		"unknown icon":    `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text","icon":"remote"}]}`,
		"duplicate id":    `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"stack","id":"same","children":[{"type":"text","id":"same"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseNativePluginAdminUI([]byte(raw), manifest)
			require.Error(t, err)
		})
	}
}

func TestParseNativePluginAdminUIRejectsLimitsAndTrailingJSON(t *testing.T) {
	manifest := testPluginManifest(nil)
	base := `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[{"type":"text"}]}`
	_, err := ParseNativePluginAdminUI([]byte(base+" {}"), manifest)
	require.Error(t, err)
	tooShort := `{"schema_version":1,"title":"x","poll_interval_seconds":1,"layout":[{"type":"text"}]}`
	_, err = ParseNativePluginAdminUI([]byte(tooShort), manifest)
	require.Error(t, err)
	deep := `{"schema_version":1,"title":"x","poll_interval_seconds":2,"layout":[` + strings.Repeat(`{"type":"stack","children":[`, PluginAdminUIMaxDepth+1) + `{"type":"text"}` + strings.Repeat(`]}`, PluginAdminUIMaxDepth+1) + `]}`
	_, err = ParseNativePluginAdminUI([]byte(deep), manifest)
	require.Error(t, err)
}

func TestManifestV2UIContract(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.SchemaVersion = 2
	manifest.Requires.UIBridge = 0
	manifest.Requires.AdminUI = 1
	manifest.UI = PluginUIManifest{Type: PluginUITypeNative, Definition: "ui/admin-ui.json"}
	manifest.Files[manifest.UI.Definition] = strings.Repeat("a", 64)
	require.NoError(t, manifest.validateForRuntime(manifest.RuntimeKey()))

	for _, ui := range []PluginUIManifest{
		{Type: "bad"},
		{Type: PluginUITypeNative, Entrypoint: "ui/index.html"},
		{Type: PluginUITypeNone, Definition: "ui/admin-ui.json"},
	} {
		manifest.UI = ui
		require.Error(t, manifest.validateForRuntime(manifest.RuntimeKey()))
	}
}

func TestCodexStateNativeDefinitionStaysWithinHostContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "plugins", "codex-state", "ui", "admin-ui.json"))
	require.NoError(t, err)
	manifest := testPluginManifest(nil)
	manifest.ConfigSecrets = []string{"harvest_proxy_url", "dial_proxy_url"}
	definition, err := ParseNativePluginAdminUI(raw, manifest)
	require.NoError(t, err)
	require.Equal(t, "Codex STATE", definition.Title)
	require.NotEmpty(t, definition.Layout)
}
