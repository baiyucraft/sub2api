package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginSecretConfigNeverReturnsPersistedValues(t *testing.T) {
	manifest := PluginManifest{ConfigSecrets: []string{"proxy", "dial"}}
	raw := json.RawMessage(`{"proxy":"http://user:private-password@host","dial":"","enabled":true}`)
	public, err := pluginPublicConfig(manifest, raw)
	require.NoError(t, err)
	require.NotContains(t, string(public), "private-password")
	require.NotContains(t, string(public), "http://")
	require.JSONEq(t, `{"proxy":"","dial":"","enabled":true,"_host_secrets":{"proxy":true,"dial":false}}`, string(public))
	repo := &pluginTokenRepository{installation: &PluginInstallation{Manifest: manifest, ConfigEncrypted: "ENC:" + string(raw)}}
	manager := &PluginManager{repo: repo, encryptor: pluginTokenEncryptor{}}
	response, err := manager.GetConfig(context.Background(), 1)
	require.NoError(t, err)
	require.JSONEq(t, string(public), string(response))
}

func TestPluginSecretNormalConfigSaveCannotReplaceClearOrInjectSecrets(t *testing.T) {
	manifest := PluginManifest{ConfigSecrets: []string{"proxy", "dial"}}
	stored := json.RawMessage(`{"proxy":"private-value","enabled":true}`)
	for _, input := range []string{`{"proxy":"","dial":"injected","enabled":false}`, `{"enabled":false}`, `{"proxy":"attacker","enabled":false,"_host_secrets":{"proxy":false}}`} {
		merged, err := pluginMergeConfigSecrets(manifest, stored, json.RawMessage(input), false)
		require.NoError(t, err)
		require.JSONEq(t, `{"proxy":"private-value","enabled":false}`, string(merged))
	}
	merged, err := pluginMergeConfigSecrets(manifest, stored, json.RawMessage(`{"proxy":"","dial":"replacement","enabled":true}`), true)
	require.NoError(t, err)
	require.JSONEq(t, `{"proxy":"","dial":"replacement","enabled":true}`, string(merged))
}

func TestPluginSecretDeclarationIsOptInAndBounded(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.ConfigSecrets = []string{"proxy"}
	require.Error(t, manifest.Validate())
	manifest.Requires.HostServiceAPI = 2
	manifest.Requires.HostFeatures = []string{"config-secrets.v1"}
	require.NoError(t, manifest.Validate())
	for _, fields := range [][]string{{"proxy", "proxy"}, {"_host_secrets"}, {"nested/path"}, {""}} {
		manifest.ConfigSecrets = fields
		require.Error(t, manifest.Validate())
	}
	legacy := json.RawMessage(`{"old_api1_plugin":"unchanged"}`)
	result, err := pluginPublicConfig(PluginManifest{}, legacy)
	require.NoError(t, err)
	require.Equal(t, legacy, result)
}
