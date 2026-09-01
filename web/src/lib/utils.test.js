import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// Force 24-hour clock so schedule/time strings are deterministic across the
// runner's locale (getHour12() otherwise drives an 'auto' locale branch).
vi.mock('./runtime-config.js', () => ({ getHour12: () => false }))

import {
  formatBytes,
  formatDuration,
  formatSpeed,
  largestBackupsByJob,
  statusColor,
  statusBadge,
  parseConfig,
  describeSchedule,
  relTimeUntil,
  prettyAnomalySummary,
  snapshotMigrationMessage,
  itemDisplayLabel,
  normaliseItemType,
  itemTypeNoun,
  itemTypeCountLabel,
  commonItemType,
  unchangedItemCount,
} from './utils.js'


describe('formatBytes', () => {
  it('handles zero / falsy', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(null)).toBe('0 B')
    expect(formatBytes(undefined)).toBe('0 B')
  })
  it('scales units', () => {
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1048576)).toBe('1 MB')
    expect(formatBytes(1073741824)).toBe('1 GB')
  })

  it('matches the daemon at and beyond the largest unit', () => {
    // internal/format.Bytes renders these identically. PB used to be missing
    // here, so the index ran off the units array and produced '1 undefined'.
    expect(formatBytes(1024 ** 4)).toBe('1 TB')
    expect(formatBytes(1024 ** 5)).toBe('1 PB')
    expect(formatBytes(1024 ** 6)).toBe('1024 PB')
    // Just under a boundary promotes, matching the daemon exactly. The old
    // Math.log index disagreed with it here.
    expect(formatBytes(1024 ** 5 - 1)).toBe('1 PB')
    expect(formatBytes(1024 ** 2 - 1)).toBe('1 MB')
    // Rounding tie and degraded inputs, all matching internal/format.Bytes.
    expect(formatBytes(1280)).toBe('1.3 KB')
    expect(formatBytes(-1536)).toBe('-1.5 KB')
    expect(formatBytes(NaN)).toBe('—')
    expect(formatBytes(Infinity)).toBe('—')
    expect(formatBytes(0.5)).toBe('1 B')
    expect(formatBytes(2.5)).toBe('3 B')
  })
})

describe('formatDuration', () => {
  it('rejects nullish / negative', () => {
    expect(formatDuration(null)).toBe('–')
    expect(formatDuration(-5)).toBe('–')
  })
  it('formats seconds / minutes / hours', () => {
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(90)).toBe('1m 30s')
    expect(formatDuration(3661)).toBe('1h 1m')
  })
})

describe('formatSpeed', () => {
  it('returns null when inputs are missing', () => {
    expect(formatSpeed(0, 1)).toBeNull()
    expect(formatSpeed(100, 0)).toBeNull()
  })
  it('formats bytes/sec', () => {
    expect(formatSpeed(1048576, 1)).toBe('1 MB/s')
  })
  it('inherits the shared contract above GB and at boundaries', () => {
    // Old implementation capped units at GB/s (1 TB/s printed "1024 GB/s")
    // and used Math.log indexing that diverged near unit boundaries.
    expect(formatSpeed(1024 ** 4, 1)).toBe('1 TB/s')
    expect(formatSpeed(1024 ** 5, 1)).toBe('1 PB/s')
    expect(formatSpeed(1024 ** 3, 2)).toBe(formatBytes(1024 ** 3 / 2) + '/s')
  })
})

