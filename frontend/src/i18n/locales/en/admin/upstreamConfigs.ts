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
      authMode: 'Auth',
      credentials: 'Credentials',
      lastSync: 'Last Sync',
      actions: 'Actions'
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
      saving: 'Saving...'
    },
    dialog: {
      createTitle: 'Add Upstream',
      editTitle: 'Edit Upstream Config'
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
      saveFailed: 'Failed to save upstream config',
      deleted: 'Upstream config deleted',
      deleteFailed: 'Failed to delete upstream config',
      testSuccess: 'Connection test succeeded',
      testFailed: 'Connection test failed',
      syncSuccess: 'Upstream synced, found {count} key(s)',
      syncFailed: 'Failed to sync upstream',
      syncAllSuccess: 'Synced {success} upstream(s), found {keys} key(s)',
      syncAllPartial: 'Sync finished: {success} succeeded, {failed} failed, found {keys} key(s)',
      syncAllFailed: 'Failed to sync all upstreams'
    }
  }
}
