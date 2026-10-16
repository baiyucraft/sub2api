package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulingProtectionRequestLocalErrorKeepsOnlyProtectionReselect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  UpstreamFailoverError
		accept bool
	}{
		{"budget", UpstreamFailoverError{PluginAdmissionRejected: true, ProtectionOrdinaryFallback: true, Scope: GatewayFailureScopeRequest}, true},
		{"scope changed", UpstreamFailoverError{PluginAdmissionRejected: true, Scope: GatewayFailureScopeRequest, Reason: "scheduling_protection_scope_changed"}, true},
		{"ordinary upstream error", UpstreamFailoverError{Scope: GatewayFailureScopeRequest}, false},
		{"plugin rejection", UpstreamFailoverError{PluginAdmissionRejected: true, Scope: GatewayFailureScopeRequest}, false},
		{"account fault", UpstreamFailoverError{PluginAdmissionRejected: true, ProtectionOrdinaryFallback: true, Scope: GatewayFailureScopeAccount}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := schedulingProtectionRequestLocalError(fmt.Errorf("wrapped: %w", &tc.value))
			if tc.accept {
				require.Same(t, &tc.value, actual)
			} else {
				require.Nil(t, actual)
			}
		})
	}
}
