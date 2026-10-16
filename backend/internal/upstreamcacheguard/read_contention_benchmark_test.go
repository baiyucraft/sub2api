package upstreamcacheguard

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkCacheReadPublicationContention isolates reader/publication locking.
// It deliberately does not model SQL, gateway scheduling, TTFT, or completion
// transactions. The baseline is the legacy read pattern on this same engine.
func BenchmarkCacheReadPublicationContention(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		for _, page := range []int{20, 100} {
			for _, batch := range []bool{false, true} {
				name := fmt.Sprintf("accounts=%d/page=%d/batch=%t", size, page, batch)
				b.Run(name, func(b *testing.B) {
					e := NewEngine(nil)
					s := DefaultSettings()
					s.Mode = Protect
					if err := e.ApplySettings(s); err != nil {
						b.Fatal(err)
					}
					at := time.Now().UTC()
					e.now = func() time.Time { return at }
					e.loadedAt = at
					for id := 1; id <= size; id++ {
						a := AccountState{AccountID: int64(id), UpstreamKeyID: 1, Phase: Healthy, ValidRequests: 100, TotalInputTokens: 10000, CacheReadTokens: 9000}
						e.preparePolicies(&a)
						e.accounts[accountIdentity(int64(id), 1)] = AccountSnapshot{AccountState: a, Version: 1, LoadedAt: at}
					}
					candidates := make([]Candidate, page)
					for i := range candidates {
						candidates[i] = Candidate{AccountID: int64(i + 1), UpstreamKeyID: 1}
					}
					stop := make(chan struct{})
					var workers sync.WaitGroup
					var publicationWait, publications, refreshWait, refreshes atomic.Int64
					for worker := 0; worker < 4; worker++ {
						workers.Add(1)
						go func(id int) {
							defer workers.Done()
							tick := time.NewTicker(time.Millisecond)
							defer tick.Stop()
							for {
								select {
								case <-stop:
									return
								case <-tick.C:
									started := benchmarkClockNS()
									e.mu.Lock()
									publicationWait.Add(benchmarkClockNS() - started)
									key := accountIdentity(int64(id+1), 1)
									a := e.accounts[key]
									a.Version++
									e.accounts[key] = a
									e.mu.Unlock()
									publications.Add(1)
								}
							}
						}(worker)
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						tick := time.NewTicker(time.Second)
						defer tick.Stop()
						for {
							select {
							case <-stop:
								return
							case <-tick.C:
								started := benchmarkClockNS()
								e.mu.Lock()
								refreshWait.Add(benchmarkClockNS() - started)
								e.loadedAt = at
								e.mu.Unlock()
								refreshes.Add(1)
							}
						}
					}()
					var sampleMu sync.Mutex
					var samples []int64
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						local := make([]int64, 0, 4096)
						seen := 0
						for pb.Next() {
							started := benchmarkClockNS()
							if batch {
								e.CachedMetadataAt(1, at)
								e.CachedCandidatesAt(1, candidates, at)
								e.CachedSharedAccountsAt(candidates, at)
							} else {
								e.CachedSnapshot(1)
								for _, candidate := range candidates {
									e.CachedSharedAccount(candidate.AccountID, candidate.UpstreamKeyID)
								}
							}
							elapsed := benchmarkClockNS() - started
							if len(local) < cap(local) {
								local = append(local, elapsed)
							} else {
								local[seen%len(local)] = elapsed
							}
							seen++
						}
						sampleMu.Lock()
						samples = append(samples, local...)
						sampleMu.Unlock()
					})
					b.StopTimer()
					close(stop)
					workers.Wait()
					sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
					for _, percentile := range []int{50, 95, 99} {
						if len(samples) != 0 {
							b.ReportMetric(float64(samples[(len(samples)-1)*percentile/100]), fmt.Sprintf("p%d-ns", percentile))
						}
					}
					if n := publications.Load(); n != 0 {
						b.ReportMetric(float64(publicationWait.Load())/float64(n), "publish-lock-wait-ns")
						b.ReportMetric(float64(n), "publications")
					}
					if n := refreshes.Load(); n != 0 {
						b.ReportMetric(float64(refreshWait.Load())/float64(n), "refresh-lock-wait-ns")
						b.ReportMetric(float64(n), "refreshes")
					}
				})
			}
		}
	}
}
