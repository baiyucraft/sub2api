package service

import (
	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/Wei-Shaw/sub2api/internal/forkscheduling/legacy"
)

const (
	UpstreamSchedulerConcurrencyOverrideKey = forkscheduling.UpstreamSchedulerConcurrencyOverrideKey

	ConcurrencyTargetAccount  = forkscheduling.ConcurrencyTargetAccount
	ConcurrencyTargetUpstream = forkscheduling.ConcurrencyTargetUpstream

	UpstreamConcurrencySourceOverride  = forkscheduling.UpstreamConcurrencySourceOverride
	UpstreamConcurrencySourceProvider  = forkscheduling.UpstreamConcurrencySourceProvider
	UpstreamConcurrencySourceUnlimited = forkscheduling.UpstreamConcurrencySourceUnlimited
	UpstreamConcurrencySourceDefault   = forkscheduling.UpstreamConcurrencySourceDefault

	DefaultUpstreamSchedulerConcurrency = forkscheduling.DefaultUpstreamSchedulerConcurrency
	MaxUpstreamSchedulerConcurrency     = forkscheduling.MaxUpstreamSchedulerConcurrency
)

type UpstreamSchedulerConcurrency = forkscheduling.UpstreamSchedulerConcurrency

// extraValue remains in the service package because upstream configuration
// parsing also uses it; the scheduling normalization itself lives in legacy.
func extraValue(extra map[string]any, key string) any {
	if extra == nil {
		return nil
	}
	return extra[key]
}

// ResolveUpstreamSchedulerConcurrency is the single authority for resolving
// the shared concurrency limit of an upstream config.
func ResolveUpstreamSchedulerConcurrency(extra map[string]any) UpstreamSchedulerConcurrency {
	return legacy.ResolveUpstreamSchedulerConcurrency(extra)
}

type ConcurrencyTarget = forkscheduling.ConcurrencyTarget

func (a *Account) SchedulingConcurrencyTarget() ConcurrencyTarget {
	if a == nil {
		return ConcurrencyTarget{Kind: ConcurrencyTargetAccount}
	}
	var upstreamConfigID int64
	if a.UpstreamConfigID != nil {
		upstreamConfigID = *a.UpstreamConfigID
	}
	return legacy.ResolveConcurrencyTarget(forkscheduling.ConcurrencyAccountView{
		AccountID:         a.ID,
		UpstreamConfigID:  upstreamConfigID,
		AccountLimit:      a.Concurrency,
		UpstreamLimit:     a.UpstreamConcurrencyLimit,
		UpstreamUnlimited: a.UpstreamConcurrencyUnlimited,
	})
}

func AccountConcurrencyLoadDescriptor(account *Account) AccountWithConcurrency {
	if account == nil {
		return AccountWithConcurrency{}
	}
	target := account.SchedulingConcurrencyTarget()
	return AccountWithConcurrency{
		ID:             account.ID,
		MaxConcurrency: target.Limit,
		TargetKind:     target.Kind,
		TargetID:       target.ID,
	}
}

// AccountSchedulingLoadDescriptor keeps LoadFactor as a scheduling-only
// virtual capacity for ordinary accounts. Upstream-bound accounts always use
// the shared upstream concurrency limit for both capacity and load ranking.
func AccountSchedulingLoadDescriptor(account *Account) AccountWithConcurrency {
	if account == nil {
		return AccountWithConcurrency{}
	}
	target := account.SchedulingConcurrencyTarget()
	maxConcurrency := target.Limit
	if target.Kind == ConcurrencyTargetAccount {
		maxConcurrency = account.EffectiveLoadFactor()
	}
	return AccountWithConcurrency{
		ID:             account.ID,
		MaxConcurrency: maxConcurrency,
		TargetKind:     target.Kind,
		TargetID:       target.ID,
	}
}
