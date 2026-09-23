import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ChannelCustomizationView from '../ChannelCustomizationView.vue'

const { getSettings, updateSettings, getAllGroups, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(), updateSettings: vi.fn(), getAllGroups: vi.fn(), showError: vi.fn(), showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelCustomization: { getSettings, updateSettings },
    groups: { getAll: getAllGroups }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))

const messages = { en: { common: { loading: 'Loading', saving: 'Saving', error: 'Error' }, admin: { channels: { customization: new Proxy({}, { get: (_, key) => String(key) }) } } } }
const i18n = createI18n({ legacy: false, locale: 'en', messages })
const rule = { name: 'maibon', enabled: true, api_key_ids: [], api_key_names: ['maibon-gpt'], user_ids: [], user_emails: ['1069167864@qq.com'], methods: ['GET'], exact_paths: ['/v1/models'], path_prefixes: [], models: [], user_agent_contains: [], query_params: {}, min_delay_ms: 100, max_delay_ms: 300, status_code: 200, content_type: 'application/json', response_body: '{}', hit_count: 12 }

function mountView() {
  return mount(ChannelCustomizationView, {
    global: {
      plugins: [i18n],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { template: '<div v-if="show"><slot /><slot name="footer" /></div>', props: ['show'] },
        ConfirmDialog: true,
        Icon: true,
        Toggle: { template: '<button @click="$emit(\'update:modelValue\', !modelValue)"><slot /></button>', props: ['modelValue'] }
      }
    }
  })
}

