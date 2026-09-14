package legacy

import (
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

var _ forkscheduling.PreferredPoolPolicy = PreferredPolicy{}
var _ forkscheduling.RateOrder = EffectiveRatePolicy{}

func TestPreferredComparisonIgnoresRateOnlyInsidePreferredPool(t *testing.T) {
	left := Candidate{ID: 1, Priority: 1, Rate: 0.20, RateKnown: true, Preferred: true}
	right := Candidate{ID: 2, Priority: 1, Rate: 0.01, RateKnown: true, Preferred: true}
	if got := ComparePreferredAware(left, right); got != 0 {
		t.Fatalf("preferred comparison = %d, want 0", got)
	}
	if got := ComparePreferredAware(left, Candidate{ID: 3, Priority: 1, Rate: 0.01, RateKnown: true}); got != -1 {
		t.Fatalf("preferred versus ordinary comparison = %d, want -1", got)
	}
}

func TestPreferredGroupComparisonKeepsPriorityAndLastUsedBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC)
	left := Candidate{ID: 1, Priority: 1, LastUsedAt: &now}
	right := Candidate{ID: 2, Priority: 1, LastUsedAt: &now}
	if !SamePreferredGroup(left, right) {
		t.Fatal("same priority and last-used timestamp should share a group")
	}
	right.Priority = 2
	if SamePreferredGroup(left, right) {
		t.Fatal("different priority should not share a group")
	}
}

func TestPreferredPartitionPreservesInputOrder(t *testing.T) {
	preferred, ordinary := (PreferredPolicy{}).PartitionPreferredIndices([]Candidate{
		{ID: 10, Preferred: false},
		{ID: 11, Preferred: true},
		{ID: 12, Preferred: false},
	})
	if got, want := preferred, []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preferred indices = %v, want %v", got, want)
	}
	if got, want := ordinary, []int{0, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary indices = %v, want %v", got, want)
	}
}

func TestLegacySortComparatorsKeepRateOutOfPreferredTier(t *testing.T) {
	left := Candidate{Priority: 1, Rate: 0.20, RateKnown: true}
	right := Candidate{Priority: 1, Rate: 0.01, RateKnown: true}
	if got := ComparePriorityAndLastUsed(left, right, false); got != 0 {
		t.Fatalf("preferred tier comparison = %d, want 0", got)
	}
	if got := CompareRateOnly(left, right); got != 1 {
		t.Fatalf("rate comparison = %d, want 1", got)
	}
}
