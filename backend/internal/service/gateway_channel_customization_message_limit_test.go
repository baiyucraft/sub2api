package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGatewayCustomizationLongExactMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		text  string
		valid bool
	}{
		{"long Chinese exact", "exact", strings.Repeat("完整题目", 50), true},
		{"exact byte boundary", "exact", strings.Repeat("x", gatewayChannelCustomizationMaxExactMessageBytes), true},
		{"exact over byte boundary", "exact", strings.Repeat("x", gatewayChannelCustomizationMaxExactMessageBytes+1), false},
		{"exact invalid UTF8", "exact", "\xff", false},
		{"regex byte boundary", "regex", strings.Repeat("x", gatewayChannelCustomizationMaxStringBytes), true},
		{"regex keeps old limit", "regex", strings.Repeat("x", gatewayChannelCustomizationMaxStringBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := GatewayChannelCustomizationRule{Name: "match", UserIDs: []int64{27}, RequestMessageMatchMode: tc.mode, RequestMessageText: tc.text}
			settings := GatewayChannelCustomizationSettings{Rules: []GatewayChannelCustomizationRule{rule}}
			err := NormalizeGatewayChannelCustomizationSettings(&settings)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			raw, err := json.Marshal(settings)
			require.NoError(t, err)
			var loaded GatewayChannelCustomizationSettings
			require.NoError(t, json.Unmarshal(raw, &loaded))
			require.NoError(t, NormalizeGatewayChannelCustomizationSettings(&loaded))
			require.Equal(t, tc.text, loaded.Rules[0].RequestMessageText)
		})
	}
}
