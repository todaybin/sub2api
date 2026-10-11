import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PointsHistoryDialog from '../PointsHistoryDialog.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: mocks }))
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())
const entry = (id: number) => ({ id, amount: -100, balance_after: -70, cost_usd: 1, points_per_usd: 100, kind: 'usage', notes: `receipt ${id}`, created_at: '2026-10-11T00:00:00Z' })
function render(userId?: number) {
  return mount(PointsHistoryDialog, { props: { show: true, userId },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
}

describe('points history', () => {
  it('uses the current-user endpoint and paginates without losing the page after an error', async () => {
    mocks.get.mockResolvedValueOnce({ data: { items: [entry(1)], total: 21 } }).mockRejectedValueOnce({ message: 'Retry later' })
    const wrapper = render()
    await flushPromises()
    expect(mocks.get).toHaveBeenCalledWith('/user/points-history', { params: { page: 1, page_size: 20 } })
    await wrapper.findAll('button')[1]!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Retry later')
    expect(wrapper.text()).toContain('receipt 1')
    expect(wrapper.text()).toContain('1 / 2')
  })

  it('ignores history from the previous user that arrives after a user switch', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(r => { resolve = r }))
      .mockResolvedValueOnce({ data: { items: [entry(2)], total: 1 } })
    const wrapper = render(1)
    await wrapper.setProps({ userId: 2 })
    await flushPromises()
    resolve({ data: { items: [entry(1)], total: 40 } })
    await flushPromises()
    expect(wrapper.text()).toContain('receipt 2')
    expect(wrapper.text()).not.toContain('receipt 1')
    expect(wrapper.text()).toContain('1 / 1')
  })
})
