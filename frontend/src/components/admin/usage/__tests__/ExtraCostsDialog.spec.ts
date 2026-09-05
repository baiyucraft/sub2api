import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ExtraCostsDialog from '../ExtraCostsDialog.vue'

const { list, create, reverse, showError, showSuccess } = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), reverse: vi.fn(), showError: vi.fn(), showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({ adminAPI: { extraCosts: { list, create, reverse } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

async function openDialog() {
  const renderError = vi.fn()
  const wrapper = mount(ExtraCostsDialog, {
    props: { show: false },
    global: {
      config: { errorHandler: renderError },
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show" role="dialog"><slot /></div>' },
        Select: true, Icon: true, LoadingSpinner: true, Pagination: true
      }
    }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return { wrapper, renderError }
}

describe('ExtraCostsDialog amount input', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    list.mockResolvedValue({ items: [], total: 0, daily_total: 0, range_total: 0 })
    create.mockResolvedValue({ id: 1 })
  })

  it.each(['1', '12.50', '0'])('accepts %s through the native number input without crashing', async (amount) => {
    const { wrapper, renderError } = await openDialog()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#extra-cost-amount').setValue(amount)
    expect(renderError).not.toHaveBeenCalled()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    expect(create).not.toHaveBeenCalled()

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ amount: Number(amount) }))
    expect(wrapper.emitted('changed')).toHaveLength(1)
    expect((wrapper.get('#extra-cost-amount').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it.each(['', '-1', '1e999', 'invalid'])('rejects empty or invalid amount %s after editing', async (amount) => {
    const { wrapper, renderError } = await openDialog()
    await wrapper.get('#extra-cost-amount').setValue('10')
    await wrapper.get('#extra-cost-amount').setValue(amount)
    expect(renderError).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
