package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProtectionCandidateTierPreservesExclusionOrderAndIsolation(t *testing.T) {
	ctx := withProtectionCandidateReads(context.Background())
	group := int64(2)
	owner := &OpenAIGatewayService{}
	key := protectionCandidateKey(owner, &group, PlatformOpenAI, false)
	accounts := []Account{{ID: 5}, {ID: 2}, {ID: 7}, {ID: 4}, {ID: 8}}
	rememberProtectionCandidates(ctx, key, accounts, false)
	scope := map[int64]struct{}{5: {}, 7: {}, 4: {}}
	ctx = withProtectionCandidateScope(ctx, scope)
	tier := withProtectionCandidateTier(ctx, scope, []int64{4, 5, 4})
	got, _, known := readProtectionCandidates(tier, key)
	require.True(t, known)
	require.Equal(t, []int64{5, 2, 4, 8}, []int64{got[0].ID, got[1].ID, got[2].ID, got[3].ID})
	got[0].ID = 99
	again, _, _ := readProtectionCandidates(ctx, key)
	require.Equal(t, int64(5), again[0].ID, "a tier cannot mutate a later attempt's source order")
	_, _, known = readProtectionCandidates(context.Background(), key)
	require.False(t, known, "completed requests never share candidate hints")
	group++
	_, _, known = readProtectionCandidates(ctx, protectionCandidateKey(owner, &group, PlatformOpenAI, false))
	require.False(t, known, "group identity belongs to the query key")
}

func TestProtectionBudgetSharedAndOrdinaryContextRetained(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := WithUpstreamCacheGuardScheduling(parent, 1)
	first, firstCancel := cacheGuardDatabaseContext(ctx)
	deadline, ok := first.Deadline()
	require.True(t, ok)
	firstCancel()
	second, secondCancel := cacheGuardDatabaseContext(ctx)
	defer secondCancel()
	secondDeadline, _ := second.Deadline()
	require.False(t, secondDeadline.Before(deadline), "later stages receive the unused database allowance")
	secondCancel()
	identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	identity.budget.mu.Lock()
	identity.budget.activeElapsed = cacheGuardDatabaseBudget
	identity.budget.mu.Unlock()
	expired, expiredCancel := cacheGuardDatabaseContext(ctx)
	require.ErrorIs(t, expired.Err(), context.DeadlineExceeded)
	expiredCancel()
	require.NoError(t, ctx.Err(), "protection budget cannot cancel ordinary routing")
	require.True(t, identity.budget.exhausted.Load())
	short, shortCancel := context.WithTimeout(WithUpstreamCacheGuardScheduling(parent, 2), time.Millisecond)
	defer shortCancel()
	child, childCancel := cacheGuardDatabaseContext(short)
	defer childCancel()
	childDeadline, _ := child.Deadline()
	require.True(t, childDeadline.Before(time.Now().Add(100*time.Millisecond)))
}

func TestProtectionTierExclusionsMatchOriginal(t *testing.T) {
	for _, migration := range []bool{false, true} {
		p := cacheGuardPreference{Migration: migration, ScopeIDs: map[int64]struct{}{1: {}, 2: {}, 3: {}}, CandidateIDs: []int64{1, 2, 3, 4, 5}}
		base := map[int64]struct{}{2: {}, 9: {}}
		next := p.tierExclusions(base)
		for _, tier := range [][]int64{{1, 2}, {3}, {4, 5}, {1}, nil} {
			require.Equal(t, p.exclusionsFor(tier, base), next(tier))
		}
		require.Equal(t, map[int64]struct{}{2: {}, 9: {}}, base)
	}
}

func BenchmarkProtectionCandidateTiers(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			accounts := make([]Account, size)
			scope := make(map[int64]struct{}, size)
			for i := range accounts {
				accounts[i].ID = int64(i + 1)
				scope[int64(i+1)] = struct{}{}
			}
			ctx := withProtectionCandidateReads(context.Background())
			key := protectionCandidateKey(&OpenAIGatewayService{}, nil, PlatformOpenAI, false)
			rememberProtectionCandidates(ctx, key, accounts, false)
			ctx = withProtectionCandidateScope(ctx, scope)
			tier := withProtectionCandidateTier(ctx, scope, []int64{int64(size)})
			readProtectionCandidates(tier, key)
			b.Run("baseline_scan", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					out := make([]Account, 0, 1)
					for j := range accounts {
						if accounts[j].ID == int64(size) {
							out = append(out, accounts[j])
						}
					}
					if len(out) != 1 {
						b.Fatal("lost candidate")
					}
				}
			})
			b.Run("prepared_index", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					out, _, _ := readProtectionCandidates(tier, key)
					if len(out) != 1 {
						b.Fatal("lost candidate")
					}
				}
			})
		})
	}
}
