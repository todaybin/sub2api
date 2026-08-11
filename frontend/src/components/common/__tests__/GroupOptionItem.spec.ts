import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupOptionItem from '../GroupOptionItem.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

describe('GroupOptionItem description layout', () => {
  it('applies multiline and overflow-safe text styles', () => {
    const description = 'First section\nvery-long-unbroken-description-value-that-must-not-overflow'
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Example group',
        platform: 'openai',
        description,
      },
      global: {
        stubs: {
          GroupBadge: true,
        },
      },
    })

    const descriptionElement = wrapper
      .findAll('span')
      .find((element) => element.text() === description)

    expect(descriptionElement).toBeDefined()
    expect(descriptionElement?.classes()).toContain('whitespace-pre-line')
    expect(descriptionElement?.classes()).toContain('[overflow-wrap:anywhere]')
    expect(descriptionElement?.classes()).toContain('line-clamp-3')
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
  })

  it('shows dynamic direction unless a user-specific rate overrides the group rate', () => {
    const dynamic = mount(GroupOptionItem, {
      props: {
        name: 'Dynamic group',
        platform: 'openai',
        rateMultiplier: 0.96,
        rateMode: 'dynamic',
        dynamicRateLastDirection: 'increase',
      },
      global: { stubs: { GroupBadge: true } },
    })
    expect(dynamic.text()).toContain('groups.dynamicRateIncrease')

    const overridden = mount(GroupOptionItem, {
      props: {
        name: 'Dynamic group',
        platform: 'openai',
        rateMultiplier: 0.96,
        userRateMultiplier: 0.7,
        rateMode: 'dynamic',
        dynamicRateLastDirection: 'increase',
      },
      global: { stubs: { GroupBadge: true } },
    })
    expect(overridden.text()).not.toContain('groups.dynamicRateIncrease')

    const equalOverride = mount(GroupOptionItem, {
      props: {
        name: 'Dynamic group',
        platform: 'openai',
        rateMultiplier: 0.96,
        userRateMultiplier: 0.96,
        rateMode: 'dynamic',
        dynamicRateLastDirection: 'increase',
      },
      global: { stubs: { GroupBadge: true } },
    })
    expect(equalOverride.text()).not.toContain('groups.dynamicRateIncrease')
  })
})
