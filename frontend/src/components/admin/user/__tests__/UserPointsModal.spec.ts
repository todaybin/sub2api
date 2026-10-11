import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserPointsModal from '../UserPointsModal.vue'
import type { AdminUser } from '@/types'

const mocks = vi.hoisted(() => ({ updatePoints: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin/users', () => ({ updatePoints: mocks.updatePoints }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())

function render() {
  return mount(UserPointsModal, {
    props: { show: true, user: { id: 1, email: 'a@example.com', points_balance: -70 } as AdminUser },
    global: { stubs: {
      BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
      PointsHistoryDialog: true,
    } },
  })
}

describe('points adjustments', () => {
  it('keeps the idempotency key after a lost response, including an initially open dialog', async () => {
    mocks.updatePoints.mockRejectedValueOnce({ message: 'Network failure' }).mockResolvedValueOnce({})
    const wrapper = render()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const calls = mocks.updatePoints.mock.calls
    expect(calls[0]?.[4]).toBeTruthy()
    expect(calls[1]?.[4]).toBe(calls[0]?.[4])
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('assigns a new key when the adjustment changes', async () => {
    mocks.updatePoints.mockRejectedValue({ message: 'Network failure' })
    const wrapper = render()
    await wrapper.get('input').setValue('100')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('input').setValue('50')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updatePoints.mock.calls[1]?.[4]).not.toBe(mocks.updatePoints.mock.calls[0]?.[4])
  })

  it('blocks duplicate submissions and does not close a different user dialog on a late response', async () => {
    let resolve!: (value: unknown) => void
    mocks.updatePoints.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = render()
    await wrapper.get('input').setValue('100')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(mocks.updatePoints).toHaveBeenCalledTimes(1)
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ user: { id: 2, email: 'b@example.com' } as AdminUser })
    resolve({})
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
  })
})
