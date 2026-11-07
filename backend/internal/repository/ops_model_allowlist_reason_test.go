package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsModelAllowlistReasonUsesExactTypeWithIndependentFilters(t *testing.T) {
	groupID := int64(21)
	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{
		Reason: service.OpsErrorReasonModelNotAllowed, View: "excluded", GroupID: &groupID,
		Model: "blocked", ErrorTypesAny: []string{"invalid_request_error", service.OpsErrorReasonModelNotAllowed},
	})
	require.Contains(t, where, "e.error_type = $")
	require.Contains(t, where, "e.error_type = ANY($")
	require.Contains(t, where, "COALESCE(e.is_business_limited,false) = true")
	require.Contains(t, where, "COALESCE(e.requested_model, e.model, '') = $")
	require.Contains(t, where, "e.group_id = $")
	require.Contains(t, args, service.OpsErrorReasonModelNotAllowed)
	require.Contains(t, args, "blocked")
	require.Contains(t, args, groupID)
	defaultWhere, _ := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{})
	require.Contains(t, defaultWhere, "COALESCE(e.is_business_limited,false) = false")
	require.NotContains(t, defaultWhere, "e.error_type = $")
}
