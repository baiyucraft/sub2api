import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodexAccountTicketSettings from '../CodexAccountTicketSettings.vue'
import type {
  CodexAccountTicketModelStatus,
  CodexAccountTicketStatus,
  CodexTicketModel,
} from '@/api/admin/codexTickets'

const api = vi.hoisted(() => ({
  getCodexAccountTicket: vi.fn(),
  saveCodexAccountTicket: vi.fn(),
  harvestCodexAccountTicket: vi.fn(),
}))

vi.mock('@/api/admin/codexTickets', () => api)
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => `${key}${params ? JSON.stringify(params) : ''}`,
    locale: { value: 'zh-CN' },
  }),
}))

const models: CodexTicketModel[] = ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra']
const slug = (model: CodexTicketModel) => model === 'gpt-6-astra' ? 'astra' : model === 'gpt-5.6-sol' ? 'sol' : 'terra'
const selector = (part: string) => `[data-testid="codex-account-ticket-${part}"]`

function makeModel(model: CodexTicketModel, overrides: Partial<CodexAccountTicketModelStatus> = {}): CodexAccountTicketModelStatus {
  return {
    model,
    ticket_plan: 'pro',
    target_length: 292,
    enabled: false,
    state: 'disabled',
    active: null,
    ready: null,
    strikes: 0,
    refreshing: false,
    last_error: '',
    attempts: 0,
    watchdog: { enabled: false, trigger_count: 0 },
    ...overrides,
  }
}

function makeStatus(
  modelOverrides: Partial<Record<CodexTicketModel, Partial<CodexAccountTicketModelStatus>>> = {},
  overrides: Partial<CodexAccountTicketStatus> = {},
): CodexAccountTicketStatus {
  return {
    global_enabled: true,
    proxy_configured: true,
    proxy_display: 'pool.example:3000',
    fixed_proxy_configured: true,
    models: Object.fromEntries(models.map(model => [model, makeModel(model, modelOverrides[model])])) as Record<CodexTicketModel, CodexAccountTicketModelStatus>,
    ...overrides,
  }
}

const wrappers: ReturnType<typeof mount>[] = []
function mountCard() {
  const wrapper = mount(CodexAccountTicketSettings, { props: { accountId: 4, visible: true } })
  wrappers.push(wrapper)
  return wrapper
}

