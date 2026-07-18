package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type monitorRateRepositoryStub struct {
	ChannelMonitorRepository
	monitors          []*ChannelMonitor
	seriesByGroup     map[int64]GroupRateSnapshotSeries
	requestedGroupIDs []int64
	rateQueryCalls    int
}

func (s *monitorRateRepositoryStub) ListEnabled(context.Context) ([]*ChannelMonitor, error) {
	return s.monitors, nil
}

func (s *monitorRateRepositoryStub) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*ChannelMonitorLatest, error) {
	return map[int64][]*ChannelMonitorLatest{}, nil
}

func (s *monitorRateRepositoryStub) ComputeAvailabilityForMonitors(context.Context, []int64, int) (map[int64][]*ChannelMonitorAvailability, error) {
	return map[int64][]*ChannelMonitorAvailability{}, nil
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

	views, err := svc.ListUserView(context.Background(), MonitorRateRange24Hours)
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

	_, err := svc.ListUserView(context.Background(), "60d")
	require.ErrorIs(t, err, ErrChannelMonitorInvalidRateRange)
	require.Zero(t, repo.rateQueryCalls)
}
