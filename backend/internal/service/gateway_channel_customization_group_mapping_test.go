package service

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGatewayChannelCustomizationSettingsLegacyRuleDefaultsToLocalResponse(t *testing.T) {
	settings := decodeGatewayChannelCustomizationSettings(t, `{
		"rules": [{
			"name": "legacy-local-response",
			"enabled": true,
			"api_key_names": ["maibon-gpt"],
			"exact_paths": ["/v1/responses"],
			"status_code": 202,
			"body": "accepted"
		}]
	}`)

	require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&settings))
	wire := gatewayCustomizationRuleWire(t, settings.Rules[0])
	require.Equal(t, "local_response", wire["action"])
}

func TestNormalizeGatewayChannelCustomizationSettingsGroupMappingRequiresTargetGroup(t *testing.T) {
	for _, targetGroupID := range []int64{0, -1} {
		t.Run(strconv.FormatInt(targetGroupID, 10), func(t *testing.T) {
			settings := decodeGatewayChannelCustomizationSettings(t, `{
				"rules": [{
					"name": "map-to-gpt-pro",
					"enabled": true,
					"action": "group_mapping",
					"target_group_id": `+strconv.FormatInt(targetGroupID, 10)+`,
					"api_key_names": ["maibon-gpt"],
					"exact_paths": ["/v1/responses"]
				}]
			}`)

			err := NormalizeGatewayChannelCustomizationSettings(&settings)
			require.Error(t, err)
		})
	}
}

func TestNormalizeGatewayChannelCustomizationSettingsAcceptsGroupMappingTarget(t *testing.T) {
	settings := decodeGatewayChannelCustomizationSettings(t, `{
		"rules": [{
			"name": "map-to-gpt-pro",
			"enabled": true,
			"action": "group_mapping",
			"target_group_id": 88,
			"api_key_names": ["maibon-gpt"],
			"exact_paths": ["/v1/responses"]
		}]
	}`)

	require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&settings))
	wire := gatewayCustomizationRuleWire(t, settings.Rules[0])
	require.Equal(t, "group_mapping", wire["action"])
	require.Equal(t, float64(88), wire["target_group_id"])
}

func TestNormalizeGatewayChannelCustomizationSettingsPreservesPermissionOverrideOnlyForGroupMapping(t *testing.T) {
	settings := decodeGatewayChannelCustomizationSettings(t, `{
		"rules": [{
			"name": "map-to-restricted-group",
			"enabled": true,
			"action": "group_mapping",
			"target_group_id": 88,
			"bypass_target_group_permission": true,
			"api_key_names": ["restricted-key"],
			"exact_paths": ["/v1/responses"]
		}]
	}`)
	require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&settings))
	require.True(t, settings.Rules[0].BypassTargetGroupPermission)

	settings.Rules[0].Action = GatewayChannelCustomizationActionModelMapping
	settings.Rules[0].TargetModel = "gpt-5.6-sol"
	settings.Rules[0].Models = []string{"gpt-6-astra"}
	require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&settings))
	require.False(t, settings.Rules[0].BypassTargetGroupPermission)
}

func TestNormalizeGatewayChannelCustomizationSettingsNormalizesModelsAsRequestCondition(t *testing.T) {
	settings := decodeGatewayChannelCustomizationSettings(t, `{
		"rules": [{
			"name": "map-by-model",
			"enabled": true,
			"action": "group_mapping",
			"target_group_id": 88,
			"api_key_names": ["maibon-gpt"],
			"models": [" gpt-6-astra ", "GPT-6-ASTRA", ""]
		}]
	}`)

	require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&settings))
	require.Equal(t, []string{"gpt-6-astra"}, settings.Rules[0].Models)
}

func TestNormalizeGatewayChannelCustomizationSettingsRejectsUnknownAction(t *testing.T) {
	settings := decodeGatewayChannelCustomizationSettings(t, `{
		"rules": [{
			"name": "unknown-action",
			"enabled": true,
			"action": "redirect",
			"target_group_id": 88,
			"api_key_names": ["maibon-gpt"],
			"exact_paths": ["/v1/responses"]
		}]
	}`)

	require.Error(t, NormalizeGatewayChannelCustomizationSettings(&settings))
}

func decodeGatewayChannelCustomizationSettings(t *testing.T, raw string) GatewayChannelCustomizationSettings {
	t.Helper()
	var settings GatewayChannelCustomizationSettings
	require.NoError(t, json.Unmarshal([]byte(raw), &settings))
	return settings
}

func gatewayCustomizationRuleWire(t *testing.T, rule GatewayChannelCustomizationRule) map[string]any {
	t.Helper()
	raw, err := json.Marshal(rule)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	return wire
}
