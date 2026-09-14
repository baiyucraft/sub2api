package service

import (
	"math/rand"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling/legacy"
)

func isAccountPreferredForGroup(account *Account, groupID *int64) bool {
	if account == nil || groupID == nil {
		return false
	}
	for _, accountGroup := range account.AccountGroups {
		if accountGroup.GroupID == *groupID && accountGroup.SchedulerPreferred {
			return true
		}
	}
	return false
}

func partitionPreferredAccountsForGroup(accounts []Account, groupID *int64) ([]Account, []Account) {
	ordinary := make([]Account, 0, len(accounts))
	if len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	views := make([]legacy.Candidate, len(accounts))
	for i := range accounts {
		views[i] = legacyCandidateView(&accounts[i], isAccountPreferredForGroup(&accounts[i], groupID), 0)
	}
	preferredIndexes, ordinaryIndexes := (legacy.PreferredPolicy{}).PartitionPreferredIndices(views)
	preferred := make([]Account, 0, len(preferredIndexes))
	ordinary = make([]Account, 0, len(ordinaryIndexes))
	for _, index := range preferredIndexes {
		preferred = append(preferred, accounts[index])
	}
	for _, index := range ordinaryIndexes {
		ordinary = append(ordinary, accounts[index])
	}
	return preferred, ordinary
}

func partitionPreferredAccountPointersForGroup(accounts []*Account, groupID *int64) ([]*Account, []*Account) {
	ordinary := make([]*Account, 0, len(accounts))
	if len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	views := make([]legacy.Candidate, len(accounts))
	for i, account := range accounts {
		views[i] = legacyCandidateView(account, isAccountPreferredForGroup(account, groupID), 0)
	}
	preferredIndexes, ordinaryIndexes := (legacy.PreferredPolicy{}).PartitionPreferredIndices(views)
	preferred := make([]*Account, 0, len(preferredIndexes))
	ordinary = make([]*Account, 0, len(ordinaryIndexes))
	for _, index := range preferredIndexes {
		preferred = append(preferred, accounts[index])
	}
	for _, index := range ordinaryIndexes {
		ordinary = append(ordinary, accounts[index])
	}
	return preferred, ordinary
}

func partitionPreferredAccountWithLoadForGroup(accounts []accountWithLoad, groupID *int64) ([]accountWithLoad, []accountWithLoad) {
	ordinary := make([]accountWithLoad, 0, len(accounts))
	if len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	views := make([]legacy.Candidate, len(accounts))
	for i, account := range accounts {
		loadRate := 0
		if account.loadInfo != nil {
			loadRate = account.loadInfo.LoadRate
		}
		views[i] = legacyCandidateView(account.account, isAccountPreferredForGroup(account.account, groupID), loadRate)
	}
	preferredIndexes, ordinaryIndexes := (legacy.PreferredPolicy{}).PartitionPreferredIndices(views)
	preferred := make([]accountWithLoad, 0, len(preferredIndexes))
	ordinary = make([]accountWithLoad, 0, len(ordinaryIndexes))
	for _, index := range preferredIndexes {
		preferred = append(preferred, accounts[index])
	}
	for _, index := range ordinaryIndexes {
		ordinary = append(ordinary, accounts[index])
	}
	return preferred, ordinary
}

func appendPreferredAccountPointersFirst(accounts []*Account, groupID *int64) []*Account {
	preferred, ordinary := partitionPreferredAccountPointersForGroup(accounts, groupID)
	if len(preferred) == 0 {
		return ordinary
	}
	ordered := make([]*Account, 0, len(preferred)+len(ordinary))
	ordered = append(ordered, preferred...)
	ordered = append(ordered, ordinary...)
	return ordered
}

func compareAccountSchedulingPriorityOnly(left, right *Account) int {
	if left == nil || right == nil {
		return 0
	}
	return legacy.ComparePriorityOnly(legacyCandidateView(left, false, 0), legacyCandidateView(right, false, 0))
}

func compareLegacyAccountPriorityAndLastUsed(left, right *Account, preferOAuth bool) int {
	return legacy.ComparePriorityAndLastUsed(legacyCandidateView(left, false, 0), legacyCandidateView(right, false, 0), preferOAuth)
}

func compareLegacyAccountLoadAware(left, right *Account, leftLoadRate, rightLoadRate int, preferOAuth bool) int {
	return legacy.CompareLoadAware(legacyCandidateView(left, false, leftLoadRate), legacyCandidateView(right, false, rightLoadRate), preferOAuth)
}

// compareAccountSchedulingTierIgnoringRate is the stable, non-billing portion
// of the account ordering used inside the preferred pool.  A preferred pool
// must not be reordered by either group or upstream rate; the normal priority
// signal remains meaningful within the pool.
func compareAccountSchedulingTierIgnoringRate(left, right *Account) int {
	return compareAccountSchedulingPriorityOnly(left, right)
}

func comparePreferredAwareAccountSchedulingTier(left, right *Account, groupID *int64) int {
	leftPreferred := isAccountPreferredForGroup(left, groupID)
	rightPreferred := isAccountPreferredForGroup(right, groupID)
	if leftPreferred != rightPreferred {
		if leftPreferred {
			return -1
		}
		return 1
	}
	if left == nil || right == nil {
		return 0
	}
	leftView := legacyCandidateView(left, leftPreferred, 0)
	rightView := legacyCandidateView(right, rightPreferred, 0)
	return legacy.ComparePreferredAware(leftView, rightView)
}

func sortPreferredAccountPointersByPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if tier := legacy.ComparePriorityAndLastUsed(legacyCandidateView(a, false, 0), legacyCandidateView(b, false, 0), preferOAuth); tier != 0 {
			return tier < 0
		}
		return false
	})
	shuffleWithinPreferredPriorityAndLastUsed(accounts, preferOAuth)
}

func shuffleWithinPreferredPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && samePreferredAccountGroup(accounts[i], accounts[j]) {
			j++
		}
		if j-i > 1 {
			if preferOAuth {
				oauth := make([]*Account, 0, j-i)
				others := make([]*Account, 0, j-i)
				for _, acc := range accounts[i:j] {
					if acc.Type == AccountTypeOAuth {
						oauth = append(oauth, acc)
					} else {
						others = append(others, acc)
					}
				}
				if len(oauth) > 1 {
					rand.Shuffle(len(oauth), func(a, b int) { oauth[a], oauth[b] = oauth[b], oauth[a] })
				}
				if len(others) > 1 {
					rand.Shuffle(len(others), func(a, b int) { others[a], others[b] = others[b], others[a] })
				}
				copy(accounts[i:], oauth)
				copy(accounts[i+len(oauth):], others)
			} else {
				rand.Shuffle(j-i, func(a, b int) {
					accounts[i+a], accounts[i+b] = accounts[i+b], accounts[i+a]
				})
			}
		}
		i = j
	}
}

func samePreferredAccountGroup(a, b *Account) bool {
	if compareAccountSchedulingTierIgnoringRate(a, b) != 0 {
		return false
	}
	return legacy.SameLastUsedAt(lastUsedAt(a), lastUsedAt(b))
}

func sortPreferredAccountsWithLoadByLoadAwareness(accounts []accountWithLoad, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if tier := legacy.CompareLoadAware(legacyCandidateView(a.account, false, accountLoadRate(a.loadInfo)), legacyCandidateView(b.account, false, accountLoadRate(b.loadInfo)), preferOAuth); tier != 0 {
			return tier < 0
		}
		return false
	})
	shuffleWithinPreferredSortGroups(accounts, preferOAuth)
}

func accountLoadRate(load *AccountLoadInfo) int {
	if load == nil {
		return 0
	}
	return load.LoadRate
}

func shuffleWithinPreferredSortGroups(accounts []accountWithLoad, preferOAuth bool) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && samePreferredAccountWithLoadGroup(accounts[i], accounts[j]) {
			j++
		}
		if j-i > 1 {
			if preferOAuth {
				oauth := make([]accountWithLoad, 0, j-i)
				others := make([]accountWithLoad, 0, j-i)
				for _, acc := range accounts[i:j] {
					if acc.account.Type == AccountTypeOAuth {
						oauth = append(oauth, acc)
					} else {
						others = append(others, acc)
					}
				}
				if len(oauth) > 1 {
					rand.Shuffle(len(oauth), func(a, b int) { oauth[a], oauth[b] = oauth[b], oauth[a] })
				}
				if len(others) > 1 {
					rand.Shuffle(len(others), func(a, b int) { others[a], others[b] = others[b], others[a] })
				}
				copy(accounts[i:], oauth)
				copy(accounts[i+len(oauth):], others)
			} else {
				rand.Shuffle(j-i, func(a, b int) {
					accounts[i+a], accounts[i+b] = accounts[i+b], accounts[i+a]
				})
			}
		}
		i = j
	}
}

func samePreferredAccountWithLoadGroup(a, b accountWithLoad) bool {
	leftLoadRate, rightLoadRate := 0, 0
	if a.loadInfo != nil {
		leftLoadRate = a.loadInfo.LoadRate
	}
	if b.loadInfo != nil {
		rightLoadRate = b.loadInfo.LoadRate
	}
	if compareAccountSchedulingTierIgnoringRate(a.account, b.account) != 0 || leftLoadRate != rightLoadRate {
		return false
	}
	return legacy.SameLastUsedAt(lastUsedAt(a.account), lastUsedAt(b.account))
}

func lastUsedAt(account *Account) *time.Time {
	if account == nil || account.LastUsedAt == nil {
		return nil
	}
	value := *account.LastUsedAt
	return &value
}

func sortOpenAILegacyLoadPool(
	accounts []accountWithLoad,
	preferredPool bool,
	preferOAuth bool,
	rateOrder openAILegacyUpstreamRateOrder,
	requireCompact bool,
) {
	compactTier := func(account *Account) int {
		if !requireCompact {
			return 0
		}
		return openAICompactSupportTier(account)
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if left, right := compactTier(a.account), compactTier(b.account); left != right {
			return left > right
		}
		if preferredPool {
			if tier := compareAccountSchedulingPriorityOnly(a.account, b.account); tier != 0 {
				return tier < 0
			}
		} else {
			if tier := compareAccountSchedulingTier(a.account, b.account); tier != 0 {
				return tier < 0
			}
			if rateCmp := rateOrder.compare(a.account, b.account); rateCmp != 0 {
				return rateCmp < 0
			}
		}
		if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
			return a.loadInfo.LoadRate < b.loadInfo.LoadRate
		}
		switch {
		case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
			return true
		case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
			return false
		case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
			return preferOAuth && a.account.Type != b.account.Type && a.account.Type == AccountTypeOAuth
		default:
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})
}
