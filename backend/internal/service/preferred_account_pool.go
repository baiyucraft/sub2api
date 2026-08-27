package service

import (
	"math/rand"
	"sort"
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
	if groupID == nil || len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	preferred := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if isAccountPreferredForGroup(&account, groupID) {
			preferred = append(preferred, account)
			continue
		}
		ordinary = append(ordinary, account)
	}
	return preferred, ordinary
}

func partitionPreferredAccountPointersForGroup(accounts []*Account, groupID *int64) ([]*Account, []*Account) {
	ordinary := make([]*Account, 0, len(accounts))
	if groupID == nil || len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	preferred := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		if isAccountPreferredForGroup(account, groupID) {
			preferred = append(preferred, account)
			continue
		}
		ordinary = append(ordinary, account)
	}
	return preferred, ordinary
}

func partitionPreferredAccountWithLoadForGroup(accounts []accountWithLoad, groupID *int64) ([]accountWithLoad, []accountWithLoad) {
	ordinary := make([]accountWithLoad, 0, len(accounts))
	if groupID == nil || len(accounts) == 0 {
		return nil, append(ordinary, accounts...)
	}
	preferred := make([]accountWithLoad, 0, len(accounts))
	for _, account := range accounts {
		if isAccountPreferredForGroup(account.account, groupID) {
			preferred = append(preferred, account)
			continue
		}
		ordinary = append(ordinary, account)
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
	if left.Priority < right.Priority {
		return -1
	}
	if left.Priority > right.Priority {
		return 1
	}
	return 0
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
	if leftPreferred {
		return compareAccountSchedulingTierIgnoringRate(left, right)
	}
	return compareAccountSchedulingTier(left, right)
}

func sortPreferredAccountPointersByPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if tier := compareAccountSchedulingTierIgnoringRate(a, b); tier != 0 {
			return tier < 0
		}
		switch {
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return true
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return false
		case a.LastUsedAt == nil && b.LastUsedAt == nil:
			if preferOAuth && a.Type != b.Type {
				return a.Type == AccountTypeOAuth
			}
			return false
		default:
			return a.LastUsedAt.Before(*b.LastUsedAt)
		}
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
	return sameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

func sortPreferredAccountsWithLoadByLoadAwareness(accounts []accountWithLoad, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if tier := compareAccountSchedulingTierIgnoringRate(a.account, b.account); tier != 0 {
			return tier < 0
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
			if preferOAuth && a.account.Type != b.account.Type {
				return a.account.Type == AccountTypeOAuth
			}
			return false
		default:
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})
	shuffleWithinPreferredSortGroups(accounts, preferOAuth)
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
	if compareAccountSchedulingTierIgnoringRate(a.account, b.account) != 0 {
		return false
	}
	if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
		return false
	}
	return sameLastUsedAt(a.account.LastUsedAt, b.account.LastUsedAt)
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
