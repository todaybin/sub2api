import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src/components/common/VersionBadge.vue'), 'utf8')

describe('VersionBadge custom release updates', () => {
  it('keeps official notices visible without rendering an update command', () => {
    expect(source).toContain('hasUpdate && !selfUpdateAvailable')
    expect(source).toContain("t('version.waitingForCustomRelease')")
  })

  it('only renders and invokes update for a newer self-update release', () => {
    expect(source).toContain('selfUpdateAvailable && selfUpdateEnabled')
    expect(source).toContain('v{{ selfUpdateVersion }}')
    expect(source).toContain(
      'if (!selfUpdateEnabled.value || !selfUpdateAvailable.value || updating.value) return'
    )
  })

  it('keeps rollback behind its independent backend capability', () => {
    expect(source).toContain('v-if="rollbackEnabled"')
    expect(source).toContain('if (!isAdmin.value || !rollbackEnabled.value) return')
  })
})
