export default {
    upstreamDashboard: {
      title: 'Upstream Dashboard', description: 'Review traffic, health, performance, and estimated returns by upstream configuration.', empty: 'No upstream configurations match the filters.', openDetail: 'Open details for {name}', noReason: 'No reason recorded', estimatedUnavailable: 'Estimate unavailable',
      windows: { '1h': 'Last hour', '24h': 'Last 24 hours', '7d': 'Last 7 days', '15d': 'Last 15 days', '30d': 'Last 30 days' },
      filters: { allProviders: 'All providers', allStatuses: 'All statuses' },
      status: { operational: 'Operational', degraded: 'Degraded', critical: 'Critical', disabled: 'Disabled', data_insufficient: 'Insufficient data' },
      metrics: { requests: 'Requests', successRate: 'Success rate', ttft: 'P50 TTFT', latency: 'P50 latency', p95ttft: 'P95 TTFT', p95latency: 'P95 latency', failed: 'Failed', timeouts: 'Timeouts', accounts: 'Schedulable accounts', probeSamples: 'Probe samples', estimatedProfit: 'Estimated gross profit' },
      sections: { traffic: 'Live traffic', probe: 'Active probes', profit: 'Cost & returns' },
      actions: { channels: 'View channels', accounts: 'View accounts', usage: 'View usage' }
    },
    upstreamChannels: { title: 'Upstream Channels', description: 'Manage upstream configurations, key synchronization, and channel operations.' },
    upstreamAccounts: { title: 'Upstream Accounts', description: 'Manage upstream accounts, health, and capabilities.' }
}
