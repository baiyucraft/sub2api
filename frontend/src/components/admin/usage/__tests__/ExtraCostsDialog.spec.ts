import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ExtraCostsDialog from '../ExtraCostsDialog.vue'

const { list, create, reverse, showError, showSuccess, formatDateTime } = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), reverse: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(), formatDateTime: vi.fn()
}))

vi.mock('@/api/admin', () => ({ adminAPI: { extraCosts: { list, create, reverse } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('@/utils/format', () => ({ formatDateTime }))
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
    formatDateTime.mockImplementation((value: string) => `formatted:${value}`)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('defaults the ledger to the browser local day, including shortly after local midnight', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 0, 30, 0))

    const { wrapper } = await openDialog()

    expect(list).toHaveBeenCalledWith({
      start_date: '2026-07-13',
      end_date: '2026-07-13',
      page: 1,
      page_size: 10
    })
    expect(wrapper.find('#extra-cost-date').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.dashboard.extraCostTimeHint')
    wrapper.unmount()
  })

  it('renders the recorded occurrence timestamp instead of the date bucket', async () => {
    list.mockResolvedValue({
      items: [{
        id: 9,
        cost_date: '2026-09-17',
        created_at: '2026-09-18T01:02:03Z',
        amount: 1,
        category: 'account',
        notes: ''
      }],
      total: 1,
      daily_total: 1,
      range_total: 1
    })

    const { wrapper } = await openDialog()

    expect(formatDateTime).toHaveBeenCalledWith('2026-09-18T01:02:03Z')
    expect(wrapper.text()).toContain('formatted:2026-09-18T01:02:03Z')
    expect(wrapper.text()).not.toContain('2026-09-17')
    wrapper.unmount()
  })

  it('resets the date range to today when reopened', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 12, 0, 0))
    const { wrapper } = await openDialog()
    const dateInputs = wrapper.findAll('input[type="date"]')
    await dateInputs[0].setValue('2026-07-01')
    await dateInputs[1].setValue('2026-07-02')
    await flushPromises()

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-13',
      end_date: '2026-07-13',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
  })

  it('returns to today after creating from a historical range', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 12, 0, 0))
    const { wrapper } = await openDialog()
    const dateInputs = wrapper.findAll('input[type="date"]')
    await dateInputs[0].setValue('2026-07-01')
    await dateInputs[1].setValue('2026-07-02')
    await wrapper.get('#extra-cost-amount').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-13',
      end_date: '2026-07-13',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
  })

  it('shows the server accounting day after creating across a timezone boundary', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 23, 30, 0))
    create.mockResolvedValue({ id: 1, cost_date: '2026-07-14' })
    const { wrapper } = await openDialog()
    await wrapper.get('#extra-cost-amount').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-14',
      end_date: '2026-07-14',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
  })

  it('normalizes a legacy RFC3339 accounting day before refreshing the ledger', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 23, 30, 0))
    create.mockResolvedValue({ id: 1, cost_date: '2026-07-14T00:00:00Z' })
    const { wrapper } = await openDialog()
    await wrapper.get('#extra-cost-amount').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-14',
      end_date: '2026-07-14',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
  })

  it('keeps the saved state and restores the previous range when post-create refresh fails', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 12, 0, 0))
    vi.spyOn(console, 'error').mockImplementation(() => {})
    list
      .mockResolvedValueOnce({ items: [], total: 0, daily_total: 0, range_total: 0 })
      .mockRejectedValueOnce(new Error('refresh failed'))
    create.mockResolvedValue({ id: 1, cost_date: '2026-07-14T00:00:00Z' })

    const { wrapper } = await openDialog()
    await wrapper.get('#extra-cost-amount').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.dashboard.extraCostAdded')
    expect(showError).toHaveBeenCalledWith('admin.dashboard.extraCostRecordedRefreshFailed')
    expect(showError).not.toHaveBeenCalledWith('admin.dashboard.extraCostSaveFailed')
    expect(wrapper.emitted('changed')).toHaveLength(1)
    const dateInputs = wrapper.findAll('input[type="date"]')
    expect((dateInputs[0].element as HTMLInputElement).value).toBe('2026-07-13')
    expect((dateInputs[1].element as HTMLInputElement).value).toBe('2026-07-13')
    wrapper.unmount()
  })

  it('returns to today after reversing from a historical range', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 12, 0, 0))
    vi.spyOn(window, 'prompt').mockReturnValue('correct entry')
    reverse.mockResolvedValue({ id: 10 })
    list.mockResolvedValue({
      items: [{ id: 9, cost_date: '2026-07-01', created_at: '2026-07-01T09:00:00Z', amount: 5, category: 'account', notes: '' }],
      total: 1,
      daily_total: 0,
      range_total: 5
    })
    const { wrapper } = await openDialog()
    const dateInputs = wrapper.findAll('input[type="date"]')
    await dateInputs[0].setValue('2026-07-01')
    await dateInputs[1].setValue('2026-07-02')
    await wrapper.get('button.text-red-600').trigger('click')
    await flushPromises()

    expect(reverse).toHaveBeenCalledWith(9, expect.objectContaining({ reason: 'correct entry' }))
    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-13',
      end_date: '2026-07-13',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
  })

  it('shows the server accounting day after reversing across a timezone boundary', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 6, 13, 23, 30, 0))
    vi.spyOn(window, 'prompt').mockReturnValue('correct entry')
    reverse.mockResolvedValue({ id: 10, cost_date: '2026-07-14' })
    list.mockResolvedValue({
      items: [{ id: 9, cost_date: '2026-07-13', created_at: '2026-07-13T09:00:00Z', amount: 5, category: 'account', notes: '' }],
      total: 1,
      daily_total: 5,
      range_total: 5
    })
    const { wrapper } = await openDialog()
    await wrapper.get('button.text-red-600').trigger('click')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith({
      start_date: '2026-07-14',
      end_date: '2026-07-14',
      page: 1,
      page_size: 10
    })
    wrapper.unmount()
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
    expect(create.mock.calls[0][0]).not.toHaveProperty('cost_date')
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
