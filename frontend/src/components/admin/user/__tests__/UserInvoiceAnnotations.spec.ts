import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/types'
import UserInvoiceAnnotations from '../UserInvoiceAnnotations.vue'

const api = vi.hoisted(() => ({ getUserInvoices: vi.fn(), createUserInvoice: vi.fn(), updateUserInvoice: vi.fn(), voidUserInvoice: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: api } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${Object.values(params).join(',')}` : key }) }))
enableAutoUnmount(afterEach)
afterEach(() => vi.unstubAllGlobals())
const user = (id: number) => ({ id, email: `user${id}@example.com` }) as AdminUser
const record = (id = 1, amount = 100) => ({ id, user_id: 1, amount, invoice_date: '2026-10-10', invoice_number: 'INV-1', notes: 'annotation', status: 'active', created_by: 9, updated_by: 9, created_at: '2026-10-10T12:00:00Z', updated_at: '2026-10-10T12:00:00Z', voided_by: null, voided_at: null, void_reason: '' })
const response = (amount = 100) => ({ items: [record(1, amount)], total: 1, page: 1, pages: 1, total_invoiced: amount })
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (value: unknown) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
beforeEach(() => {
  vi.resetAllMocks()
  api.getUserInvoices.mockResolvedValue(response())
  api.createUserInvoice.mockResolvedValue(record())
  api.updateUserInvoice.mockResolvedValue(record())
  api.voidUserInvoice.mockResolvedValue({ ...record(), status: 'voided' })
})
function open(readOnly = false) {
  return mount(UserInvoiceAnnotations, {
    props: { show: true, user: user(1), readOnly },
    global: { stubs: { BaseDialog: { name: 'BaseDialog', props: ['show', 'showCloseButton', 'closeOnEscape', 'closeOnClickOutside'], template: '<section v-if="show"><slot /><slot name="footer" /></section>' } } }
  })
}
async function fill(wrapper: ReturnType<typeof open>, amount = '250') {
  await wrapper.get('[data-testid="invoice-register"]').trigger('click')
  await wrapper.get('input[type="number"]').setValue(amount)
  await wrapper.get('input[type="date"]').setValue('2026-10-01')
  await wrapper.get('input[maxlength="100"]').setValue('INV-NEW')
  await wrapper.get('textarea').setValue(' historical ')
}
async function clickText(wrapper: ReturnType<typeof open>, key: string) {
  await wrapper.findAll('button').find((button) => button.text() === key)!.trigger('click')
}

describe('user invoice annotations', () => {
  it('shows site amounts and the full active total across paginated invoice details', async () => {
    api.getUserInvoices.mockResolvedValue({ ...response(), total: 21, pages: 3, total_invoiced: 450 })
    const wrapper = open()
    await flushPromises()
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$450.00')
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    api.getUserInvoices.mockResolvedValue({ ...response(), total: 21, page: 2, pages: 3, total_invoiced: 450 })
    await clickText(wrapper, 'pagination.next')
    await flushPromises()
    expect(api.getUserInvoices).toHaveBeenLastCalledWith(1, 2, 10)
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$450.00')
    expect(wrapper.text()).not.toContain('¥')
    expect(wrapper.find('select').exists()).toBe(false)
  })

  it('distinguishes a real zero from an unavailable total', async () => {
    api.getUserInvoices.mockResolvedValueOnce({ ...response(), items: [], total: 0, total_invoiced: 0 })
    const wrapper = open()
    await flushPromises()
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$0.00')
    api.getUserInvoices.mockRejectedValueOnce(new Error('Invoices unavailable'))
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).not.toContain('$0.00')
    expect(wrapper.get('[role="alert"]').text()).toContain('Invoices unavailable')
  })

  it('ignores a previous user invoice total and records during fast switching', async () => {
    const old = deferred()
    api.getUserInvoices.mockReturnValueOnce(old.promise).mockResolvedValueOnce(response(25))
    const wrapper = open()
    await wrapper.setProps({ user: user(2) })
    await flushPromises()
    old.resolve(response(999))
    await flushPromises()
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$25.00')
    expect(wrapper.text()).not.toContain('$999.00')
    expect(api.getUserInvoices).toHaveBeenLastCalledWith(2, 1, 10)
  })

  it('keeps details available but hides all writing actions in read-only mode', async () => {
    const wrapper = open(true)
    await flushPromises()
    expect(wrapper.find('[data-testid="invoice-register"]').exists()).toBe(false)
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="invoice-1"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.users.invoices.edit')
    expect(wrapper.text()).not.toContain('admin.users.invoices.void')
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('creates an annotation with one captured user and refreshes totals, without a currency field', async () => {
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    api.getUserInvoices.mockResolvedValue(response(350))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createUserInvoice).toHaveBeenCalledWith(1, {
      amount: 250, invoice_date: '2026-10-01', invoice_number: 'INV-NEW', notes: 'historical', operation_id: expect.any(String)
    })
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$350.00')
    expect(wrapper.emitted('busy')).toEqual([[true], [false]])
  })

  it('prevents double submission and closing while writing and ignores old user write completions', async () => {
    const pending = deferred()
    api.createUserInvoice.mockReturnValueOnce(pending.promise)
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(api.createUserInvoice).toHaveBeenCalledTimes(1)
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findComponent({ name: 'BaseDialog' }).props('closeOnEscape')).toBe(false)
    await wrapper.setProps({ user: user(2) })
    await flushPromises()
    const calls = api.getUserInvoices.mock.calls.length
    pending.resolve(record())
    await flushPromises()
    expect(api.getUserInvoices.mock.calls).toHaveLength(calls)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(api.createUserInvoice.mock.calls[0][0]).toBe(1)
  })

  it('retries an unknown create result using the same operation id and unchanged payload', async () => {
    api.createUserInvoice.mockRejectedValueOnce({ status: 503, message: 'Uncertain response' }).mockResolvedValueOnce(record())
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.users.invoices.resultUnknown')
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createUserInvoice.mock.calls[1]).toEqual(api.createUserInvoice.mock.calls[0])
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('does not release the new user submission when the previous user write finishes', async () => {
    const old = deferred()
    const current = deferred()
    api.createUserInvoice.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ user: user(2) })
    await flushPromises()
    await fill(wrapper, '300')
    await wrapper.get('form').trigger('submit')
    expect(api.createUserInvoice.mock.calls.map((call) => call[0])).toEqual([1, 2])
    const busyEvents = wrapper.emitted('busy')!.length
    old.resolve(record())
    await flushPromises()
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findComponent({ name: 'BaseDialog' }).props('closeOnEscape')).toBe(false)
    expect(wrapper.emitted('busy')).toHaveLength(busyEvents)
    current.resolve(record(2))
    await flushPromises()
    expect(wrapper.emitted('busy')!.at(-1)).toEqual([false])
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('generates a valid idempotency key on HTTP when randomUUID is unavailable', async () => {
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => { bytes.fill(0x2a); return bytes }
    })
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createUserInvoice.mock.calls[0][1].operation_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })

  it('leaves known failures visible and supports correcting form inputs', async () => {
    api.createUserInvoice.mockRejectedValueOnce({ status: 400, message: 'Invalid amount' })
    const wrapper = open()
    await flushPromises()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Invalid amount')
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('input[type="number"]').setValue('300')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createUserInvoice.mock.calls[1][1].amount).toBe(300)
    expect(api.createUserInvoice.mock.calls[1][1].operation_id).not.toBe(api.createUserInvoice.mock.calls[0][1].operation_id)
  })

  it('corrects a record and voids with a required reason, refreshing the effective total', async () => {
    const wrapper = open()
    await flushPromises()
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    await clickText(wrapper, 'admin.users.invoices.edit')
    await wrapper.get('input[type="number"]').setValue('80')
    api.getUserInvoices.mockResolvedValue(response(80))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateUserInvoice).toHaveBeenCalledWith(1, 1, { amount: 80, invoice_date: '2026-10-10', invoice_number: 'INV-1', notes: 'annotation', expected_updated_at: '2026-10-10T12:00:00Z' })
    await clickText(wrapper, 'admin.users.invoices.void')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.voidUserInvoice).not.toHaveBeenCalled()
    await wrapper.get('textarea').setValue(' duplicate invoice ')
    api.getUserInvoices.mockResolvedValue({ ...response(), items: [{ ...record(), status: 'voided', void_reason: 'duplicate invoice', voided_by: 9, voided_at: '2026-10-12T00:00:00Z' }], total_invoiced: 0 })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.voidUserInvoice).toHaveBeenCalledWith(1, 1, 'duplicate invoice')
    expect(wrapper.get('[data-testid="invoice-summary"]').text()).toContain('$0.00')
    expect(wrapper.text()).toContain('admin.users.invoices.status.voided')
    expect(wrapper.text()).toContain('duplicate invoice')
    expect(wrapper.findAll('button').some((button) => button.text() === 'admin.users.invoices.edit')).toBe(false)
  })

  it('blocks stale edits and reloads the latest record before allowing another correction', async () => {
    const wrapper = open()
    await flushPromises()
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    await clickText(wrapper, 'admin.users.invoices.edit')
    await wrapper.get('input[type="number"]').setValue('80')
    api.updateUserInvoice.mockRejectedValueOnce({ status: 409, reason: 'USER_INVOICE_STALE', message: 'English backend message' })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.users.invoices.stale')
    expect(wrapper.text()).not.toContain('English backend message')
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(api.updateUserInvoice).toHaveBeenCalledTimes(1)
    const refreshed = { ...record(1, 110), updated_at: '2026-10-12T14:00:00Z' }
    api.getUserInvoices.mockResolvedValue({ ...response(110), items: [refreshed] })
    await clickText(wrapper, 'admin.users.invoices.reload')
    await flushPromises()
    expect(api.getUserInvoices).toHaveBeenLastCalledWith(1, 1, 10)
    await clickText(wrapper, 'admin.users.invoices.edit')
    expect((wrapper.get('input[type="number"]').element as HTMLInputElement).value).toBe('110')
    await wrapper.get('input[type="number"]').setValue('90')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateUserInvoice).toHaveBeenLastCalledWith(1, 1, expect.objectContaining({ amount: 90, expected_updated_at: '2026-10-12T14:00:00Z' }))
  })

  it('preserves the edit baseline version while retrying an unknown write result', async () => {
    const wrapper = open()
    await flushPromises()
    await wrapper.get('[data-testid="invoice-summary"]').trigger('click')
    await flushPromises()
    await clickText(wrapper, 'admin.users.invoices.edit')
    await wrapper.get('input[type="number"]').setValue('80')
    api.updateUserInvoice.mockRejectedValueOnce({ status: 503, message: 'Unknown result' }).mockResolvedValueOnce(record(1, 80))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateUserInvoice.mock.calls[1]).toEqual(api.updateUserInvoice.mock.calls[0])
    expect(api.updateUserInvoice.mock.calls[1][2].expected_updated_at).toBe('2026-10-10T12:00:00Z')
  })
})
