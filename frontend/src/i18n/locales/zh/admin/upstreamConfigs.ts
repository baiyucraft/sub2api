export default {
  upstreamConfigs: {
    title: '上游配置',
    description: '集中维护上游中转站登录态、代理和 Key，账号管理只绑定指定上游配置。',
    searchPlaceholder: '搜索名称或 Base URL',
    sensitiveHint: '这些凭据会保存为高敏上游凭据，仅用于同步 Key、倍率、额度和刷新 JWT；接口不会回显明文密码、JWT、Refresh Token、Cookie 或 API Key。',
    columns: {
      name: '名称',
      provider: '类型',
      baseUrl: 'Base URL',
      balance: '人民币余额',
      rates: '倍率摘要',
      authMode: '认证',
      credentials: '凭据',
      lastSync: '最近同步',
      actions: '操作'
    },
    balance: {
      totalRecharged: '累计充值 {amount}',
      lowBalance: '低余额',
      rawBalance: '原生剩余额度：{amount}',
      rawUsed: '原生已用额度：{amount}',
      rawTotal: '原生总额度：{amount}',
      email: '上游邮箱：{email}',
      syncedAt: '额度同步：{time}',
      error: '额度错误：{error}'
    },
    rates: {
      raw: '原始倍率 {value}',
      cost: '成本倍率 {value}',
      recharge: '充值倍率 {value}x'
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
      username: '账号 {status}',
      password: '密码 {status}',
      accessToken: 'JWT {status}',
      refreshToken: 'Refresh {status}'
    },
    actions: {
      create: '添加上游',
      test: '测试连接',
      syncKeys: '同步上游',
      syncAll: '同步全部',
      syncRuns: '同步记录',
      events: '事件',
      costTrend: '成本趋势',
      settings: '上游设置',
      openDashboard: '打开上游后台',
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
      pastePlaceholder: '支持 Bearer、JWT、裸 rt_ Refresh Token、包含 access_token / refresh_token 的 JSON，或浏览器 localStorage 表格行（auth_token / refresh_token）',
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
        jwt: 'JWT',
        rawRefresh: 'Sub2API Refresh Token'
      }
    },
    fields: {
      name: '名称',
      provider: '类型',
      baseUrl: 'Base URL',
      authMode: '认证方式',
      proxy: '代理',
      rechargeRate: '充值倍率',
      rechargeRateHint: '上游余额折算为实际成本的系数；成本倍率 = 原始倍率 × 充值折算率。',
      balanceToCnyRate: '余额兑人民币汇率',
      balanceToCnyRatePlaceholder: '留空使用上游汇率',
      balanceToCnyRateHint: '仅在上游未提供可靠人民币汇率时使用。',
      proxyId: '代理 ID',
      proxyPlaceholder: '留空不使用',
      loginEmail: '登录邮箱',
      loginUsername: '登录账号',
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
      savedAndSynced: '已保存并同步成功，发现 {keys} 个 Key，更新 {accounts} 个账号',
      savedAndSyncedPartial: '已保存，但同步仅部分完成：发现 {keys} 个 Key，更新 {accounts} 个账号',
      savedButSyncFailed: '已保存，但同步失败：{error}',
      saveFailed: '保存上游配置失败',
      newapiUsernameRequired: 'NewAPI 上游必须填写登录账号',
      newapiPasswordRequired: 'NewAPI 上游必须填写登录密码',
      deleted: '上游配置已删除',
      deleteFailed: '删除上游配置失败',
      testSuccess: '连接测试成功',
      testFailed: '连接测试失败',
      syncSuccess: '已同步上游，发现 {count} 个 Key',
      syncPartial: '同步仅部分完成，当前可用 {count} 个 Key，请查看同步记录',
      syncFailed: '同步上游失败',
      syncAllSuccess: '已同步 {success} 个上游，共发现 {keys} 个 Key',
      syncAllPartial: '同步完成：成功 {success} 个，部分成功 {partial} 个，失败 {failed} 个，共发现 {keys} 个 Key',
      syncAllFailed: '同步全部上游失败',
      invalidDashboardUrl: '上游 Base URL 无效，无法打开后台',
      rechargeRateInvalid: '充值倍率必须大于 0 且不超过 100',
      balanceToCnyRateInvalid: '余额兑人民币汇率必须为正数',
      loadSyncRunsFailed: '加载同步记录失败',
      loadSyncRunFailed: '加载同步结果失败',
      loadEventsFailed: '加载上游事件失败',
      loadTrendFailed: '加载成本趋势失败',
      loadSettingsFailed: '加载上游设置失败',
      settingsSaved: '上游设置已保存',
      saveSettingsFailed: '保存上游设置失败'
    },
    settings: {
      lowBalanceThreshold: '低余额阈值（人民币）',
      lowBalanceThresholdHint: '余额低于该值时在上游列表标红；设置为 0 表示关闭提醒。'
    },
    operations: {
      syncRunsTitle: '同步记录',
      eventsTitle: '上游事件',
      trendTitle: '成本趋势',
      settingsTitle: '上游设置',
      runSubtitle: '批次 #{id}',
      syncRunsHint: '查看批量或单个上游同步的执行结果。',
      selectUpstream: '选择上游',
      success: '成功',
      partial: '部分成功',
      failed: '失败',
      duration: '耗时',
      requests: '请求数',
      upstreamCost: '上游成本',
      billedCost: '计费金额',
      grossProfit: '毛利',
      unconvertedCost: '未换算成本',
      openIncidents: '未恢复事故',
      recentEvents: '最近事件',
      emptySyncRuns: '暂无同步记录',
      emptySyncResults: '该批次暂无结果明细',
      emptyEvents: '暂无上游事件',
      emptyBalanceHistory: '暂无余额历史',
      emptyTrend: '暂无成本趋势数据',
      balanceHistory: '余额历史',
      loadMore: '加载更多',
      syncRunSummary: '共 {total} 个上游：成功 {success}，部分成功 {partial}，失败 {failed}',
      syncRecordSummary: '远端 Key {remote}，已保存 {persisted}，更新账号 {accounts}',
      incidentMetric: '当前值 {value}，阈值 {threshold}',
      legacyAttributed: '其中 {count} 个请求来自旧版归因数据。',
      status: {
        succeeded: '成功',
        partial: '部分成功',
        failed: '失败',
        open: '未恢复',
        resolved: '已恢复',
        unknown: '未知'
      },
      trigger: {
        manual_single: '手动单个同步',
        manual_batch: '手动批量同步',
        scheduled: '定时同步',
        unknown: '未知触发方式'
      },
      trendSeries: {
        baseCost: '原始上游成本',
        actualCost: '实际上游成本',
        billedCost: '计费金额',
        grossProfit: '毛利'
      }
    }
  }
}
