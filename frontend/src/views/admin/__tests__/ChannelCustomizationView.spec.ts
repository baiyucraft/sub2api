import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ChannelCustomizationView from '../ChannelCustomizationView.vue'

const { getSettings, updateSettings, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(), updateSettings: vi.fn(), showError: vi.fn(), showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelCustomization: { getSettings, updateSettings }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))

const messages = { en: { common: { loading: 'Loading', saving: 'Saving', error: 'Error' }, admin: { channels: { customization: new Proxy({}, { get: (_, key) => String(key) }) } } } }
const i18n = createI18n({ legacy: false, locale: 'en', messages })
const rule = { name: 'maibon', enabled: true, api_key_ids: [], api_key_names: ['maibon-gpt'], user_ids: [], user_emails: ['1069167864@qq.com'], methods: ['GET'], exact_paths: ['/v1/models'], path_prefixes: [], user_agent_contains: [], query_params: {}, min_delay_ms: 100, max_delay_ms: 300, status_code: 200, content_type: 'application/json', response_body: '{}', hit_count: 12 }

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
})
