package legacy

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

type Candidate = forkscheduling.CandidateView

type PreferredPolicy struct{}

func (PreferredPolicy) CompareWithinPreferred(left, right forkscheduling.CandidateView) int {
	return ComparePriorityOnly(left, right)
}

// PartitionPreferredIndices returns the preferred/ordinary partition for
// already-resolved candidates while preserving input order. Returning indices
// lets service adapters retain their domain pointers without moving domain
// objects into the legacy package.
func (PreferredPolicy) PartitionPreferredIndices(candidates []forkscheduling.CandidateView) (preferred, ordinary []int) {
	preferred = make([]int, 0, len(candidates))
	ordinary = make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		if candidate.Preferred {
			preferred = append(preferred, index)
		} else {
			ordinary = append(ordinary, index)
		}
	}
	return preferred, ordinary
}

type EffectiveRatePolicy struct{}

func (EffectiveRatePolicy) Compare(left, right forkscheduling.CandidateView) int {
	return CompareRateOnly(left, right)
}

func ComparePriorityOnly(left, right Candidate) int {
	if left.Priority < right.Priority {
		return -1
	}
	if left.Priority > right.Priority {
		return 1
	}
	return 0
}

func CompareTier(left, right Candidate) int {
	if priority := ComparePriorityOnly(left, right); priority != 0 {
		return priority
	}
	if left.RateKnown != right.RateKnown {
		if left.RateKnown {
			return -1
		}
		return 1
	}
	if !left.RateKnown || left.Rate == right.Rate {
		return 0
	}
	if left.Rate < right.Rate {
		return -1
	}
	return 1
}

// CompareRateOnly compares only the effective rate signal. Priority and
// other account ordering are intentionally owned by the caller's outer tier.
func CompareRateOnly(left, right Candidate) int {
	if left.RateKnown != right.RateKnown {
		if left.RateKnown {
			return -1
		}
		return 1
	}
	if !left.RateKnown || left.Rate == right.Rate {
		return 0
	}
	if left.Rate < right.Rate {
		return -1
	}
	return 1
}

func ComparePreferredAware(left, right Candidate) int {
	if left.Preferred != right.Preferred {
		if left.Preferred {
			return -1
		}
		return 1
	}
	if left.Preferred {
		return ComparePriorityOnly(left, right)
	}
	return CompareTier(left, right)
}

func SameLastUsedAt(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	// The existing service sorter groups LastUsedAt at second precision.
	return left.Unix() == right.Unix()
}

func SamePreferredGroup(left, right Candidate) bool {
	return ComparePriorityOnly(left, right) == 0 && SameLastUsedAt(left.LastUsedAt, right.LastUsedAt)
}

func SamePreferredLoadGroup(left, right Candidate) bool {
	return ComparePriorityOnly(left, right) == 0 && left.LoadRate == right.LoadRate && SameLastUsedAt(left.LastUsedAt, right.LastUsedAt)
}

// ComparePriorityAndLastUsed is the deterministic part of the existing
// service sorter. Randomization of equal groups remains at the caller.
func ComparePriorityAndLastUsed(left, right Candidate, preferOAuth bool) int {
	if priority := ComparePriorityOnly(left, right); priority != 0 {
		return priority
	}
	return compareLastUsedAndOAuth(left, right, preferOAuth)
}

// CompareLoadAware adds the existing load-rate tier before the same LRU/OAuth
// tie-breaker. It deliberately does not randomize equal candidates.
func CompareLoadAware(left, right Candidate, preferOAuth bool) int {
	if priority := ComparePriorityOnly(left, right); priority != 0 {
		return priority
	}
	if left.LoadRate != right.LoadRate {
		if left.LoadRate < right.LoadRate {
			return -1
		}
		return 1
	}
	return compareLastUsedAndOAuth(left, right, preferOAuth)
}

func compareLastUsedAndOAuth(left, right Candidate, preferOAuth bool) int {
	switch {
	case left.LastUsedAt == nil && right.LastUsedAt != nil:
		return -1
	case left.LastUsedAt != nil && right.LastUsedAt == nil:
		return 1
	case left.LastUsedAt == nil && right.LastUsedAt == nil:
		if preferOAuth && left.OAuth != right.OAuth {
			if left.OAuth {
				return -1
			}
			return 1
		}
		return 0
	default:
		if left.LastUsedAt.Before(*right.LastUsedAt) {
			return -1
		}
		if right.LastUsedAt.Before(*left.LastUsedAt) {
			return 1
		}
		return 0
	}
}
