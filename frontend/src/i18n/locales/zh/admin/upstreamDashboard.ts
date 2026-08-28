export default {
    upstreamDashboard: {
      title: '上游看板', description: '按上游配置查看流量、健康、性能与估算收益。', empty: '暂无匹配的上游配置', openDetail: '打开 {name} 详情', noReason: '暂无原因', estimatedUnavailable: '估算不可用',
      windows: { '1h': '1 小时', '24h': '24 小时', '7d': '7 天', '15d': '15 天', '30d': '30 天' },
      filters: { title: '筛选范围', window: '时间窗口', provider: 'Provider', status: '状态', sort: '状态排序', sortAsc: '切换为正序', sortDesc: '切换为倒序', searchLabel: '搜索渠道', allProviders: '全部 Provider', allStatuses: '全部状态' },
      summary: { configurations: '上游配置', withTraffic: '有真实流量', needAttention: '需要关注', schedulableAccounts: '可调度账号', openIncidents: '未解决事件', balanceLow: '余额不足渠道' },
      windowLabel: '窗口：{window}', lastUpdated: '更新于 {time}', lastProbe: '最近 {time}', emptyHint: '调整筛选条件，或等待新的流量与探针数据。', noTrafficData: '当前窗口暂无真实流量数据。', noTrendData: '当前窗口暂无趋势数据。',
      status: { operational: '正常', degraded: '降级', critical: '异常', disabled: '已停用', data_insufficient: '数据不足', healthy: '健康', success: '成功', error: '错误', failed: '失败', suspended: '已暂停', stale: '已过期', unknown: '未知' },
      reasons: { probe_succeeded: '探针完整结束', traffic_succeeded: '真实请求成功', authentication_failed: '上游鉴权失败', capacity_limited: '上游容量或频率受限', upstream_server_error: '上游服务端错误', probe_transport_error: '探测连接或传输失败', probe_response_mismatch: '探针响应不匹配', probe_incomplete_stream: '探针流未完整结束', probe_http_error: '探针 HTTP 错误', probe_request_failed: '探针请求失败', probe_quality_degraded: '探针质量判定降级', gateway_intercepted: '网关或 WAF 拦截', currency_unavailable: '缺少币种换算数据', no_snapshot: '暂无余额快照', stale_snapshot: '余额快照已过期', threshold_not_configured: '未配置余额告警阈值' },
      confidence: { current_success: '当前成功', mixed: '结果混合', unsuccessful: '未成功', data_insufficient: '数据不足' },
      events: { balance_low: '余额过低', recharge_rate_changed: '充值倍率变更', key_rate_changed: 'Key 倍率变更', key_effective_rate_changed: 'Key 有效倍率变更', key_actual_rate_changed: 'Key 实际倍率变更', currency_conversion_changed: '币种换算变更' },
      metrics: { requests: '请求数', successRate: '成功率', ttft: 'P50 首字', latency: 'P50 总耗时', p95ttft: 'P95 首字', p95latency: 'P95 总耗时', failed: '失败数', timeouts: '超时', authErrors: '认证/配置', accounts: '可调度账号', tempUnschedulable: '临时不可调度', probeSamples: '探针样本', confidence: '可信度', estimatedProfit: '估算毛利', balance: '余额', balanceLow: '余额不足，请及时充值', balanceThreshold: '告警阈值 {amount}', openIncidents: '未解决事件', lastRateChange: '最近倍率变更', lastRateChangeAt: '最近倍率变更：{time}', rateChanged: '倍率已变更', rateChangedAt: '倍率变更于 {time}', balanceUpdated: '余额更新于 {time}', balanceUnavailable: '余额数据不可用' },
      sections: { trend: '流量趋势', traffic: '真实流量', probe: '主动探针', operations: '运营信号', accounts: '账号构成', profit: '成本收益', errors: '最近错误' },
      noIncidentData: '暂无未解决事件', noRateChangeData: '暂无倍率变更记录',
      actions: { channels: '查看渠道', accounts: '查看账号', usage: '查看使用记录' }
    },
    upstreamChannels: { title: '上游渠道', description: '管理上游配置、Key 同步和渠道操作。' },
    upstreamAccounts: { title: '上游账号', description: '管理上游账号、健康状态和能力。' }
}
