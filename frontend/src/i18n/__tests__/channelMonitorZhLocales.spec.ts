import { describe, expect, it } from 'vitest'
import zh from '@/i18n/locales/zh/dashboard'

describe('Chinese channel monitor labels', () => {
  it('does not expose internal English status or chart labels', () => {
    expect(zh.monitorCommon.past).toBe('过去')
    expect(zh.monitorCommon.now).toBe('现在')
    expect(zh.channelStatus.overall.operational).toBe('正常')
    expect(zh.channelStatus.overall.degraded).toBe('降级')
    expect(zh.channelStatus.overall.unavailable).toBe('不可用')
    expect(zh.channelStatus.rateTrend.timeColumn).toBe('时间')
    expect(Object.values(zh.channelStatus.overall)).not.toContain('OPERATIONAL')
    expect(Object.values(zh.channelStatus.overall)).not.toContain('DEGRADED')
    expect(zh.monitorCommon.past).not.toBe('PAST')
    expect(zh.monitorCommon.now).not.toBe('NOW')
  })
})
