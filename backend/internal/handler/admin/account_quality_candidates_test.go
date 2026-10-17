package admin

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type leanQualityAdmin struct {
	*stubAdminService
	loadedIDs []int64
	query     service.AccountQualityCandidatesQuery
}

func (s *leanQualityAdmin) AccountQualityCandidateIDs(_ context.Context, query service.AccountQualityCandidatesQuery) ([]int64, error) {
	s.query = query
	return []int64{3, 2, 1}, nil
}
func (s *leanQualityAdmin) AccountQualityCandidatePage(_ context.Context, _ service.AccountQualityCandidatesQuery, ids []int64) ([]service.Account, error) {
	s.loadedIDs = append([]int64{}, ids...)
	rows := make([]service.Account, len(ids))
	for i, id := range ids {
		rows[i].ID = id
	}
	return rows, nil
}

func TestAccountQualityLeanCandidatesFilterBeforeLoadingPage(t *testing.T) {
	admin := &leanQualityAdmin{stubAdminService: newStubAdminService()}
	firstToken, duration := 800.0, 5000.0
	repo := &accountListQualityUsageRepo{samples: map[int64]service.AccountQualitySamples{
		1: {Recent1h: service.AccountQualityWindow{SampleCount: 3, FirstTokenSampleCount: 3, AverageFirstTokenMs: &firstToken, AverageDurationMs: &duration}},
		3: {Recent1h: service.AccountQualityWindow{SampleCount: 3, FirstTokenSampleCount: 3, AverageFirstTokenMs: &firstToken, AverageDurationMs: &duration}},
	}}
	usage := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &AccountHandler{adminService: admin, accountUsageService: usage}
	filter, ok := service.ParseAccountQualityFilter("1h-A")
	require.True(t, ok)
	rows, total, stats, err := h.listAccountsWithQualityFilter(context.Background(), 2, 1, "openai", "apikey", "active", "needle", 0, "", "name", "desc", service.AccountListScopeUpstream, filter, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, stats, 1)
	require.Contains(t, stats, int64(1))
	require.Equal(t, []int64{1}, admin.loadedIDs)
	require.Equal(t, int64(1), rows[0].ID)
	require.Equal(t, "needle", admin.query.Search)
	require.Equal(t, "desc", admin.query.SortOrder)
}
