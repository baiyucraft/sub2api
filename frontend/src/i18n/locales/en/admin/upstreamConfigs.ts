export default {
  upstreamConfigs: {
    title: 'Upstream Configs',
    description: 'Maintain upstream relay login state, proxy, and keys in one place; accounts only bind to a selected upstream config.',
    searchPlaceholder: 'Search name or Base URL',
    sensitiveHint: 'These credentials are stored as highly sensitive upstream secrets and are only used to sync keys, rates, quota, and refresh JWTs. Passwords, JWTs, refresh tokens, cookies, and API keys are never returned in plaintext.',
    columns: {
      name: 'Name',
      provider: 'Type',
      baseUrl: 'Base URL',
      balance: 'CNY Balance',
      rates: 'Rate Summary',
      authMode: 'Auth',
      credentials: 'Credentials',
      lastSync: 'Last Sync',
      actions: 'Actions'
    },
    balance: {
      totalRecharged: 'Total recharged {amount}',
      totalQuota: 'Total quota {amount}',
      totalUsed: 'Total used {amount}',
      lowBalance: 'Low balance',
      rawBalance: 'Raw balance: {amount}',
      rawUsed: 'Raw used: {amount}',
      rawTotal: 'Raw total: {amount}',
      email: 'Upstream email: {email}',
      syncedAt: 'Quota synced: {time}',
      error: 'Quota error: {error}'
    },
    rates: {
      raw: 'Raw rate {value}',
      cost: 'Cost rate {value}',
      recharge: 'Recharge rate {value}x'
    },
    providers: {
      sub2api: 'sub2api',
      newapi: 'newapi',
      other: 'Other'
    },
    filters: {
      allProviders: 'All Types'
    },
    authModes: {
      userLogin: 'Email / Password',
      manualJwt: 'Manual JWT',
      userLoginShort: 'Login',
      manualJwtShort: 'JWT'
    },
    credentialStatus: {
      configured: 'Configured',
      missing: 'Missing',
      email: 'Email {status}',
      username: 'Username {status}',
      password: 'Password {status}',
      accessToken: 'JWT {status}',
      refreshToken: 'Refresh {status}'
    },
    actions: {
      create: 'Add Upstream',
      test: 'Test Connection',
      syncKeys: 'Sync Upstream',
      syncAll: 'Sync All',
      syncRuns: 'Sync Runs',
      events: 'Events',
      costTrend: 'Cost Trend',
      settings: 'Upstream Settings',
      openDashboard: 'Open Upstream Dashboard',
      saving: 'Saving...'
    },
    dialog: {
      createTitle: 'Add Upstream',
      editTitle: 'Edit Upstream Config'
    },
    tokenAssistant: {
      open: 'Login Helper',
      title: 'Upstream Login Helper',
      inlineHint: 'Copy tokens after logging in upstream; parse locally and fill this form.',
      securityHint: 'Pasted content is parsed locally in the browser. It is not saved automatically or sent to the backend; review candidates before applying.',
      loginPageHint: 'Log in on the upstream site first, then copy localStorage, a Network response, or Authorization content.',
      openLoginPage: 'Open Login Page',
      pasteLabel: 'Paste Login Data',
      pastePlaceholder: 'Supports Bearer, JWT, raw rt_ refresh tokens, JSON with access_token / refresh_token, or browser localStorage table rows (auth_token / refresh_token)',
      accessCandidate: 'Access Token Candidate',
      refreshCandidate: 'Refresh Token Candidate',
      doNotApply: 'Do not apply',
      candidateLabel: '{source} / suffix {suffix}',
      noTokenFound: 'No usable token was recognized.',
      noSelection: 'Select at least one token candidate.',
      apply: 'Apply to Form',
      applied: 'Token values filled. Review and save to persist them.',
      invalidBaseUrl: 'Enter a valid upstream Base URL first.',
      jwtUnverifiedNoExp: 'This JWT candidate was parsed locally; its signature and validity were not verified.',
      jwtExpiresAt: 'Locally parsed JWT expiry: {time}. Signature and permission must still be verified upstream.',
      jwtExpired: 'Local parsing shows this JWT expired at {time}; a refresh token can still be saved if present.',
      sources: {
        bearer: 'Bearer',
        jwt: 'JWT',
        rawRefresh: 'Sub2API Refresh Token'
      }
    },
    fields: {
      name: 'Name',
      provider: 'Type',
      baseUrl: 'Base URL',
      authMode: 'Auth Mode',
      proxy: 'Proxy',
      rechargeRate: 'Recharge Rate',
      rechargeRateHint: 'Factor converting upstream balance into actual cost. Cost rate = raw rate multiplied by recharge rate.',
      balanceToCnyRate: 'Balance to CNY Rate',
      balanceToCnyRatePlaceholder: 'Use upstream rate when empty',
      balanceToCnyRateHint: 'Admin business conversion rate. It overrides upstream display rates; leave empty to use the upstream rate.',
      proxyId: 'Proxy ID',
      proxyPlaceholder: 'Empty means none',
      loginEmail: 'Login Email',
      loginUsername: 'Login Username',
      loginPassword: 'Login Password',
      accessToken: 'Access Token',
      refreshToken: 'Refresh Token',
      keepPasswordPlaceholder: 'Leave blank to keep saved password',
      keepAccessTokenPlaceholder: 'Leave blank to keep saved JWT',
      keepRefreshTokenPlaceholder: 'Leave blank to keep saved refresh token'
    },
    emptyTitle: 'No upstream configs',
    emptyDescription: 'Create an upstream site, then bind accounts to keys from that site in account management.',
    delete: {
      title: 'Delete Upstream Config',
      message: "Delete upstream config '{name}'? The backend will reject deletion if accounts are still linked."
    },
    messages: {
      loadFailed: 'Failed to load upstream configs',
      loadProxiesFailed: 'Failed to load proxies',
      created: 'Upstream config created',
      updated: 'Upstream config updated',
      savedAndSynced: 'Saved and synced. Found {keys} key(s), updated {accounts} account(s)',
      savedAndSyncedPartial: 'Saved, but sync only partially completed. Found {keys} key(s), updated {accounts} account(s)',
      savedButSyncFailed: 'Saved, but sync failed: {error}',
      saveFailed: 'Failed to save upstream config',
      newapiUsernameRequired: 'NewAPI upstream requires a login username',
      newapiPasswordRequired: 'NewAPI upstream requires a login password',
      deleted: 'Upstream config deleted',
      deleteFailed: 'Failed to delete upstream config',
      testSuccess: 'Connection test succeeded',
      testFailed: 'Connection test failed',
      syncSuccess: 'Upstream synced, found {count} key(s)',
      syncPartial: 'Sync partially completed with {count} usable key(s). Check sync records for details.',
      syncFailed: 'Failed to sync upstream',
      syncAllSuccess: 'Synced {success} upstream(s), found {keys} key(s)',
      syncAllPartial: 'Sync finished: {success} succeeded, {partial} partial, {failed} failed, found {keys} key(s)',
      syncAllFailed: 'Failed to sync all upstreams',
      invalidDashboardUrl: 'Invalid upstream Base URL; cannot open dashboard',
      rechargeRateInvalid: 'Recharge rate must be greater than 0 and at most 100',
      balanceToCnyRateInvalid: 'Balance to CNY rate must be a positive number',
      loadSyncRunsFailed: 'Failed to load sync runs',
      loadSyncRunFailed: 'Failed to load sync run results',
      loadEventsFailed: 'Failed to load upstream events',
      loadTrendFailed: 'Failed to load cost trend',
      loadSettingsFailed: 'Failed to load upstream settings',
      settingsSaved: 'Upstream settings saved',
      saveSettingsFailed: 'Failed to save upstream settings'
    },
    settings: {
      lowBalanceThreshold: 'Low Balance Threshold (CNY)',
      lowBalanceThresholdHint: 'Balances below this value are highlighted in red. Set to 0 to disable the warning.'
    },
    operations: {
      syncRunsTitle: 'Sync Runs',
      eventsTitle: 'Upstream Events',
      trendTitle: 'Cost Trend',
      settingsTitle: 'Upstream Settings',
      runSubtitle: 'Run #{id}',
      syncRunsHint: 'Inspect batch and single-upstream synchronization results.',
      selectUpstream: 'Select upstream',
      success: 'Succeeded',
      partial: 'Partial',
      failed: 'Failed',
      duration: 'Duration',
      requests: 'Requests',
      upstreamCost: 'Upstream Cost',
      billedCost: 'Billed Cost',
      grossProfit: 'Gross Profit',
      unconvertedCost: 'Unconverted Cost',
      openIncidents: 'Open Incidents',
      recentEvents: 'Recent Events',
      emptySyncRuns: 'No sync runs',
      emptySyncResults: 'No result details for this run',
      emptyEvents: 'No upstream events',
      emptyBalanceHistory: 'No balance history',
      emptyTrend: 'No cost trend data',
      balanceHistory: 'Balance History',
      loadMore: 'Load More',
      syncRunSummary: '{total} upstreams: {success} succeeded, {partial} partial, {failed} failed',
      syncRecordSummary: 'Remote keys {remote}, persisted {persisted}, accounts updated {accounts}',
      incidentMetric: 'Current {value}, threshold {threshold}',
      legacyAttributed: '{count} request(s) use legacy attribution data.',
      status: {
        succeeded: 'Succeeded',
        partial: 'Partial',
        failed: 'Failed',
        open: 'Open',
        resolved: 'Resolved',
        unknown: 'Unknown'
      },
      trigger: {
        manual_single: 'Manual single sync',
        manual_batch: 'Manual batch sync',
        scheduled: 'Scheduled sync',
        unknown: 'Unknown trigger'
      },
      trendSeries: {
        baseCost: 'Raw Upstream Cost',
        actualCost: 'Actual Upstream Cost',
        billedCost: 'Billed Cost',
        grossProfit: 'Gross Profit'
      }
    }
  }
}
