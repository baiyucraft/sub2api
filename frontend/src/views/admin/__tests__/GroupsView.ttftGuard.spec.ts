import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupsView from '../GroupsView.vue'
import GroupUpstreamProtectionPolicyModal from '@/components/admin/group/GroupUpstreamProtectionPolicyModal.vue'
import type { AdminGroup } from '@/types'
import type { GroupUpstreamProtectionPolicy } from '@/api/admin/groups'

const { listGroups, listPolicies } = vi.hoisted(() => ({ listGroups: vi.fn(), listPolicies: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {
  list: listGroups, listUpstreamProtectionPolicies: listPolicies,
  getLiveCapability: vi.fn().mockResolvedValue({ supported: false }), getModelAllowlistCandidates: vi.fn().mockResolvedValue([]),
  getUsageSummary: vi.fn().mockResolvedValue([]), getCapacitySummary: vi.fn().mockResolvedValue([]), getAll: vi.fn().mockResolvedValue([])
}, accounts: { list: vi.fn() } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const rows = [
  { id: 7, name: 'OpenAI', platform: 'openai', status: 'active', subscription_type: 'standard' },
  { id: 8, name: 'Claude', platform: 'anthropic', status: 'active', subscription_type: 'standard' }
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
})

describe('GroupsView scheduling protection entry', () => {
  it('offers the unified action to cache-capable non-OpenAI groups and opens that group', async () => {
    const wrapper = render()
    await flushPromises()
    expect(listPolicies).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-testid="group-upstream-protection"]')).toHaveLength(2)
    await wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').trigger('click')
    expect(wrapper.get('[data-test="policy-modal"]').text()).toContain('Claude')
    await wrapper.get('[data-test="close-policy"]').trigger('click')
    expect(wrapper.find('[data-test="policy-modal"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('updates only the saved group priority summary after the atomic dialog response', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').trigger('click')
    wrapper.findComponent(GroupUpstreamProtectionPolicyModal).vm.$emit('saved', { group_id: 8, priority: 'ttft_first' } as GroupUpstreamProtectionPolicy)
    await flushPromises()
    expect(wrapper.get('[data-group="7"] [data-testid="group-upstream-protection"]').text()).toContain('priorities.cache_first')
    expect(wrapper.get('[data-group="8"] [data-testid="group-upstream-protection"]').text()).toContain('priorities.ttft_first')
    expect(listGroups).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
