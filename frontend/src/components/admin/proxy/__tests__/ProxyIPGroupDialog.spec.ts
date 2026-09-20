import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { listGroups, createGroup, updateGroup, deleteGroup, getGroup, getProxies, showError, showSuccess } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  deleteGroup: vi.fn(),
  getGroup: vi.fn(),
  getProxies: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    proxyIpGroups: {
      list: listGroups,
      create: createGroup,
      update: updateGroup,
      delete: deleteGroup,
      getById: getGroup,
    },
    proxies: { getAll: getProxies },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

import ProxyIPGroupDialog from '../ProxyIPGroupDialog.vue'

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /></div>',
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  emits: ['update:modelValue'],
  template: '<button type="button" data-testid="choose-members" @click="$emit(\'update:modelValue\', [2, 3, 3])">members</button>',
})

const mountDialog = () => mount(ProxyIPGroupDialog, {
  props: { show: true },
  global: {
    stubs: {
      BaseDialog: BaseDialogStub,
      ConfirmDialog: true,
      Select: SelectStub,
      Icon: true,
    },
  },
})

describe('ProxyIPGroupDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listGroups.mockResolvedValue([])
    getProxies.mockResolvedValue([
      { id: 2, name: 'p2', host: '127.0.0.2', port: 8080 },
      { id: 3, name: 'p3', host: '127.0.0.3', port: 8080 },
    ])
    createGroup.mockResolvedValue({})
    updateGroup.mockResolvedValue({})
  })

  it('creates a group with de-duplicated proxy members', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue(' primary ')
    await wrapper.get('input[type="number"]').setValue('4')
    await wrapper.get('[data-testid="proxy-ip-group-members"]').trigger('click')
    await wrapper.get('[data-testid="proxy-ip-group-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith({
      name: 'primary',
      per_ip_concurrency: 4,
      proxy_ids: [2, 3],
    })
  })

  it('defaults new groups to 10 concurrency per IP', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('default limit')
    await wrapper.get('[data-testid="proxy-ip-group-members"]').trigger('click')
    await wrapper.get('[data-testid="proxy-ip-group-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({
      per_ip_concurrency: 10,
    }))
    expect(wrapper.get('input[type="number"]').attributes('max')).toBe('1000')
  })

  it('allows creating an empty group that will fail closed at runtime', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('empty pool')
    await wrapper.get('[data-testid="proxy-ip-group-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledWith({
      name: 'empty pool',
      per_ip_concurrency: 10,
      proxy_ids: [],
    })
  })

  it('rejects concurrency above 1000', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('too large')
    await wrapper.get('input[type="number"]').setValue('1001')
    await wrapper.get('[data-testid="proxy-ip-group-members"]').trigger('click')
    await wrapper.get('[data-testid="proxy-ip-group-form"]').trigger('submit.prevent')

    expect(showError).toHaveBeenCalledWith('admin.proxies.ipGroups.concurrencyInvalid')
    expect(createGroup).not.toHaveBeenCalled()
  })

  it('updates the selected group with its existing members', async () => {
    listGroups.mockResolvedValue([
      { id: 8, name: 'existing', member_count: 1, per_ip_concurrency: 2, proxy_ids: [2] },
    ])
    const wrapper = mountDialog()
    await flushPromises()
    const groupButton = wrapper.findAll('button').find(button => button.text().includes('existing'))
    expect(groupButton).toBeDefined()
    await groupButton!.trigger('click')
    await wrapper.get('input[type="text"]').setValue('updated')
    await wrapper.get('[data-testid="proxy-ip-group-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledWith(8, {
      name: 'updated',
      per_ip_concurrency: 2,
      proxy_ids: [2],
    })
  })
})
