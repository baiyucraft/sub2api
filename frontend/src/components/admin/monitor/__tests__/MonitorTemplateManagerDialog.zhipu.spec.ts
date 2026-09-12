import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import MonitorTemplateManagerDialog from '@/components/admin/monitor/MonitorTemplateManagerDialog.vue'

const { listTemplates, createTemplate, updateTemplate, deleteTemplate } = vi.hoisted(() => ({
  listTemplates: vi.fn(),
  createTemplate: vi.fn(),
  updateTemplate: vi.fn(),
  deleteTemplate: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitorTemplate: {
      list: listTemplates,
      create: createTemplate,
      update: updateTemplate,
      del: deleteTemplate,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

describe('MonitorTemplateManagerDialog Zhipu api mode', () => {
  beforeEach(() => {
    listTemplates.mockReset().mockResolvedValue({ items: [] })
    createTemplate.mockReset().mockResolvedValue({})
    updateTemplate.mockReset().mockResolvedValue({})
    deleteTemplate.mockReset().mockResolvedValue(undefined)
  })

  it('exposes all three monitor protocols for Zhipu templates', async () => {
    const wrapper = mount(MonitorTemplateManagerDialog, {
      props: { show: true },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          ConfirmDialog: true,
          Icon: true,
          MonitorAdvancedRequestConfig: true,
          MonitorTemplateApplyPickerDialog: true,
        },
      },
    })
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('monitorCommon.providers.zhipu'))
      ?.trigger('click')
    await wrapper.get('button.btn-primary').trigger('click')

    expect(wrapper.get('[data-testid="monitor-template-api-mode"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid^="monitor-template-api-mode-"]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="monitor-template-api-mode-responses"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="monitor-template-api-mode-zhipu_native"]').exists()).toBe(true)

    await wrapper.get('[data-testid="monitor-template-api-mode-responses"]').trigger('click')

    await wrapper.get('input[placeholder="admin.channelMonitor.template.form.namePlaceholder"]').setValue('zhipu template')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(createTemplate).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'zhipu',
      api_mode: 'responses',
    }))
  })
})