describe('largestBackupsByJob', () => {
  const names = new Map([[1, 'Nightly'], [2, 'Weekly']])

  it('keeps the largest backup per job, not the most recent', () => {
    // Regression for #286: a full (large) run followed by smaller
    // incrementals must still report the full's size.
    const runs = [
      { job_id: 1, run_type: 'backup', size_bytes: 5, started_at: '2026-08-03' },
      { job_id: 1, run_type: 'backup', size_bytes: 100, started_at: '2026-08-01' }, // the full
      { job_id: 1, run_type: 'backup', size_bytes: 3, started_at: '2026-08-04' },
    ]
    expect(largestBackupsByJob(runs, names)).toEqual([{ name: 'Nightly', size: 100 }])
  })

  it('sorts jobs by size and maps names', () => {
    const runs = [
      { job_id: 1, run_type: 'backup', size_bytes: 10 },
      { job_id: 2, run_type: 'backup', size_bytes: 50 },
    ]
    expect(largestBackupsByJob(runs, names)).toEqual([
      { name: 'Weekly', size: 50 },
      { name: 'Nightly', size: 10 },
    ])
  })

  it('ignores non-backup runs and zero/missing sizes', () => {
    const runs = [
      { job_id: 1, run_type: 'restore', size_bytes: 999 },
      { job_id: 1, run_type: 'backup', size_bytes: 0 },
      { job_id: 1, size_bytes: 7 }, // run_type defaults to backup
    ]
    expect(largestBackupsByJob(runs, names)).toEqual([{ name: 'Nightly', size: 7 }])
  })

  it('falls back to Unknown and applies the limit', () => {
    const runs = [
      { job_id: 9, run_type: 'backup', size_bytes: 4 },
      { job_id: 1, run_type: 'backup', size_bytes: 6 },
    ]
    expect(largestBackupsByJob(runs, names, 1)).toEqual([{ name: 'Nightly', size: 6 }])
    expect(largestBackupsByJob(runs, null)).toEqual([
      { name: 'Unknown', size: 6 },
      { name: 'Unknown', size: 4 },
    ])
  })

  it('handles empty/nullish input', () => {
    expect(largestBackupsByJob(null, names)).toEqual([])
    expect(largestBackupsByJob([], names)).toEqual([])
  })
})

// These two back the cross-cutting "partial status" cluster — lock current
// behaviour so a deliberate change shows up as a failing characterization test.
describe('statusColor / statusBadge', () => {
  it('maps known statuses (case-insensitive)', () => {
    expect(statusColor('partial')).toBe('text-warning')
    expect(statusColor('PENDING')).toBe('text-warning')
    expect(statusColor('failed')).toBe('text-danger')
    expect(statusColor('success')).toBe('text-success')
    expect(statusBadge('partial')).toBe('badge badge-warning')
    expect(statusBadge('success')).toBe('badge badge-success')
  })
  it('falls back for unknown / missing', () => {
    expect(statusColor('nope')).toBe('text-text-muted')
    expect(statusColor(undefined)).toBe('text-text-muted')
    expect(statusBadge('nope')).toBe('badge badge-neutral')
  })
})

describe('parseConfig', () => {
  it('handles empty, object, valid and invalid JSON', () => {
    expect(parseConfig('')).toEqual({})
    expect(parseConfig(null)).toEqual({})
    expect(parseConfig({ a: 1 })).toEqual({ a: 1 })
    expect(parseConfig('{"a":1}')).toEqual({ a: 1 })
    expect(parseConfig('{bad')).toEqual({})
  })
})

describe('describeSchedule', () => {
  it('manual / passthrough', () => {
    expect(describeSchedule('')).toBe('Manual only')
    expect(describeSchedule('not a cron')).toBe('not a cron')
  })
  it('daily / weekly / monthly / yearly', () => {
    expect(describeSchedule('0 2 * * *')).toBe('Daily at 02:00')
    expect(describeSchedule('30 14 * * *')).toBe('Daily at 14:30')
    expect(describeSchedule('0 2 * * 1')).toBe('Weekly on Mon at 02:00')
    expect(describeSchedule('0 2 * * 1,3,5')).toBe('Mon, Wed, Fri at 02:00')
    expect(describeSchedule('0 2 1 * *')).toBe('Monthly on 1st at 02:00')
    expect(describeSchedule('0 2 L * *')).toBe('Monthly on last day at 02:00')
    expect(describeSchedule('0 2 5 6 *')).toBe('Yearly on June 5th at 02:00')
  })
})

