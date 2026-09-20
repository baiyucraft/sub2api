package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestRetiredCodexTicketDefaultsAbsent(t *testing.T) {
	resetViperWithJWTSecret(t)
	setDefaults()
	for _, key := range viper.AllKeys() {
		require.False(t, strings.HasPrefix(key, "gateway.openai_codex_ticket"), key)
	}
	_, exists := reflect.TypeOf(GatewayConfig{}).FieldByName("OpenAICodexTicket")
	require.False(t, exists)
}

func TestLoadIgnoresRetiredCodexTicketYAML(t *testing.T) {
	resetViperWithJWTSecret(t)
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`gateway:
  openai_compact_model: gpt-5.1
  openai_codex_ticket:
    enabled: true
    target_length: ignored-invalid-integer
    ttl_seconds: -1
    refresh_before_seconds: ignored-invalid-integer
    harvest_proxy_url: http://user:retired-state-config-fixture-6f293a@proxy.invalid:80
    harvest_dial_proxy_url: ignored-invalid-proxy
    harvest_probe_interval_seconds: ignored-invalid-integer
    harvest_attempt_timeout_seconds: ignored-invalid-integer
    fail_closed: ignored-invalid-boolean
    models:
      legacy: ignored-model-map
`), 0o600))
	t.Setenv("CONFIG_FILE", configFile)
	t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_ENABLED", "ignored-invalid-boolean")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "gpt-5.1", cfg.Gateway.OpenAICompactModel)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "retired-state-config-fixture-6f293a")
	require.NotContains(t, string(encoded), "OpenAICodexTicket")
	require.NotContains(t, string(encoded), "openai_codex_ticket")
}
