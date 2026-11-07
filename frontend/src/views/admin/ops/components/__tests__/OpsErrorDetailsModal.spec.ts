import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'
import Select from '@/components/common/Select.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const { listRequestErrors, listUpstreamErrors } = vi.hoisted(() => ({
  listRequestErrors: vi.fn(),
  listUpstreamErrors: vi.fn(),
}))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { listRequestErrors, listUpstreamErrors } }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

async function openModal(errorType: 'request' | 'upstream' = 'request') {
  const wrapper = mount(OpsErrorDetailsModal, {
    props: {
      show: false, errorType, timeRange: 'custom', platform: 'openai', groupId: 22,
      customStartTime: '2026-11-01T00:00:00Z', customEndTime: '2026-11-07T00:00:00Z',
    },
    global: { stubs: {
      BaseDialog: { template: '<div><slot /></div>' }, Select: true, OpsErrorLogTable: true,
    } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('Ops error details model allowlist filter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listRequestErrors.mockResolvedValue({ items: [], total: 0 })
    listUpstreamErrors.mockResolvedValue({ items: [], total: 0 })
  })

  it('opens the excluded request view with the exact reason and retains group/time scope', async () => {
    const wrapper = await openModal()
    const viewSelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option) => option.value === 'model_not_allowed')
    )
    expect(viewSelect).toBeDefined()
    viewSelect!.vm.$emit('update:modelValue', 'model_not_allowed')
    await flushPromises()
    expect(listRequestErrors).toHaveBeenLastCalledWith(expect.objectContaining({
      view: 'excluded', reason: 'model_not_allowed', group_id: 22, platform: 'openai',
      start_time: '2026-11-01T00:00:00Z', end_time: '2026-11-07T00:00:00Z',
    }))
    expect(listUpstreamErrors).not.toHaveBeenCalled()

    await wrapper.find('button').trigger('click')
    await flushPromises()
    const resetQuery = listRequestErrors.mock.lastCall?.[0]
    expect(resetQuery).toMatchObject({ view: 'errors' })
    expect(resetQuery).not.toHaveProperty('reason')
    wrapper.unmount()
  })

  it('keeps the rejection filter out of upstream errors', async () => {
    const wrapper = await openModal('upstream')
    expect(wrapper.findAllComponents(Select).some((select) =>
      select.props('options').some((option) => option.value === 'model_not_allowed')
    )).toBe(false)
    expect(listUpstreamErrors).toHaveBeenLastCalledWith(expect.objectContaining({ view: 'errors' }))
    expect(listUpstreamErrors.mock.lastCall?.[0]).not.toHaveProperty('reason')
    wrapper.unmount()
  })

  it.each([['zh', zh], ['en', en]])('provides the rejection filters in %s', (_name, locale) => {
    const details = (locale as typeof zh).admin.ops.errorDetails
    for (const key of ['reasonFilter', 'allReasons', 'modelAllowlistRejected', 'filterModel'] as const) {
      expect(details[key]).toBeTruthy()
    }
  })
})
