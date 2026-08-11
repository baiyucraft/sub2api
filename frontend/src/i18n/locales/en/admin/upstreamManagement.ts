export default {
  upstreamManagement: {
    title: 'Upstream Management',
    description: 'Manage accounts derived from upstream keys, scheduling, health, TTFT and quality.',
    saved: 'Saved',
    derivedAccount: {
      title: 'Upstream-derived account',
      description: 'The key, endpoint, proxy, multiplier, priority and load factor are derived from Upstream Configuration and cannot be edited here.',
      bulkDescription: 'Keys, endpoints, proxies, multipliers, priorities, load factors and credentials are maintained in Upstream Configuration. Bulk edits here only change runtime scheduling properties.',
      configId: 'Upstream config ID',
      keyId: 'Upstream Key ID',
      site: 'Upstream site'
    },
    saveFailed: 'Failed to save upstream management settings',
    settings: {
      title: 'Upstream management settings',
      open: 'Upstream management settings',
      loadFailed: 'Failed to load upstream management settings',
      invalid: 'Check the first-token threshold, sample count, and probe models.'
    },
    rateTrend: { keyId: 'Key #{id}' },
    ttftGuard: {
      title: 'First-token performance protection',
      description: 'Applies only to upstream accounts; slow accounts are temporarily downgraded without changing the built-in scheduler.',
      enabled: 'Enabled',
      threshold: 'Threshold (seconds)',
      minSamples: 'Minimum samples',
      invalid: 'The threshold must be 5–300 seconds and minimum samples must be an integer from 2–20.',
      tip: {
        definition: 'TTFT is the time from sending a request until the first valid token is received.',
        scope: 'It applies only to upstream accounts and does not change multipliers, priority, billing, or scheduler scoring.',
        normal: 'Degrade when the minimum sample count is reached and EWMA meets the threshold.',
        fast: 'Degrade quickly after two samples at 1.5× the threshold, or immediately after one sample at 3×.',
        immediate: 'A degraded account retains about 5% of scheduling opportunities for recovery probes.',
        recovery: 'Recover after three probes at or below 0.6×; if the entire group is degraded, the original scheduler fails open.'
      }
    },
    probeModels: {
      title: 'Platform probe models',
      description: 'Candidates come from upstream accounts, configured models, and the server catalog. You can enter a new model when needed.',
      invalid: 'All three probe models are required and must be at most 120 characters.',
      search: 'Search models',
      placeholder: 'Select or enter a model',
      useCustom: 'Use custom model: '
    },
    concurrency: {
      sharedTooltip: 'All Keys under this upstream config share one concurrency pool. Source: {source}; effective limit: {limit}',
      sources: { override: 'Manual override', provider: 'Upstream snapshot', unlimited: 'Upstream unlimited', default: 'Default 100' }
    },
    actions: { probe: 'Probe', observation: 'Observe', events: 'Events', probeRecorded: 'Probe result recorded', probeFailed: 'Failed to probe upstream Key', observationFailed: 'Failed to update Key observation' },
    health: {
      keyHealth: 'Key health',
      healthy: 'Healthy',
      degraded: 'Degraded',
      suspended: 'Probe suspended',
      observing: 'Observing',
      recovering: 'Recovering',
      disabled: 'Observation disabled',
      noData: 'No health data',
      reason: 'Reason',
      lastProbe: 'Last probe',
      probeStatus: 'Last probe result',
      lastTraffic: 'Recent traffic evidence',
      schedulable: 'Account schedulable',
      failures: 'Consecutive failures',
      recovery: 'Recovery progress',
      observationDisabled: 'Key observation is disabled; automatic probes will not run.',
      temporarilyExcluded: 'Temporarily excluded by Key health',
      past: 'Past',
      now: 'Now',
      historySummary: '{healthy} / {total} healthy',
      noHistory: 'No continuous health observations yet',
      openDetails: 'Open Key health history details',
      recentObservations: 'Recent health observations',
      retentionHint: 'Keeps the latest 30 observations',
      observedAt: 'Observed at',
      source: 'Evidence source',
      result: 'Result',
      sources: {
        probe: 'Active probe',
        traffic: 'Real request',
        admin: 'Administrator action'
      },
      reasons: {
        probe_succeeded: 'Active probe succeeded',
        traffic_succeeded: 'Real request succeeded',
        observation_enabled: 'Key observation enabled',
        observation_disabled: 'Key observation disabled',
        recovered: 'Health state recovered',
        authentication_failed: 'Upstream authentication failed',
        capacity_limited: 'Upstream capacity or rate limited',
        upstream_server_error: 'Upstream server error',
        probe_transport_error: 'Probe connection or transport failed'
      }
    },
    events: { title: 'Key health details', stateChanges: 'State change events', loading: 'Loading health details…', empty: 'No state change events', loadFailed: 'Failed to load Key health details' },
    columns: { accountKey: 'Account / Key', upstream: 'Upstream', config: 'Config', key: 'Key', modelMapping: 'Model mapping', health: 'Health' },
    export: { action: 'Export upstream status', success: 'Upstream status exported', failed: 'Failed to export upstream status' },
    filters: { allConfigs: 'All upstream configs', allKeys: 'All Keys', loadFailed: 'Failed to load upstream filter options' }
  }
}
