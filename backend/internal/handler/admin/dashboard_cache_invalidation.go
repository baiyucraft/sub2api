package admin

// InvalidateDashboardQueryCaches clears dashboard and usage snapshots whose
// account-cost totals can include the extra-cost ledger.
func InvalidateDashboardQueryCaches() {
	for _, cache := range []*snapshotCache{
		usageStatsCache,
		dashboardTrendCache,
		dashboardModelStatsCache,
		dashboardGroupStatsCache,
		dashboardUsersTrendCache,
		dashboardAPIKeysTrendCache,
		dashboardSnapshotV2Cache,
		dashboardUsersRankingCache,
		dashboardBatchUsersUsageCache,
		dashboardBatchAPIKeysUsageCache,
	} {
		cache.Clear()
	}
}
