export default {
  upstreamConfigs: {
    title: 'Upstream Configs',
    description: 'Maintain upstream relay login state, proxy, and keys in one place; accounts only bind to a selected upstream config.',
    searchPlaceholder: 'Search name or Base URL',
    sensitiveHint: 'These credentials are stored as highly sensitive upstream secrets and are only used to sync keys, rates, and refresh JWTs. Passwords, JWTs, refresh tokens, and API keys are never returned in plaintext.',
    columns: {
      name: 'Name',
      provider: 'Type',
      baseUrl: 'Base URL',
      balance: 'Balance',
      authMode: 'Auth',
      credentials: 'Credentials',
      lastSync: 'Last Sync',
      actions: 'Actions'
    },
    balance: {
      totalRecharged: 'Recharged {amount}',
      email: 'Upstream email: {email}',
      syncedAt: 'Balance synced: {time}',
      error: 'Balance error: {error}'
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
      password: 'Password {status}',
      accessToken: 'JWT {status}',
      refreshToken: 'Refresh {status}'
    },
    actions: {
      create: 'Add Upstream',
      test: 'Test Connection',
      syncKeys: 'Sync Upstream',
      syncAll: 'Sync All',
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
      pastePlaceholder: 'Supports Bearer, JWT, raw rt_ refresh tokens, JSON with access_token / refresh_token, or localStorage content',
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
      proxyId: 'Proxy ID',
      proxyPlaceholder: 'Empty means none',
      loginEmail: 'Login Email',
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
      savedButSyncFailed: 'Saved, but sync failed: {error}',
      saveFailed: 'Failed to save upstream config',
      deleted: 'Upstream config deleted',
      deleteFailed: 'Failed to delete upstream config',
      testSuccess: 'Connection test succeeded',
      testFailed: 'Connection test failed',
      syncSuccess: 'Upstream synced, found {count} key(s)',
      syncFailed: 'Failed to sync upstream',
      syncAllSuccess: 'Synced {success} upstream(s), found {keys} key(s)',
      syncAllPartial: 'Sync finished: {success} succeeded, {failed} failed, found {keys} key(s)',
      syncAllFailed: 'Failed to sync all upstreams',
      invalidDashboardUrl: 'Invalid upstream Base URL; cannot open dashboard'
    }
  }
}
