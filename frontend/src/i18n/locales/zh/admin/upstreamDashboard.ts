export default {
    upstreamDashboard: {
      title: '上游看板', description: '按上游配置查看流量、健康、性能与估算收益。', empty: '暂无匹配的上游配置', openDetail: '打开 {name} 详情', noReason: '暂无原因', estimatedUnavailable: '估算不可用',
      windows: { '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天', '15d': '最近 15 天', '30d': '最近 30 天' },
      filters: { allProviders: '全部 Provider', allStatuses: '全部状态' },
      status: { operational: '正常', degraded: '降级', critical: '异常', disabled: '已停用', data_insufficient: '数据不足' },
      metrics: { requests: '请求数', successRate: '成功率', ttft: 'P50 首字', latency: 'P50 总耗时', p95ttft: 'P95 首字', p95latency: 'P95 总耗时', failed: '失败数', timeouts: '超时', accounts: '可调度账号', probeSamples: '探针样本', estimatedProfit: '估算毛利' },
      sections: { traffic: '真实流量', probe: '主动探针', profit: '成本收益' },
      actions: { channels: '查看渠道', accounts: '查看账号', usage: '查看使用记录' }
    },
    upstreamChannels: { title: '上游渠道', description: '管理上游配置、Key 同步和渠道操作。' },
    upstreamAccounts: { title: '上游账号', description: '管理上游账号、健康状态和能力。' }
}