describe('relTimeUntil', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
  })
  afterEach(() => vi.useRealTimers())
  const at = (ms) => new Date(Date.now() + ms).toISOString()
  it('handles nullish and past', () => {
    expect(relTimeUntil(null)).toBeNull()
    expect(relTimeUntil(at(-1000))).toBe('overdue')
  })
  it('formats future offsets', () => {
    expect(relTimeUntil(at(30 * 60000))).toBe('in 30m')
    expect(relTimeUntil(at(2 * 3600000))).toBe('in 2h')
    expect(relTimeUntil(at(90 * 60000))).toBe('in 1h 30m')
    expect(relTimeUntil(at(2 * 86400000))).toBe('in 2d')
  })
})

describe('prettyAnomalySummary', () => {
  it('humanizes byte counts and bare-second durations', () => {
    expect(prettyAnomalySummary('size anomaly: 1048576 bytes (0.8x)')).toContain('1 MB')
    expect(prettyAnomalySummary('duration anomaly: 90s (0.8x)')).toContain('1m 30s')
  })

  it('corrects contradictory legacy size summaries without rewriting history', () => {
    expect(prettyAnomalySummary('This backup grew to 2.7 GB, about <1× its usual 2.8 GB.'))
      .toBe('This backup shrank to 2.7 GB, about <1× its usual 2.8 GB.')
  })
})

describe('snapshotMigrationMessage', () => {
  it('falls back when no migration happened', () => {
    expect(snapshotMigrationMessage({}, 'Snapshot path updated')).toEqual({
      text: 'Snapshot path updated',
      tone: 'success',
    })
    expect(snapshotMigrationMessage(null, 'Snapshot path updated').text).toBe('Snapshot path updated')
  })

  it('confirms a completed migration and where the old copy was removed from', () => {
    const { text, tone } = snapshotMigrationMessage({
      migration: { from: '/mnt/cache/.vault/vault.db', to: '/mnt/garbage/vault.db', files_retired: 5, completed: true },
    })
    expect(tone).toBe('success')
    expect(text).toBe('Database migrated to /mnt/garbage/vault.db — 5 files removed from /mnt/cache/.vault/vault.db')
  })

  it('uses the singular for a single retired file', () => {
    const { text } = snapshotMigrationMessage({
      migration: { from: '/old/vault.db', to: '/new/vault.db', files_retired: 1, completed: true },
    })
    expect(text).toContain('1 file removed')
    expect(text).not.toContain('1 files')
  })

  it('omits the cleanup detail when nothing needed removing', () => {
    const { text, tone } = snapshotMigrationMessage({
      migration: { from: '/old/vault.db', to: '/new/vault.db', files_retired: 0, completed: true },
    })
    expect(tone).toBe('success')
    expect(text).toBe('Database migrated to /new/vault.db')
  })

  // An incomplete migration must read as a failure: the database did not move,
  // so the user needs to know it is still at the previous location.
  it('reports a warning as an error', () => {
    const { text, tone } = snapshotMigrationMessage({
      migration: { from: '/old/vault.db', to: '/new/vault.db', warning: 'the new copy could not be verified' },
    })
    expect(tone).toBe('error')
    expect(text).toBe('Database not migrated — the new copy could not be verified')
  })
})

