package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type monitorRateRepositoryStub struct {
	ChannelMonitorRepository
	monitors            []*ChannelMonitor
	seriesByGroup       map[int64]GroupRateSnapshotSeries
	requestedGroupIDs   []int64
	rateQueryCalls      int
	availabilityWindows []int
	availabilityIDs     [][]int64
}

func (s *monitorRateRepositoryStub) ListEnabled(context.Context) ([]*ChannelMonitor, error) {
	return s.monitors, nil
}

func (s *monitorRateRepositoryStub) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*ChannelMonitorLatest, error) {
	return map[int64][]*ChannelMonitorLatest{}, nil
}

func (s *monitorRateRepositoryStub) ComputeAvailabilityForMonitors(_ context.Context, ids []int64, windowDays int) (map[int64][]*ChannelMonitorAvailability, error) {
	s.availabilityWindows = append(s.availabilityWindows, windowDays)
	s.availabilityIDs = append(s.availabilityIDs, append([]int64(nil), ids...))
	out := make(map[int64][]*ChannelMonitorAvailability, len(ids))
	for _, id := range ids {
		out[id] = []*ChannelMonitorAvailability{{Model: "gpt", AvailabilityPct: float64(windowDays)}}
	}
	return out, nil
}

func (s *monitorRateRepositoryStub) ListRecentHistoryForMonitors(context.Context, []int64, map[int64]string, int) (map[int64][]*ChannelMonitorHistoryEntry, error) {
	return map[int64][]*ChannelMonitorHistoryEntry{}, nil
}

func (s *monitorRateRepositoryStub) ListGroupRateSnapshots(
	_ context.Context,
	groupIDs []int64,
	_, _ time.Time,
) (map[int64]GroupRateSnapshotSeries, error) {
	s.rateQueryCalls++
	s.requestedGroupIDs = append([]int64(nil), groupIDs...)
	return s.seriesByGroup, nil
}

func TestListUserView_PublicRateTrendIsBatchedAndOptional(t *testing.T) {
	groupID := int64(9)
	otherGroupID := int64(10)
	effective := time.Now().UTC().Add(-48 * time.Hour)
	repo := &monitorRateRepositoryStub{
		monitors: []*ChannelMonitor{
			{ID: 1, PrimaryModel: "gpt", GroupID: &groupID, ShowGroupRate: true},
			{ID: 2, PrimaryModel: "claude", GroupID: &groupID, ShowGroupRate: true},
			{ID: 3, PrimaryModel: "gemini", GroupID: &otherGroupID, ShowGroupRate: false},
		},
		seriesByGroup: map[int64]GroupRateSnapshotSeries{
			groupID: {
				ObservedSince: effective,
				Snapshots: []GroupRateSnapshot{{
					GroupID:        groupID,
					RateMultiplier: 1.25,
					Timezone:       "UTC",
					EffectiveAt:    effective,
				}},
			},
		},
	}
	svc := NewChannelMonitorService(repo, nil)

	views, err := svc.ListUserView(context.Background(), MonitorRateRange24Hours, map[int64]struct{}{
		groupID: {}, otherGroupID: {},
	})
	require.NoError(t, err)
	require.Len(t, views, 3)
	require.Equal(t, 1, repo.rateQueryCalls)
	require.Equal(t, []int64{groupID}, repo.requestedGroupIDs)

	require.True(t, views[0].ShowGroupRate)
	require.Equal(t, 1.25, *views[0].CurrentPublicRate)
	require.NotEmpty(t, views[0].RateTrend)
	require.True(t, views[1].ShowGroupRate)
	require.Equal(t, views[0].RateTrend, views[1].RateTrend)

	require.False(t, views[2].ShowGroupRate)
	require.Nil(t, views[2].CurrentPublicRate)
	require.Nil(t, views[2].RateObservedSince)
	require.Nil(t, views[2].RateTrend)
}

func TestListUserView_InvalidRateRangeFailsBeforeRepositoryQuery(t *testing.T) {
	repo := &monitorRateRepositoryStub{}
	svc := NewChannelMonitorService(repo, nil)

	_, err := svc.ListUserView(context.Background(), "60d", nil)
	require.ErrorIs(t, err, ErrChannelMonitorInvalidRateRange)
	require.Zero(t, repo.rateQueryCalls)
}

