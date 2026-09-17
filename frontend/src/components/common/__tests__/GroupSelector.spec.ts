import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupSelector from '../GroupSelector.vue'

const authState = { isSimpleMode: false }

vi.mock('@/stores', () => ({ useAuthStore: () => authState }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 1, name: 'Basic', platform: 'anthropic', status: 'active' },
  { id: 2, name: 'Composite', platform: 'composite', status: 'active' }
] as any

const mountSelector = (modelValue: number[] = [], preferredGroupIds?: number[]) => mount(GroupSelector, {
  props: {
    modelValue,
    groups,
    ...(preferredGroupIds === undefined ? {} : { preferredGroupIds })
  },
  global: { stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true } }
})

describe('GroupSelector simple-mode binding policy', () => {
  beforeEach(() => { authState.isSimpleMode = false })

  it('hides composite groups in simple mode and preserves basic groups', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Basic')
    expect(wrapper.text()).not.toContain('Composite')
  })

  it('keeps composite groups available in advanced mode', () => {
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Composite')
  })

  it('cleans hidden historical composite IDs while preserving visible selections', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector([1, 2])
    expect(wrapper.emitted('update:modelValue')).toEqual([[[1]]])
  })

  it('keeps the legacy UI unchanged when preferred groups are not bound', () => {
    const wrapper = mountSelector([1])
    expect(wrapper.find('[data-testid="preferred-group-1"]').exists()).toBe(false)
  })

  it('only allows selected groups to be marked preferred', async () => {
    const wrapper = mountSelector([], [])
    expect(wrapper.get('[data-testid="preferred-group-1"]').attributes('disabled')).toBeDefined()

    await wrapper.setProps({ modelValue: [1] })
    await wrapper.get('[data-testid="preferred-group-1"]').trigger('click')

    expect(wrapper.emitted('update:preferredGroupIds')).toEqual([[[1]]])
  })

  it('removes preferred status when a selected group is unchecked', async () => {
    const wrapper = mountSelector([1], [1])
    await wrapper.get<HTMLInputElement>('input[value="1"]').setValue(false)

    expect(wrapper.emitted('update:modelValue')).toEqual([[[]]])
    expect(wrapper.emitted('update:preferredGroupIds')).toEqual([[[]]])
  })

  it('normalizes preferred groups to the selected group subset', () => {
    const wrapper = mountSelector([1], [1, 2, 2])
    expect(wrapper.emitted('update:preferredGroupIds')).toEqual([[[1]]])
  })

  it('does not mutate group or preferred state while disabled', async () => {
    const wrapper = mountSelector([1], [1])
    await wrapper.setProps({ disabled: true })

    expect(wrapper.get<HTMLInputElement>('input[value="1"]').element.disabled).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-testid="preferred-group-1"]').element.disabled).toBe(true)
    await wrapper.get('[data-testid="preferred-group-1"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.emitted('update:preferredGroupIds')).toBeUndefined()
  })
})