describe('ChannelCustomizationView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAllGroups.mockResolvedValue([
      { id: 2, name: 'gpt-pro', platform: 'openai', status: 'active' },
      { id: 3, name: 'retired-group', platform: 'openai', status: 'inactive' }
    ])
    getSettings.mockResolvedValue({ observer: { enabled: true, api_key_ids: [], api_key_names: ['maibon-gpt'], user_ids: [], user_emails: ['1069167864@qq.com'], output_path: '.tmp/observer.jsonl' }, rules: [rule] })
    updateSettings.mockResolvedValue({ observer: { enabled: true, api_key_ids: [], api_key_names: ['maibon-gpt'], user_ids: [], user_emails: ['1069167864@qq.com'], output_path: '.tmp/observer.jsonl' }, rules: [rule] })
  })

  it('loads rules and existing request observer settings', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="customization-rules"]').text()).toContain('maibon')
    expect(wrapper.get('[data-testid="customization-rule-hits-0"]').text()).toContain('12')
    expect(wrapper.get('[data-testid="gateway-request-observer-settings"]').text()).toContain('.tmp/observer.jsonl')
  })

  it('renders safely when the backend returns null rule fields', async () => {
    getSettings.mockResolvedValueOnce({
      observer: { enabled: false, api_key_ids: null, api_key_names: null, user_ids: null, user_emails: null, output_path: null },
      rules: [{ name: 'legacy', enabled: true, api_key_ids: null, api_key_names: null, user_ids: null, user_emails: null, methods: null, exact_paths: null, path_prefixes: null, user_agent_contains: null, query_params: null, request_message_text: null, min_delay_ms: null, max_delay_ms: null, status_code: null, content_type: null, response_body: null }]
    })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="customization-rule-0"]').text()).toContain('legacy')
    expect(wrapper.get('[data-testid="gateway-request-observer-settings"]').text()).toContain('/app/.tmp/maibon-probe-observation/requests.jsonl')
  })

  it('blocks saving when settings cannot be loaded', async () => {
    getSettings.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('header button.btn-primary').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="customization-rules"]').exists()).toBe(false)
    expect(updateSettings).not.toHaveBeenCalled()
  })

  it('validates a rule target before calling the API', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-create-rule"]').trigger('click')
    await wrapper.get('[data-testid="customization-rule-form"]').trigger('submit')
    await wrapper.get('header button.btn-primary').trigger('click')
    expect(showError).toHaveBeenCalled()
    expect(updateSettings).not.toHaveBeenCalled()
  })

  it('keeps local response settings usable when target groups fail to load', async () => {
    getAllGroups.mockRejectedValueOnce(new Error('groups unavailable'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('header button.btn-primary').trigger('click')

    expect(showError).toHaveBeenCalled()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      rules: [expect.objectContaining({ action: 'local_response' })]
    }))
  })

  it('preserves the regex message match mode when saving a rule', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-rule-0"] button[title="admin.customization.edit"]').trigger('click')
    await wrapper.get('[data-testid="customization-message-match-mode"]').setValue('regex')
    await wrapper.get('[data-testid="customization-rule-form"] input.font-mono').setValue('^hi$')
    await wrapper.get('[data-testid="customization-rule-form"]').trigger('submit')
    await wrapper.get('header button.btn-primary').trigger('click')
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      rules: [expect.objectContaining({ action: 'local_response', request_message_match_mode: 'regex', request_message_text: '^hi$' })]
    }))
  })

  it('loads and saves request model conditions', async () => {
    getSettings.mockResolvedValueOnce({
      observer: { enabled: true, api_key_ids: [], api_key_names: [], user_ids: [], user_emails: [], output_path: '.tmp/observer.jsonl' },
      rules: [{ ...rule, models: ['gpt-6-astra'] }]
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-rule-0"] button[title="admin.customization.edit"]').trigger('click')

    const modelField = wrapper.get('[data-testid="customization-rule-form"] textarea.font-mono')
    expect((modelField.element as HTMLTextAreaElement).value).toBe('gpt-6-astra')
    await modelField.setValue('gpt-6-astra\ngpt-5.6-luna')
    await wrapper.get('[data-testid="customization-rule-form"]').trigger('submit')
    await wrapper.get('header button.btn-primary').trigger('click')

    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      rules: [expect.objectContaining({ models: ['gpt-6-astra', 'gpt-5.6-luna'] })]
    }))
  })

  it('allows a model-only condition when saving a rule', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-create-rule"]').trigger('click')
    const form = wrapper.get('[data-testid="customization-rule-form"]')
    await form.get('input.input').setValue('map-astra')
    await form.findAll('textarea').at(0)?.setValue('maibon-gpt')
    await form.get('textarea.font-mono').setValue('gpt-6-astra')
    await form.trigger('submit')
    await wrapper.get('header button.btn-primary').trigger('click')

    expect(showError).not.toHaveBeenCalled()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      rules: expect.arrayContaining([expect.objectContaining({ name: 'map-astra', models: ['gpt-6-astra'] })])
    }))
  })

  it('defaults legacy rules to the local response action', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-rule-0"] button[title="admin.customization.edit"]').trigger('click')
    expect((wrapper.get('[data-testid="customization-action-select"]').element as HTMLSelectElement).value).toBe('local_response')
    expect(wrapper.get('[data-testid="customization-local-response-fields"]').exists()).toBe(true)
  })

  it('shows active target groups and saves a group mapping action', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-rule-0"] button[title="admin.customization.edit"]').trigger('click')
    await wrapper.get('[data-testid="customization-action-select"]').setValue('group_mapping')

    expect(wrapper.find('[data-testid="customization-local-response-fields"]').exists()).toBe(false)
    const targetSelect = wrapper.get('[data-testid="customization-target-group-select"]')
    expect(targetSelect.text()).toContain('gpt-pro')
    expect(targetSelect.text()).not.toContain('retired-group')

    await targetSelect.setValue('2')
    await wrapper.get('[data-testid="customization-rule-form"]').trigger('submit')
    expect(wrapper.get('[data-testid="customization-rule-target-group-0"]').text()).toContain('gpt-pro')
    await wrapper.get('header button.btn-primary').trigger('click')

    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      rules: [expect.objectContaining({ action: 'group_mapping', target_group_id: 2 })]
    }))
  })

  it('rejects a group mapping without an active target group', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="customization-rule-0"] button[title="admin.customization.edit"]').trigger('click')
    await wrapper.get('[data-testid="customization-action-select"]').setValue('group_mapping')
    await wrapper.get('[data-testid="customization-rule-form"]').trigger('submit')
    await wrapper.get('header button.btn-primary').trigger('click')

    expect(showError).toHaveBeenCalled()
    expect(updateSettings).not.toHaveBeenCalled()
  })
})
