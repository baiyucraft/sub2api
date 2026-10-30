//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNullableFloat64FieldPatchStates(t *testing.T) {
	for _, tt := range []struct {
		name  string
		body  string
		set   bool
		value *float64
	}{
		{name: "omitted", body: `{}`},
		{name: "null", body: `{"percent":null}`, set: true},
		{name: "whitespace null", body: `{"percent":  null  }`, set: true},
		{name: "95", body: `{"percent":95}`, set: true, value: nullableFloat64TestValue(95)},
		{name: "zero", body: `{"percent":0}`, set: true, value: nullableFloat64TestValue(0)},
		{name: "fractional", body: `{"percent":95.25}`, set: true, value: nullableFloat64TestValue(95.25)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var request struct {
				Percent NullableFloat64Field `json:"percent"`
			}
			require.NoError(t, json.Unmarshal([]byte(tt.body), &request))
			require.Equal(t, tt.set, request.Percent.Set)
			require.Equal(t, tt.value, request.Percent.Value)
		})
	}
}

func TestNullableFloat64FieldRejectsNonNumericJSON(t *testing.T) {
	for _, body := range []string{`"95"`, `true`, `[]`, `{}`, `1e309`} {
		t.Run(body, func(t *testing.T) {
			var value NullableFloat64Field
			require.Error(t, json.Unmarshal([]byte(body), &value))
		})
	}
}

func TestNullableFloat64FieldNullClearsPreviousValue(t *testing.T) {
	var value NullableFloat64Field
	require.NoError(t, json.Unmarshal([]byte(`95`), &value))
	require.Equal(t, nullableFloat64TestValue(95), value.Value)
	require.NoError(t, json.Unmarshal([]byte(`null`), &value))
	require.True(t, value.Set)
	require.Nil(t, value.Value)
	require.NoError(t, json.Unmarshal([]byte(`0`), &value))
	require.Equal(t, nullableFloat64TestValue(0), value.Value)
}

func nullableFloat64TestValue(value float64) *float64 { return &value }
