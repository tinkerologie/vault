import { describe, expect, it } from 'vitest'

import { recoveryCounts } from './recovery.js'

const plan = {
  steps: [{ items: [
    { type: 'container', name: 'one', has_restore_point: true },
    { type: 'vm', name: 'two', has_restore_point: false },
    { type: 'folder', name: 'three', has_restore_point: false },
  ] }],
}

describe('recoveryCounts', () => {
  it('falls back to the recovery plan when live discovery is unavailable', () => {
    expect(recoveryCounts({
      plan,
      containers: [], vms: [], folders: [],
      available: { containers: false, vms: false, folders: true },
      enabled: { containers: true, vms: true, folders: true, flash: true },
    })).toEqual({ totalItems: 3, totalUnprotected: 2 })
  })

  it('deduplicates the same configured item across jobs', () => {
    const duplicatePlan = {
      steps: [
        { items: [{ type: 'container', name: 'one', has_restore_point: false }] },
        { items: [{ type: 'container', name: 'one', has_restore_point: true }] },
      ],
    }
    expect(recoveryCounts({
      plan: duplicatePlan,
      containers: [], vms: [], folders: [],
      available: { containers: false, vms: false, folders: false },
      enabled: { containers: true, vms: false, folders: false, flash: false },
    })).toEqual({ totalItems: 1, totalUnprotected: 0 })
  })

  it('excludes disabled backup types from readiness', () => {
    expect(recoveryCounts({
      plan,
      containers: [], vms: [], folders: [],
      available: { containers: false, vms: false, folders: false },
      enabled: { containers: false, vms: false, folders: false, flash: false },
    })).toEqual({ totalItems: 0, totalUnprotected: 0 })
  })

  it('keeps regular folders and flash drives behind their own settings', () => {
    const folderPlan = { steps: [{ items: [
      { type: 'folder', name: 'share', has_restore_point: false },
      { type: 'folder', name: 'boot', preset: 'flash', has_restore_point: false },
    ] }] }
    const common = {
      plan: folderPlan,
      containers: [], vms: [], folders: [],
      available: { containers: false, vms: false, folders: false },
    }
    expect(recoveryCounts({
      ...common,
      enabled: { containers: false, vms: false, folders: true, flash: false },
    })).toEqual({ totalItems: 1, totalUnprotected: 1 })
    expect(recoveryCounts({
      ...common,
      enabled: { containers: false, vms: false, folders: false, flash: true },
    })).toEqual({ totalItems: 1, totalUnprotected: 1 })
  })

  it('keeps configured custom folders but drops stale discovered presets', () => {
    const folderPlan = { steps: [{ items: [
      { type: 'folder', name: 'custom', has_restore_point: false },
      { type: 'folder', name: 'old-flash', preset: 'flash', has_restore_point: false },
    ] }] }
    expect(recoveryCounts({
      plan: folderPlan,
      containers: [], vms: [], folders: [],
      available: { containers: false, vms: false, folders: true },
      enabled: { containers: false, vms: false, folders: true, flash: true },
    })).toEqual({ totalItems: 1, totalUnprotected: 1 })
  })
})
