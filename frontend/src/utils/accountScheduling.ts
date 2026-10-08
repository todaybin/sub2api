import type { Account } from '@/types'

export function accountRecoveryHint(account: Account): string {
  if (account.schedulable) return ''
  const extra = account.extra
  if (extra?.scheduling_manual_paused_at) return 'admin.accounts.status.manualRecoveryRequired'
  if (typeof extra?.balance_auto_restore_token === 'string' && extra.balance_auto_restore_token.length > 0) {
    return account.status === 'active'
      ? 'admin.accounts.status.balanceAutoRecovery'
      : 'admin.accounts.status.manualRecoveryRequired'
  }
  const checkin = extra?.codebuddy_checkin as Record<string, unknown> | undefined
  if (extra?.upstream_billing_balance_auto_disabled_at || checkin?.auto_disabled_by_balance === true) {
    return 'admin.accounts.status.legacyPauseConfirmation'
  }
  return 'admin.accounts.status.manualRecoveryRequired'
}
