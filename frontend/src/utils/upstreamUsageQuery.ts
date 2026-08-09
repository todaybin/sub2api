export type UpstreamUsageQueryMode = 'auto' | 'generic' | 'new_api' | 'custom'

export function genericUpstreamUsageTemplate(): Record<string, unknown> {
  return {
    version: 1,
    template_type: 'generic',
    result_type: 'balance',
    request: {
      url: '{{rootUrl}}/user/balance',
      method: 'GET',
      headers: { Authorization: 'Bearer {{apiKey}}', 'User-Agent': 'cc-switch/1.0' }
    },
    mapping: {
      is_valid: { default: true },
      remaining: { path: 'balance' },
      unit: { path: 'currency', default: 'USD' }
    }
  }
}

export function newAPIUpstreamUsageTemplate(): Record<string, unknown> {
  return {
    version: 1,
    template_type: 'new_api',
    result_type: 'balance',
    request: {
      url: '{{rootUrl}}/api/user/self',
      method: 'GET',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Bearer {{accessToken}}',
        'User-Agent': 'cc-switch/1.0',
        'New-Api-User': '{{userId}}'
      }
    },
    mapping: {
      is_valid: { path: 'success', default: false },
      invalid_message: { path: 'message', default: 'query failed' },
      plan_name: { path: 'data.group', default: 'default' },
      remaining: { path: 'data.quota', divisor: 500000 },
      used: { path: 'data.used_quota', divisor: 500000 },
      total: { derive: 'remaining_plus_used' },
      unit: { default: 'USD' }
    }
  }
}

export function customUpstreamUsageTemplate(): Record<string, unknown> {
  return {
    version: 1,
    template_type: 'custom',
    result_type: 'balance',
    variables: {
      tenantId: { value: '', secret: false }
    },
    request: {
      url: '{{rootUrl}}/user/balance',
      method: 'GET',
      query: {},
      headers: {
        Authorization: 'Bearer {{apiKey}}',
        'User-Agent': 'sub2api/usage-probe'
      }
    },
    mapping: {
      is_valid: { path: 'is_active', default: true },
      invalid_message: { path: 'message' },
      plan_name: { path: 'plan_name' },
      remaining: { path: 'balance' },
      used: { path: 'used' },
      total: { path: 'total' },
      unit: { path: 'currency', default: 'USD' },
      extra: { path: 'data' }
    }
  }
}

export function templateJSON(mode: UpstreamUsageQueryMode): string {
  if (mode === 'generic') return JSON.stringify(genericUpstreamUsageTemplate(), null, 2)
  if (mode === 'new_api') return JSON.stringify(newAPIUpstreamUsageTemplate(), null, 2)
  if (mode === 'custom') return JSON.stringify(customUpstreamUsageTemplate(), null, 2)
  return ''
}
