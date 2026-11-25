import { describe, expect, it } from 'vitest'
import { runStatusLabel } from './statusLabels.js'

describe('runStatusLabel', () => {
  it.each([
    ['completed', 'Completed'],
    ['success', 'Completed'],
    ['failed', 'Failed'],
    ['error', 'Failed'],
    ['partial', 'Partial'],
    ['running', 'Running'],
    ['skipped', 'Skipped'],
    ['cancelled', 'Cancelled'],
    ['pending', 'Queued'],
    ['queued', 'Queued'],
    ['COMPLETED', 'Completed'],
  ])('labels %s as %s', (status, label) => {
    expect(runStatusLabel(status)).toBe(label)
  })

  it('capitalises a status it does not know', () => {
    expect(runStatusLabel('verifying')).toBe('Verifying')
  })

  it('says Unknown for a missing status', () => {
    expect(runStatusLabel('')).toBe('Unknown')
    expect(runStatusLabel(undefined)).toBe('Unknown')
  })
})
