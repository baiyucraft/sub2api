export default {
  upstreamConfigs: {
    title: '上游配置',
    description: '集中维护上游中转站登录态、代理和 Key，账号管理只绑定指定上游配置。',
    searchPlaceholder: '搜索名称或 Base URL',
    sensitiveHint: '这些凭据会保存为高敏上游凭据，仅用于同步 Key、倍率和刷新 JWT；接口不会回显明文密码、JWT、Refresh Token 或 API Key。',
    columns: {
      name: '名称',
      provider: '类型',
      baseUrl: 'Base URL',
      authMode: '认证',
      credentials: '凭据',
      lastSync: '最近同步',
      actions: '操作'
    },
    providers: {
      sub2api: 'sub2api',
      newapi: 'newapi',
      other: '其他'
    },
    filters: {
      allProviders: '全部类型'
    },
    authModes: {
      userLogin: '账密登录',
      manualJwt: '手动 JWT',
      userLoginShort: '账密',
      manualJwtShort: 'JWT'
    },
    credentialStatus: {
      configured: '已配置',
      missing: '未配置',
      email: '邮箱 {status}',
      password: '密码 {status}',
      accessToken: 'JWT {status}',
      refreshToken: 'Refresh {status}'
    },
    actions: {
      create: '新增配置',
      test: '测试连接',
      syncKeys: '同步 Key',
      saving: '保存中...'
    },
    dialog: {
      createTitle: '新增上游配置',
      editTitle: '编辑上游配置'
    },
    fields: {
      name: '名称',
      provider: '类型',
      baseUrl: 'Base URL',
      authMode: '认证方式',
      proxy: '代理',
      proxyId: '代理 ID',
      proxyPlaceholder: '留空不使用',
      loginEmail: '登录邮箱',
      loginPassword: '登录密码',
      accessToken: 'Access Token',
      refreshToken: 'Refresh Token',
      keepPasswordPlaceholder: '留空保留旧密码',
      keepAccessTokenPlaceholder: '留空保留旧 JWT',
      keepRefreshTokenPlaceholder: '留空保留旧 refresh token'
    },
    emptyTitle: '暂无上游配置',
    emptyDescription: '新增一个上游站点后，账号管理可以直接绑定该站点下的 Key。',
    delete: {
      title: '删除上游配置',
      message: "确定要删除上游配置 '{name}' 吗？有关联账号时后端会拒绝删除。"
    },
    messages: {
      loadFailed: '加载上游配置失败',
      loadProxiesFailed: '加载代理列表失败',
      created: '上游配置已创建',
      updated: '上游配置已更新',
      saveFailed: '保存上游配置失败',
      deleted: '上游配置已删除',
      deleteFailed: '删除上游配置失败',
      testSuccess: '连接测试成功',
      testFailed: '连接测试失败',
      syncSuccess: '已同步 {count} 个 Key',
      syncFailed: '同步 Key 失败'
    }
  }
}