func TestListUserView_FiltersBeforeBatchAggregationAndUsesSelectedWindow(t *testing.T) {
	allowedGroupID := int64(7)
	deniedGroupID := int64(8)
	repo := &monitorRateRepositoryStub{monitors: []*ChannelMonitor{
		{ID: 1, PrimaryModel: "gpt", CredentialMode: ChannelMonitorCredentialManual},
		{ID: 2, PrimaryModel: "gpt", GroupID: &allowedGroupID, CredentialMode: ChannelMonitorCredentialManagedLocal},
		{ID: 3, PrimaryModel: "gpt", GroupID: &deniedGroupID, CredentialMode: ChannelMonitorCredentialManagedLocal},
	}}
	svc := NewChannelMonitorService(repo, nil)

	views, err := svc.ListUserView(context.Background(), MonitorRateRange15Days, map[int64]struct{}{allowedGroupID: {}})
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, []int64{1, 2}, []int64{views[0].ID, views[1].ID})
	require.Equal(t, []int{monitorAvailability15Days, monitorAvailability7Days}, repo.availabilityWindows)
	for _, ids := range repo.availabilityIDs {
		require.Equal(t, []int64{1, 2}, ids)
	}
	require.Equal(t, float64(monitorAvailability15Days), views[0].Availability)
	require.Equal(t, float64(monitorAvailability7Days), views[0].Availability7d)
}

func TestListUserView_SevenDayRangeReusesSingleAvailabilityQuery(t *testing.T) {
	repo := &monitorRateRepositoryStub{monitors: []*ChannelMonitor{{
		ID: 1, PrimaryModel: "gpt", CredentialMode: ChannelMonitorCredentialManual,
	}}}
	svc := NewChannelMonitorService(repo, nil)

	views, err := svc.ListUserView(context.Background(), MonitorRateRange7Days, nil)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, []int{monitorAvailability7Days}, repo.availabilityWindows)
	require.Equal(t, views[0].Availability7d, views[0].Availability)
}

func TestFilterUserVisibleMonitors_UnboundManagedMonitorIsHidden(t *testing.T) {
	visible := filterUserVisibleMonitors([]*ChannelMonitor{
		{ID: 1, CredentialMode: ChannelMonitorCredentialManual},
		{ID: 2, CredentialMode: ChannelMonitorCredentialManagedLocal},
	}, nil)
	require.Len(t, visible, 1)
	require.Equal(t, int64(1), visible[0].ID)
}

type monitorDetailRepositoryStub struct {
	ChannelMonitorRepository
	monitor             *ChannelMonitor
	availabilityWindows []int
	latestCalls         int
}

func (s *monitorDetailRepositoryStub) GetByID(context.Context, int64) (*ChannelMonitor, error) {
	return s.monitor, nil
}

func (s *monitorDetailRepositoryStub) ListLatestPerModel(context.Context, int64) ([]*ChannelMonitorLatest, error) {
	s.latestCalls++
	return []*ChannelMonitorLatest{{Model: s.monitor.PrimaryModel, Status: MonitorStatusOperational}}, nil
}

func (s *monitorDetailRepositoryStub) ComputeAvailability(_ context.Context, _ int64, windowDays int) ([]*ChannelMonitorAvailability, error) {
	s.availabilityWindows = append(s.availabilityWindows, windowDays)
	return []*ChannelMonitorAvailability{{Model: s.monitor.PrimaryModel, AvailabilityPct: float64(windowDays)}}, nil
}

func TestGetUserDetail_UnauthorizedGroupReturnsNotFoundBeforeAggregation(t *testing.T) {
	groupID := int64(11)
	repo := &monitorDetailRepositoryStub{monitor: &ChannelMonitor{
		ID: 4, Enabled: true, GroupID: &groupID, PrimaryModel: "gpt",
		CredentialMode: ChannelMonitorCredentialManagedLocal,
	}}
	svc := NewChannelMonitorService(repo, nil)

	_, err := svc.GetUserDetail(context.Background(), 4, MonitorRateRange24Hours, nil)
	require.ErrorIs(t, err, ErrChannelMonitorNotFound)
	require.Zero(t, repo.latestCalls)
	require.Empty(t, repo.availabilityWindows)
}

func TestGetUserDetail_Includes24HourAvailability(t *testing.T) {
	groupID := int64(11)
	repo := &monitorDetailRepositoryStub{monitor: &ChannelMonitor{
		ID: 4, Enabled: true, GroupID: &groupID, PrimaryModel: "gpt",
		CredentialMode: ChannelMonitorCredentialManagedLocal,
	}}
	svc := NewChannelMonitorService(repo, nil)

	detail, err := svc.GetUserDetail(
		context.Background(), 4, MonitorRateRange24Hours, map[int64]struct{}{groupID: {}},
	)
	require.NoError(t, err)
	require.Equal(t, []int{
		monitorAvailability24Hours,
		monitorAvailability7Days,
		monitorAvailability15Days,
		monitorAvailability30Days,
	}, repo.availabilityWindows)
	require.Len(t, detail.Models, 1)
	require.Equal(t, float64(monitorAvailability24Hours), detail.Models[0].Availability24h)
}
