import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import PluginSecretsDialog from '../PluginSecretsDialog.vue'

const wrappers: VueWrapper[] = []
function mountDialog() {
  const wrapper = mount(PluginSecretsDialog, {
    props: { name: 'Example', fields: ['endpoint', 'token'], configured: { endpoint: true, token: false }, loading: false, ready: true, saving: false, error: '' },
    global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: {} }, missingWarn: false, fallbackWarn: false })],
      stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } },
  })
  wrappers.push(wrapper)
  return wrapper
}
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount() })

describe('trusted plugin secret dialog', () => {
  it('defaults every field to keep, with no stored value in an input', async () => {
    const wrapper = mountDialog()
    expect(wrapper.findAll('input')).toHaveLength(0)
    expect(wrapper.findAll<HTMLSelectElement>('select').every(select => select.element.value === 'keep')).toBe(true)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toEqual([[{}]])
  })

  it('uses an empty password input for replacement and sends empty only for explicit clear', async () => {
    const wrapper = mountDialog()
    await wrapper.get('[data-secret-mode="endpoint"]').setValue('replace')
    const input = wrapper.get<HTMLInputElement>('[data-secret-value="endpoint"]')
    expect(input.element.type).toBe('password')
    expect(input.element.value).toBe('')
    expect(wrapper.get('[data-testid="secrets-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    await input.setValue('NEW_PRIVATE')
    await wrapper.get('[data-secret-mode="token"]').setValue('clear')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toEqual([[{ endpoint: 'NEW_PRIVATE', token: '' }]])
    await wrapper.get('[data-secret-mode="endpoint"]').setValue('keep')
    await wrapper.get('[data-secret-mode="endpoint"]').setValue('replace')
    expect(wrapper.get<HTMLInputElement>('[data-secret-value="endpoint"]').element.value).toBe('')
  })

  it('cancels without emitting values and blocks writes while loading or saving', async () => {
    const wrapper = mountDialog()
    await wrapper.get('[data-testid="secrets-cancel"]').trigger('click')
    expect(wrapper.emitted('cancel')).toEqual([[]])
    expect(wrapper.emitted('save')).toBeUndefined()
    await wrapper.setProps({ ready: false, loading: true })
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    await wrapper.setProps({ ready: true, loading: false, saving: true })
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })
})
