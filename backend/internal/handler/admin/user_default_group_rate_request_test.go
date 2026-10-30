//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateUserRequestDefaultGroupRatePatchStates(t *testing.T) {
	for _, tt := range []struct {
		name  string
		body  string
		set   bool
		value *float64
	}{
		{name: "omitted", body: `{}`},
		{name: "null", body: `{"default_group_rate_percent":null}`, set: true},
		{name: "95", body: `{"default_group_rate_percent":95}`, set: true, value: adminDefaultGroupRateTestValue(95)},
		{name: "zero", body: `{"default_group_rate_percent":0}`, set: true, value: adminDefaultGroupRateTestValue(0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var request UpdateUserRequest
			require.NoError(t, json.Unmarshal([]byte(tt.body), &request))
			require.Equal(t, tt.set, request.DefaultGroupRatePercent.Set)
			require.Equal(t, tt.value, request.DefaultGroupRatePercent.Value)
			require.Nil(t, request.AllowedGroups)
			require.Nil(t, request.GroupRates)
			require.Nil(t, request.GroupRatePercents)
			require.False(t, request.ResetGroupRatePercents)
		})
	}
}

func TestUpdateUserRequestResetAndExceptionsRemainDistinct(t *testing.T) {
	var request UpdateUserRequest
	require.NoError(t, json.Unmarshal([]byte(`{"default_group_rate_percent":95,"reset_group_rate_percents":true,"group_rate_percents":{"8":90,"9":0,"10":null}}`), &request))
	require.True(t, request.ResetGroupRatePercents)
	require.True(t, request.DefaultGroupRatePercent.Set)
	require.Equal(t, adminDefaultGroupRateTestValue(95), request.DefaultGroupRatePercent.Value)
	require.Equal(t, adminDefaultGroupRateTestValue(90), request.GroupRatePercents[8])
	require.Equal(t, adminDefaultGroupRateTestValue(0), request.GroupRatePercents[9])
	require.Contains(t, request.GroupRatePercents, int64(10))
	require.Nil(t, request.GroupRatePercents[10])
}

func adminDefaultGroupRateTestValue(value float64) *float64 { return &value }
