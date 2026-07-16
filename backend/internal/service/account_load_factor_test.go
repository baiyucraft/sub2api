//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtrHelper(v int) *int { return &v }

func TestEffectiveLoadFactor_NilAccount(t *testing.T) {
	var a *Account
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NilLoadFactor_PositiveConcurrency(t *testing.T) {
	a := &Account{Concurrency: 5}
	require.Equal(t, 5, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NilLoadFactor_ZeroConcurrency(t *testing.T) {
	a := &Account{Concurrency: 0}
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_PositiveLoadFactor(t *testing.T) {
	a := &Account{Concurrency: 5, LoadFactor: intPtrHelper(20)}
	require.Equal(t, 20, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_ZeroLoadFactor_FallbackToConcurrency(t *testing.T) {
	a := &Account{Concurrency: 5, LoadFactor: intPtrHelper(0)}
	require.Equal(t, 5, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NegativeLoadFactor_FallbackToConcurrency(t *testing.T) {
	a := &Account{Concurrency: 3, LoadFactor: intPtrHelper(-1)}
	require.Equal(t, 3, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_ZeroLoadFactor_ZeroConcurrency(t *testing.T) {
	a := &Account{Concurrency: 0, LoadFactor: intPtrHelper(0)}
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

func TestAutoUpstreamLoadFactor(t *testing.T) {
	tests := []struct {
		name        string
		priority    int
		concurrency int
		want        int
	}{
		{"priority 5 doubles base", 5, 100, 200},
		{"priority 6 uses one and half base", 6, 100, 150},
		{"priority 10 uses one and half base", 10, 100, 150},
		{"priority 11 uses base", 11, 100, 100},
		{"priority 20 uses base", 20, 100, 100},
		{"priority 21 uses three quarters base", 21, 100, 75},
		{"priority 50 uses three quarters base", 50, 100, 75},
		{"priority 51 uses half base", 51, 100, 50},
		{"rounds fractional result", 21, 7, 5},
		{"zero concurrency uses one", 5, 0, 2},
		{"negative concurrency uses one", 100, -10, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, AutoUpstreamLoadFactor(tt.priority, tt.concurrency))
		})
	}
}

func TestApplyUpstreamAutoLoadFactor(t *testing.T) {
	cfgID := int64(1)
	keyID := int64(2)
	a := &Account{UpstreamConfigID: &cfgID, UpstreamKeyID: &keyID, Priority: 10, Concurrency: 100, LoadFactor: intPtrHelper(999)}

	require.True(t, a.ApplyUpstreamAutoLoadFactor())
	require.NotNil(t, a.LoadFactor)
	require.Equal(t, 150, *a.LoadFactor)
}
