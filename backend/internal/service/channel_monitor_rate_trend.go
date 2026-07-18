package service

import (
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	MonitorRateRange24Hours = "24h"
	MonitorRateRange7Days   = "7d"
	MonitorRateRange30Days  = "30d"
)

var ErrChannelMonitorInvalidRateRange = infraerrors.BadRequest(
	"CHANNEL_MONITOR_INVALID_RATE_RANGE", "rate_range must be one of 24h/7d/30d",
)

// GroupRateSnapshot is one immutable version of a group's public rate
// configuration. User-specific multipliers are deliberately absent.
type GroupRateSnapshot struct {
	GroupID            int64
	RateMultiplier     float64
	PeakRateEnabled    bool
	PeakStart          string
	PeakEnd            string
	PeakRateMultiplier float64
	Timezone           string
	EffectiveAt        time.Time
}

// GroupRateSnapshotSeries contains the versions needed for one requested
// window and the timestamp from which any history is actually available.
type GroupRateSnapshotSeries struct {
	ObservedSince time.Time
	Snapshots     []GroupRateSnapshot
}

// PublicRateTrendPoint represents a step change in the public rate.
type PublicRateTrendPoint struct {
	ObservedAt time.Time
	Rate       float64
}

type PublicRateTrend struct {
	CurrentRate   *float64
	ObservedSince *time.Time
	Points        []PublicRateTrendPoint
}

func ParseMonitorRateRange(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", MonitorRateRange24Hours:
		return MonitorRateRange24Hours, nil
	case MonitorRateRange7Days:
		return MonitorRateRange7Days, nil
	case MonitorRateRange30Days:
		return MonitorRateRange30Days, nil
	default:
		return "", ErrChannelMonitorInvalidRateRange
	}
}

func monitorRateRangeStart(rateRange string, now time.Time) time.Time {
	switch rateRange {
	case MonitorRateRange7Days:
		return now.Add(-7 * 24 * time.Hour)
	case MonitorRateRange30Days:
		return now.Add(-30 * 24 * time.Hour)
	default:
		return now.Add(-24 * time.Hour)
	}
}

func buildPublicRateTrend(series GroupRateSnapshotSeries, from, until time.Time) PublicRateTrend {
	if until.Before(from) || len(series.Snapshots) == 0 {
		return PublicRateTrend{Points: []PublicRateTrendPoint{}}
	}

	snapshots := append([]GroupRateSnapshot(nil), series.Snapshots...)
	sort.SliceStable(snapshots, func(i, j int) bool {
		return snapshots[i].EffectiveAt.Before(snapshots[j].EffectiveAt)
	})

	points := make([]PublicRateTrendPoint, 0, len(snapshots)*3)
	for i := range snapshots {
		snapshot := snapshots[i]
		if snapshot.EffectiveAt.After(until) {
			break
		}
		segmentStart := snapshot.EffectiveAt
		if segmentStart.Before(from) {
			segmentStart = from
		}
		segmentEnd := until
		if i+1 < len(snapshots) && snapshots[i+1].EffectiveAt.Before(segmentEnd) {
			segmentEnd = snapshots[i+1].EffectiveAt
		}
		if segmentEnd.Before(segmentStart) {
			continue
		}

		points = append(points, PublicRateTrendPoint{
			ObservedAt: segmentStart,
			Rate:       publicRateAt(snapshot, segmentStart),
		})
		points = append(points, peakBoundaryPoints(snapshot, segmentStart, segmentEnd)...)
	}

	points = normalizePublicRateTrendPoints(points, from, until)
	latest := latestEffectiveSnapshot(snapshots, until)
	if latest == nil {
		return PublicRateTrend{Points: points}
	}
	current := publicRateAt(*latest, until)
	observedSince := series.ObservedSince
	if observedSince.IsZero() {
		observedSince = snapshots[0].EffectiveAt
	}
	return PublicRateTrend{
		CurrentRate:   &current,
		ObservedSince: &observedSince,
		Points:        points,
	}
}

func latestEffectiveSnapshot(snapshots []GroupRateSnapshot, at time.Time) *GroupRateSnapshot {
	var latest *GroupRateSnapshot
	for i := range snapshots {
		if snapshots[i].EffectiveAt.After(at) {
			break
		}
		latest = &snapshots[i]
	}
	return latest
}

func publicRateAt(snapshot GroupRateSnapshot, at time.Time) float64 {
	if !snapshot.PeakRateEnabled || snapshot.PeakRateMultiplier < 0 {
		return snapshot.RateMultiplier
	}
	start, startOK := parseMinutes(snapshot.PeakStart)
	end, endOK := parseMinutes(snapshot.PeakEnd)
	if !startOK || !endOK || start == end {
		return snapshot.RateMultiplier
	}
	local := at.In(snapshotLocation(snapshot.Timezone))
	minute := local.Hour()*60 + local.Minute()
	inPeak := minute >= start && minute < end
	if start > end {
		inPeak = minute >= start || minute < end
	}
	if inPeak {
		return snapshot.RateMultiplier * snapshot.PeakRateMultiplier
	}
	return snapshot.RateMultiplier
}

func peakBoundaryPoints(snapshot GroupRateSnapshot, from, until time.Time) []PublicRateTrendPoint {
	if !snapshot.PeakRateEnabled || !until.After(from) {
		return nil
	}
	startMinute, startOK := parseMinutes(snapshot.PeakStart)
	endMinute, endOK := parseMinutes(snapshot.PeakEnd)
	if !startOK || !endOK || startMinute == endMinute {
		return nil
	}

	location := snapshotLocation(snapshot.Timezone)
	localFrom := from.In(location)
	localUntil := until.In(location)
	firstDay := time.Date(localFrom.Year(), localFrom.Month(), localFrom.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -1)
	lastDay := time.Date(localUntil.Year(), localUntil.Month(), localUntil.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)

	points := make([]PublicRateTrendPoint, 0, 8)
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		for _, minute := range []int{startMinute, endMinute} {
			boundary := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, location)
			if !boundary.After(from) || !boundary.Before(until) {
				continue
			}
			points = append(points, PublicRateTrendPoint{
				ObservedAt: boundary,
				Rate:       publicRateAt(snapshot, boundary),
			})
		}
	}
	return points
}

func snapshotLocation(name string) *time.Location {
	if location, err := time.LoadLocation(strings.TrimSpace(name)); err == nil {
		return location
	}
	return timezone.Location()
}

func normalizePublicRateTrendPoints(points []PublicRateTrendPoint, from, until time.Time) []PublicRateTrendPoint {
	sort.SliceStable(points, func(i, j int) bool {
		return points[i].ObservedAt.Before(points[j].ObservedAt)
	})
	out := make([]PublicRateTrendPoint, 0, len(points))
	for _, point := range points {
		if point.ObservedAt.Before(from) || point.ObservedAt.After(until) {
			continue
		}
		if len(out) > 0 && point.ObservedAt.Equal(out[len(out)-1].ObservedAt) {
			out[len(out)-1] = point
			continue
		}
		if len(out) > 0 && point.Rate == out[len(out)-1].Rate {
			continue
		}
		out = append(out, point)
	}
	return out
}
