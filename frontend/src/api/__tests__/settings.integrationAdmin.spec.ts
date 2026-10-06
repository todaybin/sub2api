import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, delete: remove } }))

import {
  getIntegrationAdminCredentials,
  regenerateIntegrationAdminCredentials,
  deleteIntegrationAdminCredentials,
  type IntegrationAdminCredentialsStatus,
  type GeneratedIntegrationAdminCredentials
} from '@/api/admin/settings'

describe('admin integration credential API contract', () => {
  beforeEach(() => vi.clearAllMocks())

  it('returns masked status and new AppID / Secret fields using unchanged settings URLs', async () => {
    const status: IntegrationAdminCredentialsStatus = { exists: true, masked_appid: '01234567...cdef' }
    const credentials: GeneratedIntegrationAdminCredentials = {
      appid: '0123456789abcdef0123456789abcdef', secret: '0123456789abcdef'.repeat(4)
    }
    get.mockResolvedValueOnce({ data: status })
    post.mockResolvedValueOnce({ data: credentials })
    remove.mockResolvedValueOnce({ data: { message: 'deleted' } })

    await expect(getIntegrationAdminCredentials()).resolves.toEqual(status)
    await expect(regenerateIntegrationAdminCredentials()).resolves.toEqual(credentials)
    await expect(deleteIntegrationAdminCredentials()).resolves.toEqual({ message: 'deleted' })
    expect(get).toHaveBeenCalledWith('/admin/settings/integration-admin')
    expect(post).toHaveBeenCalledWith('/admin/settings/integration-admin/regenerate')
    expect(remove).toHaveBeenCalledWith('/admin/settings/integration-admin')
  })
})
