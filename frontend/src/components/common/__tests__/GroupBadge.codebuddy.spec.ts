import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupBadge from '../GroupBadge.vue'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values?: Record<string, unknown>) => `${key}:${JSON.stringify(values ?? {})}`,
  }),
}))

describe('GroupBadge CodeBuddy rates', () => {
  it('shows the group rate and dynamic adjustment, then respects a user rate override', async () => {
    const wrapper = mount(GroupBadge, {
      props: {
        name: 'CodeBuddy',
        platform: 'codebuddy',
        rateMultiplier: 1.2,
        rateMode: 'dynamic',
        dynamicRateLastDirection: 'increase',
        dynamicRateLastChange: 0.2,
      },
      global: {
        stubs: {
          PlatformIcon: true,
        },
      },
    })

    expect(wrapper.text()).toContain('CodeBuddy')
    expect(wrapper.text()).toContain('1.2x')
    expect(wrapper.text()).toContain('groups.dynamicRateIncrease:{} +0.2x')

    await wrapper.setProps({ userRateMultiplier: 0 })
    expect(wrapper.get('.line-through').text()).toBe('1.2x')
    expect(wrapper.get('.font-bold').text()).toBe('0x')
    expect(wrapper.text()).not.toContain('groups.dynamicRateIncrease')

    await wrapper.setProps({ showRate: false })
    expect(wrapper.text()).toBe('CodeBuddy')
    wrapper.unmount()
  })
})
