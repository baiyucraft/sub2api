package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountListETagTracksGuardOnlyChangesBothDirections(t *testing.T) {
	for _, lite := range []bool{false, true} {
		t.Run(map[bool]string{true: "lite", false: "full"}[lite], func(t *testing.T) {
			build := func(state string, rate float64) string {
				summary := &service.UpstreamCacheGuardSummary{State: state, RatePercent: &rate, ValidRequests: 22}
				states := []service.UpstreamCacheGuardState{{GroupID: 7, State: state, RatePercent: &rate, ValidRequests: 22}}
				if lite {
					rows := []AccountListItemWithConcurrency{{AccountListItem: &dto.AccountListItem{ID: 101}, UpstreamCacheGuard: summary, UpstreamCacheGuardStates: states}}
					return buildAccountsListETag(rows, 1, 1, 20, "", "", "", "", true)
				}
				rows := []AccountWithConcurrency{{Account: &dto.Account{ID: 101}, UpstreamCacheGuard: summary, UpstreamCacheGuardStates: states}}
				return buildAccountsListETag(rows, 1, 1, 20, "", "", "", "", false)
			}
			original := build("normal", 93.2)
			changed := build("protected", 68.1)
			reverted := build("normal", 93.2)
			require.NotEmpty(t, original)
			require.NotEqual(t, original, changed)
			require.Equal(t, original, reverted)
			require.False(t, ifNoneMatchMatched(original, changed))
			require.False(t, ifNoneMatchMatched(changed, reverted))
			require.True(t, ifNoneMatchMatched(reverted, original))
		})
	}
}
