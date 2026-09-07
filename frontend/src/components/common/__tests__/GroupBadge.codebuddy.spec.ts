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

describe('GroupBadge CodeBuddy billing details', () => {
  it('renders model-level upstream and final costs in the hover content', () => {
    const wrapper = mount(GroupBadge, {
      props: {
        name: 'CodeBuddy',
        platform: 'codebuddy',
        rateMultiplier: 1.2,
        rateMode: 'dynamic',
        codeBuddyBilling: {
          reference_cost_units: 70,
          reference_credits: 2000,
          tokens_per_credit: 31874,
          group_rate_mode: 'dynamic',
          group_rate_multiplier: 1.2,
          dynamic_profit_rate: 0.2,
          dynamic_profit_rate_percent: 20,
          models: [{
            model: 'hy3',
            credits_multiplier: 0.79,
            upstream_cost_per_token: 0.00000086,
            final_cost_per_token: 0.00000103,
            final_multiplier: 0.948,
          }],
        },
      },
      global: {
        stubs: {
          PlatformIcon: true,
          HelpTooltip: {
            template: '<div data-test="billing-tooltip"><slot name="trigger" /><slot /></div>',
          },
        },
      },
    })

    const tooltip = wrapper.get('[data-test="billing-tooltip"]')
    expect(tooltip.text()).toContain('hy3')
    expect(tooltip.text()).toContain('keys.codeBuddyBillingUpstream')
    expect(tooltip.text()).toContain('keys.codeBuddyBillingFinal')
  })
})