describe('CodexAccountTicketSettings', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.resetAllMocks()
    api.getCodexAccountTicket.mockResolvedValue(makeStatus())
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.useRealTimers()
  })

  it('renders Astra, Sol and Terra with independent active, ready, strikes and cooldown state', async () => {
    api.getCodexAccountTicket.mockResolvedValue(makeStatus({
      'gpt-6-astra': {
        enabled: true,
        state: 'ready',
        active: { remaining_seconds: 125, expires_at: '2026-09-19T12:00:00Z' },
        strikes: 1,
      },
      'gpt-5.6-sol': {
        enabled: true,
        ticket_plan: 'team',
        target_length: 332,
        state: 'harvesting',
        ready: { remaining_seconds: 600 },
        strikes: 2,
        refreshing: true,
        retry_after: '2026-09-19T12:05:00Z',
        watchdog: { enabled: true, trigger_count: 3, last_reason: 'state_312' },
      },
    }))

    const wrapper = mountCard()
    await flushPromises()

    for (const model of models) expect(wrapper.find(selector(`row-${slug(model)}`)).exists()).toBe(true)
    expect(wrapper.get(selector('active-astra')).text()).toContain('2m 05s')
    expect(wrapper.get(selector('ready-slot-sol')).text()).toContain('10m 00s')
    expect(wrapper.get(selector('strikes-sol')).text()).toContain('2')
    expect(wrapper.get(selector('retry-after-sol')).text()).toContain('cooldownUntil')
    expect(wrapper.get(selector('watchdog-sol')).text()).toContain('watchdogState312')
    expect((wrapper.get(selector('plan-sol')).element as HTMLSelectElement).value).toBe('team')
  })

  it('saves the complete three-model settings map', async () => {
    const wrapper = mountCard()
    await flushPromises()

    await wrapper.get(selector('enabled-astra')).trigger('click')
    await wrapper.get(selector('plan-sol')).setValue('team')
    await wrapper.get(selector('enabled-terra')).trigger('click')

    const savedResponse = makeStatus({
      'gpt-6-astra': { enabled: true, state: 'waiting' },
      'gpt-5.6-sol': { ticket_plan: 'team', target_length: 332 },
      'gpt-5.6-terra': { enabled: true, state: 'waiting' },
    })
    api.saveCodexAccountTicket.mockResolvedValue(savedResponse)

    await wrapper.get(selector('save')).trigger('click')
    await flushPromises()

    expect(api.saveCodexAccountTicket).toHaveBeenCalledWith(4, {
      models: {
        'gpt-6-astra': { enabled: true, ticket_plan: 'pro' },
        'gpt-5.6-sol': { enabled: false, ticket_plan: 'team' },
        'gpt-5.6-terra': { enabled: true, ticket_plan: 'pro' },
      },
    })
    expect(wrapper.text()).toContain('admin.accounts.stateTicket.saved')
  })

  it('starts harvesting for the selected model only', async () => {
    api.getCodexAccountTicket.mockResolvedValue(makeStatus({
      'gpt-5.6-sol': { enabled: true, state: 'waiting' },
    }))
    api.harvestCodexAccountTicket.mockResolvedValue(makeStatus({
      'gpt-5.6-sol': { enabled: true, state: 'harvesting', attempts: 1 },
    }))

    const wrapper = mountCard()
    await flushPromises()
    await wrapper.get(selector('harvest-sol')).trigger('click')
    await flushPromises()

    expect(api.harvestCodexAccountTicket).toHaveBeenCalledWith(4, 'gpt-5.6-sol')
    expect(wrapper.get(selector('state-sol')).text()).toContain('harvesting')
    expect(wrapper.get(selector('harvest-sol')).attributes('disabled')).toBeDefined()
  })

  it('keeps local edits during polling and reloads drafts for a different account', async () => {
    const wrapper = mountCard()
    await flushPromises()
    await wrapper.get(selector('enabled-astra')).trigger('click')
    await wrapper.get(selector('plan-terra')).setValue('team')

    api.getCodexAccountTicket.mockResolvedValue(makeStatus({}, { global_enabled: false }))
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.get(selector('enabled-astra')).attributes('aria-checked')).toBe('true')
    expect((wrapper.get(selector('plan-terra')).element as HTMLSelectElement).value).toBe('team')
    expect(wrapper.find(selector('global-off')).exists()).toBe(true)

    api.getCodexAccountTicket.mockResolvedValue(makeStatus({
      'gpt-5.6-terra': { enabled: true, ticket_plan: 'team', target_length: 332 },
    }))
    await wrapper.setProps({ accountId: 5 })
    await flushPromises()
    expect(api.getCodexAccountTicket).toHaveBeenLastCalledWith(5)
    expect(wrapper.get(selector('enabled-astra')).attributes('aria-checked')).toBe('false')
    expect(wrapper.get(selector('enabled-terra')).attributes('aria-checked')).toBe('true')
  })

  it('normalizes the legacy single-model response and requires both proxy layers for new opt-in', async () => {
    api.getCodexAccountTicket.mockResolvedValue({
      global_enabled: true,
      proxy_configured: false,
      proxy_display: '',
      fixed_proxy_configured: false,
      model: 'gpt-6-astra',
      enabled: true,
      ticket_plan: 'pro',
      target_length: 292,
      state: 'ready',
      ticket_usable: true,
      remaining_seconds: 90,
      last_error: '',
      attempts: 0,
      watchdog: { enabled: true, trigger_count: 0 },
    } satisfies CodexAccountTicketStatus)

    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.get(selector('enabled-astra')).attributes('aria-checked')).toBe('true')
    expect(wrapper.get(selector('active-astra')).text()).toContain('1m 30s')
    expect(wrapper.get(selector('enabled-sol')).attributes('disabled')).toBeDefined()
    expect(wrapper.find(selector('fixed-proxy-missing')).exists()).toBe(true)

    await wrapper.get(selector('enabled-astra')).trigger('click')
    expect(wrapper.get(selector('save')).attributes('disabled')).toBeUndefined()
  })
})
