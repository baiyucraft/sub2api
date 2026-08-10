export default {
  upstreamManagement: {
    title: 'Upstream Management',
    description: 'Manage accounts derived from upstream keys, scheduling, health, TTFT and quality.',
    saved: 'Saved',
    derivedAccount: {
      title: 'Upstream-derived account',
      description: 'The key, endpoint, proxy, multiplier, priority and load factor are derived from Upstream Configuration and cannot be edited here.',
      bulkDescription: 'Keys, endpoints, proxies, multipliers, priorities, load factors and credentials are maintained in Upstream Configuration. Bulk edits here only change runtime scheduling properties.'
    },
    saveFailed: 'Failed to save upstream management settings',
    ttftGuard: {
      title: 'First-token performance protection',
      description: 'Applies only to upstream accounts; slow accounts are temporarily downgraded without changing the built-in scheduler.',
      enabled: 'Enabled',
      threshold: 'Threshold (seconds)',
      minSamples: 'Minimum samples',
      invalid: 'The threshold must be 5–300 seconds and minimum samples must be an integer from 2–20.'
    },
    probeModels: { title: 'Platform probe models', invalid: 'All three probe models are required and must be at most 255 characters.' },
    actions: { probe: 'Probe', observation: 'Observe', events: 'Events', probeRecorded: 'Probe result recorded', probeFailed: 'Failed to probe upstream Key', observationFailed: 'Failed to update Key observation' },
    health: { healthy: 'Healthy', degraded: 'Degraded', suspended: 'Suspended', observing: 'Observing', recovering: 'Recovering', disabled: 'Disabled', reason: 'Reason', lastProbe: 'Last probe', probeStatus: 'Last probe result', lastTraffic: 'Recent traffic evidence', schedulable: 'Account schedulable', failures: 'Consecutive failures', recovery: 'Recovery progress', temporarilyExcluded: 'Temporarily excluded by Key health' },
    events: { title: 'Key health events', loading: 'Loading events…', empty: 'No health events', loadFailed: 'Failed to load Key health events' },
    columns: { accountKey: 'Account / Key', upstream: 'Upstream', config: 'Config', key: 'Key', modelMapping: 'Model mapping' },
    export: { action: 'Export upstream status', success: 'Upstream status exported', failed: 'Failed to export upstream status' },
    filters: { allConfigs: 'All upstream configs', allKeys: 'All Keys', loadFailed: 'Failed to load upstream filter options' }
  }
}
