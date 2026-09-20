package pluginv1

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The published schema must accept the same additive fields as Go manifests.
func TestManifestSchemaDeclaresOptionalHostRequirements(t *testing.T) {
	raw, err := os.ReadFile("manifest.schema.json")
	require.NoError(t, err)
	var schema struct {
		Properties struct {
			Requires struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type  string `json:"type"`
					Items struct {
						Type string `json:"type"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"requires"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	fields := schema.Properties.Requires.Properties
	api, ok := fields["host_service_api"]
	require.True(t, ok, "requires.additionalProperties=false must allow host_service_api")
	require.Equal(t, "integer", api.Type)
	features, ok := fields["host_features"]
	require.True(t, ok, "requires.additionalProperties=false must allow host_features")
	require.Equal(t, "array", features.Type)
	require.Equal(t, "string", features.Items.Type)
	require.NotContains(t, schema.Properties.Requires.Required, "host_service_api", "legacy manifests omit host requirements")
	require.NotContains(t, schema.Properties.Requires.Required, "host_features", "legacy manifests omit host requirements")
}

func TestManifestSchemaSetupTokenCapabilityRequiresOAuthLikeFeature(t *testing.T) {
	raw, err := os.ReadFile("manifest.schema.json")
	require.NoError(t, err)
	var schema struct {
		Properties struct {
			Capabilities struct {
				Items struct {
					Properties struct {
						AccountType struct {
							Enum []string `json:"enum"`
						} `json:"account_type"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"capabilities"`
		} `json:"properties"`
		AllOf []any `json:"allOf"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.ElementsMatch(t, []string{"oauth", "setup-token"}, schema.Properties.Capabilities.Items.Properties.AccountType.Enum)
	// Keep the published capability guard aligned with PluginManifest.Validate.
	expectedGuard := `{
		"if":{"properties":{"capabilities":{"contains":{"properties":{"account_type":{"const":"setup-token"}},"required":["account_type"]}}},"required":["capabilities"]},
		"then":{"properties":{"requires":{"properties":{"host_features":{"contains":{"const":"oauth-like.v1"}}},"required":["host_features"]}}}
	}`
	var expected any
	require.NoError(t, json.Unmarshal([]byte(expectedGuard), &expected))
	require.Contains(t, schema.AllOf, expected)
}

func TestManifestSchemaConfigSecretsRequiresHostFeature(t *testing.T) {
	raw, err := os.ReadFile("manifest.schema.json")
	require.NoError(t, err)
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
		AllOf      []any                      `json:"allOf"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.Contains(t, schema.Properties, "config_secrets")
	require.NotContains(t, schema.Required, "config_secrets", "legacy manifests omit config secrets")
	expectedGuard := `{
		"if":{"properties":{"config_secrets":{"minItems":1}},"required":["config_secrets"]},
		"then":{"properties":{"requires":{"properties":{"host_features":{"contains":{"const":"config-secrets.v1"}}},"required":["host_features"]}}}
	}`
	var expected any
	require.NoError(t, json.Unmarshal([]byte(expectedGuard), &expected))
	require.Contains(t, schema.AllOf, expected)
}