describe('itemDisplayLabel', () => {
  it('returns full path when settings.path is present in JSON string', () => {
    const item = {
      item_type: 'folder',
      item_name: 'appdata',
      item_id: '/mnt/user/appdata',
      settings: '{"path":"/mnt/user/appdata","preset":""}',
    }
    expect(itemDisplayLabel(item)).toBe('/mnt/user/appdata')
  })

  it('returns full path when settings.path is present in an object', () => {
    const item = {
      item_type: 'folder',
      item_name: 'media',
      item_id: '/mnt/user/media',
      settings: { path: '/mnt/user/media', preset: '' },
    }
    expect(itemDisplayLabel(item)).toBe('/mnt/user/media')
  })

  it('falls back to item_id when settings.path is missing but item_id is an absolute path', () => {
    const item = {
      item_type: 'folder',
      item_name: 'documents',
      item_id: '/mnt/user/documents',
      settings: '{}',
    }
    expect(itemDisplayLabel(item)).toBe('/mnt/user/documents')
  })

  it('falls back to item_name when settings.path is missing and item_id is not an absolute path', () => {
    const item = {
      item_type: 'folder',
      item_name: 'custom_folder',
      item_id: 'custom_folder',
      settings: '',
    }
    expect(itemDisplayLabel(item)).toBe('custom_folder')
  })

  it('keeps friendly item_name for preset folder items', () => {
    const flashItem = {
      item_type: 'folder',
      item_name: 'Flash Drive',
      item_id: '/boot',
      settings: '{"path":"/boot","preset":"flash"}',
    }
    expect(itemDisplayLabel(flashItem)).toBe('Flash Drive')

    const flashObjItem = {
      item_type: 'folder',
      item_name: 'Flash Drive',
      item_id: '/boot',
      settings: { path: '/boot', preset: 'flash' },
    }
    expect(itemDisplayLabel(flashObjItem)).toBe('Flash Drive')
  })

  it('returns item_name unchanged for non-folder items without display_name', () => {
    expect(itemDisplayLabel({ item_type: 'container', item_name: 'plex', item_id: '12345' })).toBe('plex')
    expect(itemDisplayLabel({ item_type: 'vm', item_name: 'homeassistant', item_id: 'vmid1' })).toBe('homeassistant')
    expect(itemDisplayLabel({ item_type: 'plugin', item_name: 'community.applications', item_id: 'ca' })).toBe('community.applications')
    expect(itemDisplayLabel({ item_type: 'zfs', item_name: 'tank/data', item_id: 'tank/data' })).toBe('tank/data')
  })

  it('prefers settings.display_name for plugin and other non-folder items when present', () => {
    expect(
      itemDisplayLabel({
        item_type: 'plugin',
        item_name: 'community.applications',
        item_id: 'community.applications',
        settings: '{"display_name":"Community Applications"}',
      }),
    ).toBe('Community Applications')

    expect(
      itemDisplayLabel({
        type: 'plugin',
        name: 'vault',
        settings: { display_name: 'Vault Backup Manager' },
      }),
    ).toBe('Vault Backup Manager')
  })

  it('handles name/type/id aliases on item object', () => {
    const folderItem = {
      type: 'folder',
      name: 'appdata',
      id: '/mnt/user/appdata',
      settings: { path: '/mnt/user/appdata' },
    }
    expect(itemDisplayLabel(folderItem)).toBe('/mnt/user/appdata')

    const vmItem = {
      type: 'vm',
      name: 'windows11',
      id: 'win11',
    }
    expect(itemDisplayLabel(vmItem)).toBe('windows11')
  })

  it('handles malformed or empty settings gracefully without throwing', () => {
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: '{bad json' })).toBe('test')
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: null })).toBe('test')
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: 'null' })).toBe('test')
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: undefined })).toBe('test')
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: 123 })).toBe('test')
    expect(itemDisplayLabel({ item_type: 'folder', item_name: 'test', settings: 'true' })).toBe('test')
  })

  it('handles nullish, empty, or string inputs', () => {
    expect(itemDisplayLabel(null)).toBe('')
    expect(itemDisplayLabel(undefined)).toBe('')
    expect(itemDisplayLabel('')).toBe('')
    expect(itemDisplayLabel('simple-string')).toBe('simple-string')
  })
})

describe('normaliseItemType', () => {
  it('accepts the engine singular spelling', () => {
    expect(normaliseItemType('container')).toBe('container')
    expect(normaliseItemType('zfs')).toBe('zfs')
  })

  it('accepts TypePicker plural discovery ids', () => {
    expect(normaliseItemType('containers')).toBe('container')
    expect(normaliseItemType('vms')).toBe('vm')
    expect(normaliseItemType('plugins')).toBe('plugin')
  })

  it('is case and whitespace insensitive', () => {
    expect(normaliseItemType('  Container ')).toBe('container')
    expect(normaliseItemType('VMs')).toBe('vm')
  })

  it('returns empty string for unknown or absent types', () => {
    expect(normaliseItemType('widget')).toBe('')
    expect(normaliseItemType('')).toBe('')
    expect(normaliseItemType(null)).toBe('')
    expect(normaliseItemType(undefined)).toBe('')
    expect(normaliseItemType(7)).toBe('')
  })

  it('does not strip the trailing s from zfs', () => {
    expect(normaliseItemType('zfs')).toBe('zfs')
  })
})

