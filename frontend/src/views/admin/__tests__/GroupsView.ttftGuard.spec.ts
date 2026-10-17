import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupsView from '../GroupsView.vue'
import GroupUpstreamProtectionPolicyModal from '@/components/admin/group/GroupUpstreamProtectionPolicyModal.vue'
import type { AdminGroup } from '@/types'
import type { GroupUpstreamProtectionPolicy } from '@/api/admin/groups'
import { SCHEDULING_PROTECTION_SETTINGS_UPDATED } from '@/utils/schedulingProtection'

const { listGroups, listPolicies, getSettings, showError } = vi.hoisted(() => ({ listGroups: vi.fn(), listPolicies: vi.fn(), getSettings: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {
  list: listGroups, listUpstreamProtectionPolicies: listPolicies,
  getLiveCapability: vi.fn().mockResolvedValue({ supported: false }), getModelAllowlistCandidates: vi.fn().mockResolvedValue([]),
  getUsageSummary: vi.fn().mockResolvedValue([]), getCapacitySummary: vi.fn().mockResolvedValue([]), getAll: vi.fn().mockResolvedValue([])
}, upstreamManagement: { getSettings }, accounts: { list: vi.fn() } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const rows = [
  { id: 7, name: 'OpenAI', platform: 'openai', status: 'active', subscription_type: 'standard' },
  { id: 8, name: 'Claude', platform: 'anthropic', status: 'active', subscription_type: 'standard' },
  { id: 9, name: 'Composite', platform: 'composite', status: 'active', subscription_type: 'standard' },
  { id: 10, name: 'Antigravity', platform: 'antigravity', status: 'active', subscription_type: 'standard' }
] as AdminGroup[]
const modal = defineComponent({ name: 'GroupUpstreamProtectionPolicyModal', props: ['show', 'group'], emits: ['close', 'saved'], template: '<div v-if="show" data-test="policy-modal">{{ group.name }}<button data-test="close-policy" @click="$emit(\'close\')"/></div>' })
function render() {
  return mount(GroupsView, { global: { stubs: {
    AppLayout: { template: '<main><slot/></main>' },
    TablePageLayout: { template: '<div><slot name="table"/></div>' },
    DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id" :data-group="row.id"><slot name="cell-actions" :row="row"/></div></div>' },
    GroupUpstreamProtectionPolicyModal: modal,
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' },
    Pagination: true, ConfirmDialog: true, EmptyState: true, Select: true, Icon: true, PlatformIcon: true,
    GroupCapacityBadge: true, GroupRateMultipliersModal: true, GroupRPMOverridesModal: true, VueDraggable: true
  } } })
}
beforeEach(() => {
  localStorage.clear()
  listGroups.mockReset().mockResolvedValue({ items: rows, total: 2, page: 1, page_size: 20, pages: 1 })
  listPolicies.mockReset().mockResolvedValue([{ group_id: 7, priority: 'cache_first' }, { group_id: 8, priority: 'inherit' }])
  getSettings.mockReset().mockResolvedValue({ scheduling_protection_platforms: ['openai'] })
  showError.mockReset()
})

describe('GroupsView scheduling protection entry', () => {
  it.each(['click', 'focus', 'visibilitychange', SCHEDULING_PROTECTION_SETTINGS_UPDATED])(
    'keeps action buttons in place while refreshing protection scope on %s',
    async (event) => {
      const wrapper = render()
      try {
        await flushPromises()
        const selector = '[data-group="7"] [data-testid="group-upstream-protection"]'
        const button = wrapper.get(selector).element
        const actions = [...button.parentElement!.children]
        let resolveSettings!: (settings: { scheduling_protection_platforms: string[] }) => void
        getSettings.mockImplementationOnce(() => new Promise(resolve => { resolveSettings = resolve }))

        if (event === 'click') await wrapper.get(selector).trigger('click')
        else if (event === 'visibilitychange') document.dispatchEvent(new Event(event))
        else window.dispatchEvent(new Event(event))
        await flushPromises()

        expect(getSettings).toHaveBeenCalledTimes(2)
        expect(wrapper.get(selector).element).toBe(button)
        actions.forEach((action, index) => expect(button.parentElement!.children[index]).toBe(action))
        expect(wrapper.get(selector).text()).toContain('priorities.cache_first')
        expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(false)

        resolveSettings({ scheduling_protection_platforms: ['openai'] })
        await flushPromises()
        expect(wrapper.get(selector).element).toBe(button)
        expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(event === 'click')
        expect(showError).not.toHaveBeenCalled()
      } finally {
        wrapper.unmount()
      }
    }
  )

  it('ignores an old refresh failure after a newer platform scope has loaded', async () => {
    const wrapper = render()
    try {
      await flushPromises()
      let rejectSettings!: (error: Error) => void
      getSettings.mockImplementationOnce(() => new Promise((_, reject) => { rejectSettings = reject }))
      window.dispatchEvent(new Event('focus'))
      await flushPromises()

      getSettings.mockResolvedValue({ scheduling_protection_platforms: ['anthropic'] })
      window.dispatchEvent(new Event(SCHEDULING_PROTECTION_SETTINGS_UPDATED))
      await flushPromises()
      const selector = '[data-group="8"] [data-testid="group-upstream-protection"]'
      const button = wrapper.get(selector).element

      rejectSettings(new Error('old settings request failed'))
      await flushPromises()
      expect(wrapper.get(selector).element).toBe(button)
      expect(wrapper.find('[data-group="7"] [data-testid="group-upstream-protection"]').exists()).toBe(false)
      expect(showError).not.toHaveBeenCalled()
    } finally {
      wrapper.unmount()
    }
  })

  it('offers the unified action only to the backend-selected platform groups', async () => {
    getSettings.mockResolvedValue({ scheduling_protection_platforms: ['openai', 'anthropic', 'antigravity'] })
    const wrapper = render()
    await flushPromises()
    expect(listPolicies).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(3)
    expect(wrapper.find('[data-group="9"] [data-testid="group-upstream-protection"]').exists()).toBe(false)
    await wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="policy-modal"]').text()).toContain('Claude')
    await wrapper.get('[data-test="close-policy"]').trigger('click')
    expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('updates only the saved group priority summary after the atomic dialog response', async () => {
    getSettings.mockResolvedValue({ scheduling_protection_platforms: ['openai', 'anthropic'] })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').trigger('click')
    await flushPromises()
    wrapper.findComponent(GroupUpstreamProtectionPolicyModal).vm.$emit('saved', { group_id: 8, priority: 'ttft_first' } as GroupUpstreamProtectionPolicy)
    await flushPromises()
    expect(wrapper.get('[data-group="7"] [data-testid="group-upstream-protection"]').text()).toContain('priorities.cache_first')
    expect(wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').text()).toContain('priorities.ttft_first')
    expect(listGroups).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('defaults a legacy response to OpenAI and preserves an explicit empty backend scope', async () => {
    getSettings.mockResolvedValue({})
    const wrapper = render()
    await flushPromises()
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(1)
    expect(wrapper.find('[data-group="7"] [data-testid="group-upstream-protection"]').exists()).toBe(true)
    getSettings.mockResolvedValue({ scheduling_protection_platforms: [] })
    window.dispatchEvent(new Event(SCHEDULING_PROTECTION_SETTINGS_UPDATED))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('refreshes entries after settings change and closes an open group when its platform is removed', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-group="7"] [data-testid="group-upstream-protection"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(true)
    getSettings.mockResolvedValue({ scheduling_protection_platforms: ['anthropic'] })
    window.dispatchEvent(new Event(SCHEDULING_PROTECTION_SETTINGS_UPDATED))
    await flushPromises()
    expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(false)
    expect(wrapper.find('[data-group="7"] [data-testid="group-upstream-protection"]').exists()).toBe(false)
    expect(wrapper.find('[data-group="8"] [data-testid="group-upstream-protection"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('rechecks scope when opening, hides entries on read failure and provides an action error', async () => {
    const wrapper = render()
    await flushPromises()
    const log = vi.spyOn(console, 'error').mockImplementation(() => {})
    getSettings.mockRejectedValueOnce(new Error('settings unavailable'))
    await wrapper.get('[data-group="7"] [data-testid="group-upstream-protection"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(0)
    expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(false)
    expect(showError).toHaveBeenCalledWith('admin.upstreamManagement.protection.loadFailed')
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(1)
    log.mockRestore()
    wrapper.unmount()
  })
})
