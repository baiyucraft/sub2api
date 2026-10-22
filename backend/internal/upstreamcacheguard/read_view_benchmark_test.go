package upstreamcacheguard

import (
	"fmt"
	"testing"
	"time"
)

// This isolates one resident admission read. The baseline is the prior three
// bounded reads, not the unrelated full-directory snapshot API. SQL, refresh
// contention and gateway latency are intentionally outside this measurement.
func BenchmarkAdmissionReadView(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		for _, combined := range []bool{false, true} {
			b.Run(fmt.Sprintf("accounts=%d/combined=%t", size, combined), func(b *testing.B) {
				e := NewEngine(nil)
				s := DefaultSettings()
				s.Mode = Protect
				if err := e.ApplySettings(s); err != nil {
					b.Fatal(err)
				}
				at := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
				e.now = func() time.Time { return at }
				e.loadedAt = at
				for id := 1; id <= size; id++ {
					a := AccountState{AccountID: int64(id), UpstreamKeyID: 1, Phase: Healthy, ValidRequests: 100, TotalInputTokens: 10000, CacheReadTokens: 9000}
					e.preparePolicies(&a)
					e.accounts[accountIdentity(int64(id), 1)] = AccountSnapshot{AccountState: a, Version: 1, LoadedAt: at}
				}
				r := Request{ID: "read", LogicalSessionDigest: "resident", GroupID: 1, AccountID: 1, UpstreamKeyID: 1, Model: "model", Protocol: "openai", StartedAt: at}
				routeKey := WarmRouteKey(r)
				e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))] = SessionState{Key: SessionKey(r), LastAccountID: r.AccountID, LastModel: r.Model, LastProtocol: r.Protocol, LastRouteKey: routeKey,
					Routes: map[string]SessionRoute{routeKey: {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, LastSuccessfulAt: at.Add(-time.Second), PolicySignature: PolicySignature(s), HighCacheSeen: true}}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if combined {
						view := e.CachedAdmissionReadView(r)
						if !view.Consistent || !view.Account.Available || !view.Session.Known {
							b.Fatal("incomplete combined view")
						}
					} else {
						metadata := e.CachedMetadataAt(r.GroupID, at)
						account, available := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, at)
						session := e.CachedSessionReadAt(r, at)
						if !metadata.Available || !available || !account.Available || !session.Known {
							b.Fatal("incomplete separate view")
						}
					}
				}
			})
		}
	}
}
