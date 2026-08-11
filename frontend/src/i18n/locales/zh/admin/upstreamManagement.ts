export default {
  upstreamManagement: {
    title: '上游管理',
    description: '管理由上游 Key 派生的账号、调度状态、健康、首 Token 性能和质量。',
    saved: '已保存',
    derivedAccount: {
      title: '上游派生账号',
      description: 'Key、站点、代理、倍率、Priority 和 LoadFactor 由“上游配置”派生，不能在上游管理中修改。',
      bulkDescription: '上游 Key、站点、代理、倍率、Priority、LoadFactor 和凭据由“上游配置”维护，本批量操作只修改账号运行时可调度属性。',
      configId: '上游配置 ID',
      keyId: '上游 Key ID',
      site: '上游站点'
    },
    saveFailed: '保存上游管理配置失败',
    rateTrend: { keyId: 'Key #{id}' },
    ttftGuard: {
      title: '首 Token 性能保护',
      description: '仅作用于上游账号；慢账号会被临时降级，原有调度策略保持不变。',
      enabled: '启用',
      threshold: '降级阈值（秒）',
      minSamples: '最少样本',
      invalid: '降级阈值需为 5–300 秒，最少样本需为 2–20 的整数。'
    },
    probeModels: { title: '平台探针模型', invalid: '三个平台的探针模型均不能为空，且最长 255 个字符。' },
    actions: { probe: '探测', observation: '观测', events: '事件', probeRecorded: '探测结果已记录', probeFailed: '探测上游 Key 失败', observationFailed: '更新 Key 观测状态失败' },
    health: {
      keyHealth: 'Key 健康',
      healthy: '健康',
      degraded: '降级',
      suspended: '探测暂停',
      observing: '观测中',
      recovering: '恢复中',
      disabled: '观测已关闭',
      noData: '暂无健康数据',
      reason: '原因',
      lastProbe: '最近探测',
      probeStatus: '最近探测结果',
      lastTraffic: '最近流量证据',
      schedulable: '账号可调度',
      failures: '连续失败',
      recovery: '恢复进度',
      observationDisabled: 'Key 观测已关闭，不会执行自动探测。',
      temporarilyExcluded: '当前因 Key 健康临时排除',
      past: '过去',
      now: '现在',
      historySummary: '{healthy} / {total} 正常',
      noHistory: '暂无连续健康观测',
      openDetails: '打开 Key 健康历史详情',
      recentObservations: '最近健康观测',
      retentionHint: '最多保留最近 30 次',
      observedAt: '观测时间',
      source: '证据来源',
      result: '结果',
      sources: {
        probe: '主动探针',
        traffic: '真实请求',
        admin: '管理员操作'
      },
      reasons: {
        probe_succeeded: '主动探测成功',
        traffic_succeeded: '真实请求成功',
        observation_enabled: '已开启 Key 观测',
        observation_disabled: '已关闭 Key 观测',
        recovered: '健康状态已恢复',
        authentication_failed: '上游鉴权失败',
        capacity_limited: '上游容量或频率受限',
        upstream_server_error: '上游服务端错误',
        probe_transport_error: '探测连接或传输失败'
      }
    },
    events: { title: 'Key 健康详情', stateChanges: '状态变更事件', loading: '加载健康详情中…', empty: '暂无状态变更事件', loadFailed: '加载 Key 健康详情失败' },
    columns: { accountKey: '账号 / Key', upstream: '上游配置', config: '配置', key: 'Key', modelMapping: '模型映射', health: '健康' },
    export: { action: '导出上游状态', success: '上游状态已导出', failed: '导出上游状态失败' },
    filters: { allConfigs: '全部上游配置', allKeys: '全部 Key', loadFailed: '加载上游筛选选项失败' }
  }
}
