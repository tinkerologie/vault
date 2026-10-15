import { describe, it, expect } from 'vitest'
import { verifyCostWarning } from './verify-cost.js'

const storage = [
  { id: 1, type: 'local' },
  { id: 2, type: 's3' },
  { id: 3, type: 'sftp' },
  { id: 4, type: 'smb' },
]

describe('verifyCostWarning', () => {
  it('warns with provider charges for S3 when verification is on', () => {
    expect(verifyCostWarning(storage, 2, true)).toEqual({ providerCharges: true })
  })

  it('warns without provider charges for other remote destinations', () => {
    expect(verifyCostWarning(storage, 3, true)).toEqual({ providerCharges: false })
    expect(verifyCostWarning(storage, 4, true)).toEqual({ providerCharges: false })
  })

  it('does not warn when verification is off', () => {
    expect(verifyCostWarning(storage, 2, false)).toBeNull()
  })

  it('does not warn for a local destination', () => {
    expect(verifyCostWarning(storage, 1, true)).toBeNull()
  })

  it('does not warn when no destination is selected or it is unknown', () => {
    expect(verifyCostWarning(storage, null, true)).toBeNull()
    expect(verifyCostWarning(storage, 99, true)).toBeNull()
    expect(verifyCostWarning(undefined, 2, true)).toBeNull()
  })
})