describe('itemTypeNoun', () => {
  it('pluralises on count', () => {
    expect(itemTypeNoun('container', 1)).toBe('container')
    expect(itemTypeNoun('container', 2)).toBe('containers')
    expect(itemTypeNoun('container', 0)).toBe('containers')
  })

  it('keeps VM capitalisation', () => {
    expect(itemTypeNoun('vm', 1)).toBe('VM')
    expect(itemTypeNoun('vms', 3)).toBe('VMs')
  })

  it('uses multi-word nouns for flash drives', () => {
    expect(itemTypeNoun('flash', 1)).toBe('flash drive')
    expect(itemTypeNoun('flash', 2)).toBe('flash drives')
  })

  it('falls back to a generic noun for unknown types', () => {
    expect(itemTypeNoun('widget', 1)).toBe('item')
    expect(itemTypeNoun(null, 4)).toBe('items')
  })

  it('defaults to the singular when no count is given', () => {
    expect(itemTypeNoun('plugin')).toBe('plugin')
  })
})

describe('itemTypeCountLabel', () => {
  it('pairs the count with the right noun', () => {
    expect(itemTypeCountLabel(1, 'container')).toBe('1 container')
    expect(itemTypeCountLabel(3, 'containers')).toBe('3 containers')
    expect(itemTypeCountLabel(0, 'plugin')).toBe('0 plugins')
  })

  it('falls back to items for unknown types', () => {
    expect(itemTypeCountLabel(2, '')).toBe('2 items')
  })

  it('treats non-finite counts as zero', () => {
    expect(itemTypeCountLabel(NaN, 'container')).toBe('0 containers')
    expect(itemTypeCountLabel(undefined, 'vm')).toBe('0 VMs')
  })
})

describe('commonItemType', () => {
  it('returns the shared type of a homogeneous collection', () => {
    expect(commonItemType([{ item_type: 'container' }, { item_type: 'container' }])).toBe('container')
  })

  it('accepts the type alias field', () => {
    expect(commonItemType([{ type: 'plugins' }, { type: 'plugin' }])).toBe('plugin')
  })

  it('returns empty string for mixed collections', () => {
    expect(commonItemType([{ item_type: 'container' }, { item_type: 'folder' }])).toBe('')
  })

  it('returns empty string when any entry has an unknown type', () => {
    expect(commonItemType([{ item_type: 'container' }, { item_type: 'widget' }])).toBe('')
    expect(commonItemType([{ item_type: 'container' }, null])).toBe('')
  })

  it('returns empty string for empty or non-array input', () => {
    expect(commonItemType([])).toBe('')
    expect(commonItemType(null)).toBe('')
  })
})

describe('unchangedItemCount', () => {
  it('counts only the items flagged unchanged', () => {
    expect(unchangedItemCount([{ unchanged: true }, {}, { unchanged: true }])).toBe(2)
  })

  it('returns zero when a run captured content for every item', () => {
    expect(unchangedItemCount([{ status: 'ok' }, { status: 'ok' }])).toBe(0)
  })

  it('ignores truthy non-boolean flags and null entries', () => {
    expect(unchangedItemCount([{ unchanged: 'yes' }, null, { unchanged: 1 }])).toBe(0)
  })

  it('returns zero for empty or non-array input', () => {
    expect(unchangedItemCount([])).toBe(0)
    expect(unchangedItemCount(null)).toBe(0)
    expect(unchangedItemCount('not a run log')).toBe(0)
  })
})
