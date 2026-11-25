import { describe, expect, it } from 'vitest'
import { classifyProtection, protectedPercent, protectionState } from './protection.js'

const SUMMARY = {
  protected_keys: ['container:plex', 'container:grafana', 'folder:Flash Drive', 'folder:/mnt/user/docs'],
  out_of_date_keys: ['container:grafana'],
  pending_keys: ['vm:win11'],
  item_last_backup_at: { 'container:grafana': '2026-11-16T02:00:00Z' },
}
const DISCOVERED = {
  containers: [{ name: 'plex' }, { name: 'grafana' }, { name: 'telegraf' }],
  vms: [{ name: 'win11' }],
  folders: [{ name: 'Flash Drive', settings: { preset: 'flash' } }],
}
const ALL_AVAILABLE = { containers: true, vms: true, folders: true }

describe('protectionState', () => {
  it.each([
    ['container:plex', 'protected'],
    ['container:grafana', 'out_of_date'],
    ['vm:win11', 'pending'],
    ['container:telegraf', 'unprotected'],
  ])('%s is %s', (key, state) => {
    expect(protectionState(key, SUMMARY)).toBe(state)
  })

  it('is unknown, never protected, without a summary', () => {
    expect(protectionState('container:plex', null)).toBe('unknown')
  })
})

describe('classifyProtection', () => {
  it('counts every item once, in one state', () => {
    const result = classifyProtection({ summary: SUMMARY, discovered: DISCOVERED, available: ALL_AVAILABLE })
    expect(result.counts).toEqual({ total: 6, protected: 3, outOfDate: 1, pending: 1, unprotected: 1 })
    expect(result.known).toBe(true)
  })

  it('splits the Flash Drive from custom folders, which only jobs know about', () => {
    const { groups } = classifyProtection({ summary: SUMMARY, discovered: DISCOVERED, available: ALL_AVAILABLE })
    expect(groups.flash.map((it) => it.name)).toEqual(['Flash Drive'])
    expect(groups.folder.map((it) => it.name)).toEqual(['/mnt/user/docs'])
  })

  it('carries the last backup time for an out-of-date item', () => {
    const { groups } = classifyProtection({ summary: SUMMARY, discovered: DISCOVERED, available: ALL_AVAILABLE })
    const grafana = groups.container.find((it) => it.name === 'grafana')
    expect(grafana).toMatchObject({ state: 'out_of_date', lastBackupAt: '2026-11-16T02:00:00Z' })
  })

  it('falls back to job items when discovery is unavailable', () => {
    const result = classifyProtection({ summary: SUMMARY, discovered: {}, available: {} })
    expect(result.groups.container.map((it) => it.name)).toEqual(['plex', 'grafana'])
    expect(result.groups.vm.map((it) => it.name)).toEqual(['win11'])
    expect(result.groups.folder.map((it) => it.name)).toEqual(['Flash Drive', '/mnt/user/docs'])
    expect(result.counts.unprotected).toBe(0)
  })

  it('leaves out item types that are switched off', () => {
    const result = classifyProtection({
      summary: SUMMARY, discovered: DISCOVERED, available: ALL_AVAILABLE,
      enabled: { containers: false, vms: true, folders: false, flash: true },
    })
    expect(result.items.map((it) => it.key)).toEqual(['vm:win11', 'folder:Flash Drive'])
  })

  it('lists a discovered item only once', () => {
    const result = classifyProtection({ summary: SUMMARY, discovered: { containers: [{ name: 'plex' }, { name: 'plex' }] }, available: { containers: true } })
    expect(result.groups.container).toHaveLength(1)
  })

  it('knows nothing without a summary', () => {
    const result = classifyProtection({ summary: null, discovered: DISCOVERED, available: ALL_AVAILABLE })
    expect(result.known).toBe(false)
    expect(result.counts).toMatchObject({ total: 5, protected: 0, unprotected: 0 })
    expect(result.items.every((it) => it.state === 'unknown')).toBe(true)
  })
})

describe('protectedPercent', () => {
  it('rounds the protected share', () => {
    expect(protectedPercent({ total: 3, protected: 2 })).toBe(67)
  })

  it('is 0 with no items', () => {
    expect(protectedPercent({ total: 0, protected: 0 })).toBe(0)
  })
})
