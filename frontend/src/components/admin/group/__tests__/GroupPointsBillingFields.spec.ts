import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import GroupPointsBillingFields from '../GroupPointsBillingFields.vue'
import { isValidGroupPointsRate } from '@/utils/points'

vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, values?: Record<string, unknown>) => values ? `${key} ${JSON.stringify(values)}` : key,
}) }))
enableAutoUnmount(afterEach)

describe('group points billing fields', () => {
  it('shows the group rate and example, and emits changes', async () => {
    const wrapper = mount(GroupPointsBillingFields, { props: { enabled: true, rate: 100 } })
    expect(wrapper.get<HTMLInputElement>('input').element.value).toBe('100')
    expect(wrapper.text()).toContain('"points":2')
    await wrapper.get('input').setValue('250')
    expect(wrapper.emitted('update:rate')).toEqual([[250]])
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('update:enabled')).toEqual([[false]])
  })

  it('hides the rate when disabled without overwriting its value', async () => {
    const wrapper = mount(GroupPointsBillingFields, { props: { enabled: false, rate: 250 } })
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('update:enabled')).toEqual([[true]])
    expect(wrapper.emitted('update:rate')).toBeUndefined()
    await wrapper.setProps({ enabled: true })
    expect(wrapper.get<HTMLInputElement>('input').element.value).toBe('250')
  })

  it('requires a finite positive rate when points billing is enabled', () => {
    for (const rate of [0, -1, 1e-9, 1e12, NaN, Infinity]) {
      expect(isValidGroupPointsRate(true, rate)).toBe(false)
    }
    expect(isValidGroupPointsRate(false, 0)).toBe(true)
    for (const rate of [1e-8, 100, 999999999999]) {
      expect(isValidGroupPointsRate(true, rate)).toBe(true)
    }
  })
})
