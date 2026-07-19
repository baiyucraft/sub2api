import { defineComponent, type PropType } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import {
  DEFAULT_GROK_ENDPOINT,
  DEFAULT_GROK_MODEL,
  PROVIDERS,
  PROVIDER_GROK,
} from '@/constants/channelMonitor'

const { createMonitor, listTemplates, listGroups } = vi.hoisted(() => ({
  createMonitor: vi.fn(),
  listTemplates: vi.fn(),
  listGroups: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitor: {
      create: createMonitor,
      update: vi.fn(),
    },
    channelMonitorTemplate: {
      list: listTemplates,
    },
    groups: {
      getAll: listGroups,
    },
  },
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: vi.fn() },
}))

vi.mock('@/api/groups', () => ({
  userGroupsAPI: { getUserGroupRates: vi.fn() },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const ToggleStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: { type: Boolean, required: true } },
  emits: ['update:modelValue'],
  template: `
    <button
      v-bind="$attrs"
      type="button"
      role="switch"
      :aria-checked="modelValue"
      @click="$emit('update:modelValue', !modelValue)"
    />
  `,
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: {
    modelValue: { type: [String, Number, Boolean] as PropType<string | number | boolean | null>, default: null },
    options: { type: Array, default: () => [] },
  },
  emits: ['update:modelValue'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue ?? ''"
      @change="$emit('update:modelValue', $event.target.value === '' ? null : Number($event.target.value))"
    >
      <option value="">Select</option>
      <option v-for="option in options" :key="String(option.value)" :value="option.value">{{ option.label }}</option>
    </select>
  `,
})

function mountDialog(monitor: ChannelMonitor | null = null) {
  return mount(MonitorFormDialog, {
    props: { show: true, monitor },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Toggle: ToggleStub,
        Select: SelectStub,
        ModelTagInput: true,
        MonitorKeyPickerDialog: true,
        MonitorAdvancedRequestConfig: true,
      },
    },
  })
}

describe('channel monitor Grok provider', () => {
  beforeEach(() => {
    createMonitor.mockReset().mockResolvedValue({})
    listTemplates.mockReset().mockResolvedValue({ items: [] })
    listGroups.mockReset().mockResolvedValue([
      { id: 4, name: 'Claude 稳定', platform: 'anthropic', status: 'active' },
      { id: 7, name: 'OpenAI 稳定', platform: 'openai', status: 'active' },
    ])
  })

  it('offers Grok in the responsive provider grid and prefills its official defaults', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(PROVIDERS).toContain(PROVIDER_GROK)
    const providerButtons = wrapper.findAll('[data-testid^="monitor-provider-"]')
    expect(providerButtons).toHaveLength(4)
    expect(providerButtons[0].element.parentElement?.className).toContain('grid-cols-2')
    expect(providerButtons[0].element.parentElement?.className).toContain('sm:grid-cols-4')

    const grokButton = wrapper.get('[data-testid="monitor-provider-grok"]')
    expect(grokButton.find('svg').exists()).toBe(true)
    expect(grokButton.text()).toContain('monitorCommon.providers.grok')
    await grokButton.trigger('click')
    expect(grokButton.classes().join(' ')).toContain('zinc')

    const endpoint = wrapper.get('[data-testid="monitor-endpoint"]')
    const model = wrapper.get('[data-testid="monitor-primary-model"]')
    expect((endpoint.element as HTMLInputElement).value).toBe(DEFAULT_GROK_ENDPOINT)
    expect((model.element as HTMLInputElement).value).toBe(DEFAULT_GROK_MODEL)

    await wrapper.get('[data-testid="monitor-provider-anthropic"]').trigger('click')
    expect((endpoint.element as HTMLInputElement).value).toBe('')
    expect((model.element as HTMLInputElement).value).toBe('')

    await grokButton.trigger('click')
    await endpoint.setValue('https://gateway.example.com')
    await model.setValue('grok-custom')
    await wrapper.get('[data-testid="monitor-provider-openai"]').trigger('click')
    expect((endpoint.element as HTMLInputElement).value).toBe('https://gateway.example.com')
    expect((model.element as HTMLInputElement).value).toBe('grok-custom')
  })

  it('prefills only empty Grok fields and preserves existing provider values', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const endpoint = wrapper.get('[data-testid="monitor-endpoint"]')
    const model = wrapper.get('[data-testid="monitor-primary-model"]')
    const grokButton = wrapper.get('[data-testid="monitor-provider-grok"]')
    const anthropicButton = wrapper.get('[data-testid="monitor-provider-anthropic"]')

    await endpoint.setValue('https://gateway.example.com')
    await grokButton.trigger('click')
    expect((endpoint.element as HTMLInputElement).value).toBe('https://gateway.example.com')
    expect((model.element as HTMLInputElement).value).toBe(DEFAULT_GROK_MODEL)

    await anthropicButton.trigger('click')
    expect((endpoint.element as HTMLInputElement).value).toBe('https://gateway.example.com')
    expect((model.element as HTMLInputElement).value).toBe('')

    await endpoint.setValue('')
    await model.setValue('grok-custom')
    await grokButton.trigger('click')
    expect((endpoint.element as HTMLInputElement).value).toBe(DEFAULT_GROK_ENDPOINT)
    expect((model.element as HTMLInputElement).value).toBe('grok-custom')
  })

  it('enables group rate by default when selecting a managed local key and allows disabling it', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const groupRateToggle = wrapper.get('[data-testid="monitor-show-group-rate"]')
    expect(groupRateToggle.attributes('aria-checked')).toBe('false')

    await wrapper.get('[data-testid="monitor-credential-managed-local"]').trigger('click')
    expect(groupRateToggle.attributes('aria-checked')).toBe('true')

    await groupRateToggle.trigger('click')
    expect(groupRateToggle.attributes('aria-checked')).toBe('false')
  })

  it('submits the managed default and preserves an explicit disable', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('[data-testid="monitor-credential-managed-local"]').trigger('click')
    await wrapper.get('input[placeholder="admin.channelMonitor.form.namePlaceholder"]').setValue('managed')
    await wrapper.get('[data-testid="monitor-primary-model"]').setValue('claude-sonnet-4-5')
    await wrapper.get('[data-testid="monitor-group-select"]').setValue('4')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createMonitor).toHaveBeenLastCalledWith(expect.objectContaining({
      credential_mode: 'managed_local',
      group_id: 4,
      group_name: 'Claude 稳定',
      show_group_rate: true,
    }))

    const second = mountDialog()
    await flushPromises()
    await second.get('[data-testid="monitor-credential-managed-local"]').trigger('click')
    await second.get('[data-testid="monitor-show-group-rate"]').trigger('click')
    await second.get('input[placeholder="admin.channelMonitor.form.namePlaceholder"]').setValue('managed-hidden')
    await second.get('[data-testid="monitor-primary-model"]').setValue('claude-sonnet-4-5')
    await second.get('[data-testid="monitor-group-select"]').setValue('4')
    await second.get('form').trigger('submit')
    await flushPromises()

    expect(createMonitor).toHaveBeenLastCalledWith(expect.objectContaining({
      credential_mode: 'managed_local',
      group_id: 4,
      show_group_rate: false,
    }))
  })

  it('uses the existing group selection when editing a managed monitor', async () => {
    const monitor = {
      id: 1,
      name: 'managed',
      provider: 'anthropic',
      api_mode: 'chat_completions',
      endpoint: 'https://example.com',
      api_key_masked: 'sk-a***',
      primary_model: 'claude-sonnet-4-5',
      extra_models: [],
      group_name: 'Claude 稳定',
      group_id: 4,
      show_group_rate: true,
      credential_mode: 'managed_local',
      enabled: true,
      interval_seconds: 60,
      jitter_seconds: 0,
      max_probe_attempts: 3,
      last_checked_at: null,
      created_by: 1,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      primary_status: '',
      primary_latency_ms: null,
      availability_7d: 100,
      extra_models_status: [],
      template_id: null,
      extra_headers: {},
      body_override_mode: 'off',
      body_override: null,
    } as ChannelMonitor

    const wrapper = mountDialog(monitor)
    await flushPromises()

    const groupSelect = wrapper.get('[data-testid="monitor-group-select"]')
    expect((groupSelect.element as HTMLSelectElement).value).toBe('4')
    expect(wrapper.find('input[placeholder="admin.channelMonitor.form.groupNamePlaceholder"]').exists()).toBe(false)
  })

  it('clears the managed group when switching back to manual credentials', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('[data-testid="monitor-credential-managed-local"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="monitor-group-select"]').setValue('4')
    await wrapper.get('[data-testid="monitor-credential-manual"]').trigger('click')

    const groupNameInput = wrapper.get('input[placeholder="admin.channelMonitor.form.groupNamePlaceholder"]')
    expect((groupNameInput.element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[data-testid="monitor-group-select"]').exists()).toBe(false)

    await wrapper.get('input[placeholder="admin.channelMonitor.form.namePlaceholder"]').setValue('manual')
    await wrapper.get('[data-testid="monitor-primary-model"]').setValue('claude-sonnet-4-5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createMonitor).toHaveBeenLastCalledWith(expect.objectContaining({
      credential_mode: 'manual',
      group_id: null,
      group_name: '',
    }))
  })
})
