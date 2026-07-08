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
      create: '添加上游',
      test: '测试连接',
      syncKeys: '同步上游',
      syncAll: '同步全部',
      saving: '保存中...'
    },
    dialog: {
      createTitle: '添加上游',
      editTitle: '编辑上游配置'
    },
    tokenAssistant: {
      open: '登录辅助',
      title: '上游登录辅助',
      inlineHint: '可从上游登录后复制 Token，本地解析后回填到表单。',
      securityHint: '粘贴内容只在浏览器本地解析，不会自动保存，也不会发送到后端；请确认候选项后再应用。',
      loginPageHint: '先在上游站点完成登录，再复制 localStorage、Network 响应或 Authorization 内容。',
      openLoginPage: '打开登录页',
      pasteLabel: '粘贴登录信息',
      pastePlaceholder: '支持 Bearer、JWT、包含 access_token / refresh_token 的 JSON 或 localStorage 内容',
      accessCandidate: 'Access Token 候选',
      refreshCandidate: 'Refresh Token 候选',
      doNotApply: '不应用',
      candidateLabel: '{source} / 尾号 {suffix}',
      noTokenFound: '未识别到可用 Token。',
      noSelection: '请选择至少一个 Token 候选。',
      apply: '应用到表单',
      applied: 'Token 已回填，请确认后保存。',
      invalidBaseUrl: '请先填写有效的上游 Base URL。',
      jwtUnverifiedNoExp: '这是本地解析到的 JWT 候选，未验证签名和有效性。',
      jwtExpiresAt: '本地解析的 JWT 过期时间：{time}。签名和权限仍需由上游验证。',
      jwtExpired: '本地解析显示 JWT 已在 {time} 过期；如有 Refresh Token 仍可一并保存。',
      sources: {
        bearer: 'Bearer',
        jwt: 'JWT'
      }
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
      syncSuccess: '已同步上游，发现 {count} 个 Key',
      syncFailed: '同步上游失败',
      syncAllSuccess: '已同步 {success} 个上游，共发现 {keys} 个 Key',
      syncAllPartial: '同步完成：成功 {success} 个上游，失败 {failed} 个，共发现 {keys} 个 Key',
      syncAllFailed: '同步全部上游失败'
    }
  }
}
