package upstreamcacheguard

import (
	"sort"
	"time"
)

// cachedCascade is called while the engine's native snapshot read lock is held.
// Index only native protected rows, so per-account derivation does not rescan
// every session or every healthy account in every group on a scheduling read.
func (e *Engine) cachedCascade(target Snapshot, settings Settings, now time.Time) Snapshot {
	if !target.Available || settings.Mode == Disabled {
		return target
	}
	nativeByAccount := map[int64][]Snapshot{}
	candidates := map[int64]AccountSnapshot{}
	for _, account := range target.Accounts {
		candidates[account.AccountID] = account
	}
	for _, native := range e.groups {
		if native.GroupID == target.GroupID || !native.Available || native.SamplingGap || !settings.ContainsGroup(native.GroupID) {
			continue
		}
		for _, account := range native.Accounts {
			if account.UpstreamKeyID <= 0 || (account.Phase != Cooling && account.Phase != Trial && account.Phase != Degraded) {
				continue
			}
			source := native
			source.Accounts = []AccountSnapshot{account}
			nativeByAccount[account.AccountID] = append(nativeByAccount[account.AccountID], source)
			if _, exists := candidates[account.AccountID]; !exists {
				// This group has no native evidence for the account. Keep it unknown
				// while exposing the foreign reason to eligible account consumers.
				candidates[account.AccountID] = AccountSnapshot{AccountState: AccountState{
					AccountID: account.AccountID, UpstreamKeyID: account.UpstreamKeyID, Phase: Healthy,
				}}
			}
		}
	}
	resolver := UniformGroupPolicyResolver{Settings: settings}
	for id, sources := range nativeByAccount {
		account := candidates[id]
		linked := DeriveAccountProtection(target.GroupID, id, account.UpstreamKeyID, sources, resolver, now)
		if linked.Protected {
			account.DerivedProtection = &linked
			candidates[id] = account
		}
	}
	target.Accounts = make([]AccountSnapshot, 0, len(candidates))
	for _, account := range candidates {
		// Synthetic rows are useful only when they have a live foreign reason.
		if account.Version > 0 || account.DerivedProtection != nil {
			target.Accounts = append(target.Accounts, account)
		}
	}
	sort.Slice(target.Accounts, func(i, j int) bool { return target.Accounts[i].AccountID < target.Accounts[j].AccountID })
	return target
}

func snapshotProtectsAccount(snapshot Snapshot, accountID, keyID int64) bool {
	if !snapshot.Available {
		return false
	}
	for _, account := range snapshot.Accounts {
		if account.AccountID == accountID && account.UpstreamKeyID == keyID {
			return (account.Phase != "" && account.Phase != Healthy) ||
				(account.DerivedProtection != nil && account.DerivedProtection.Protected)
		}
	}
	return false
}
