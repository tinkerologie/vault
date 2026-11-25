import { describe, expect, it } from 'vitest'
import { deleteRestorePointSummary, restoreTargetFor, clearingRestoreSummary } from './restore-confirm.js'

describe('restoreTargetFor (#700)', () => {
  it.each([
    [{ type: 'folder', name: 'media', settings: '{"path":"/mnt/user/media"}' }, '', '/mnt/user/media'],
    [{ type: 'flash', name: 'Flash Drive', settings: { path: '/boot', preset: 'flash' } }, '', '/boot'],
    [{ type: 'plugin', name: 'unassigned.devices' }, '', '/boot/config/plugins/unassigned.devices'],
    [{ type: 'zfs', name: 'data', settings: { dataset: 'tank/data' } }, '', 'tank/data'],
    [{ type: 'zfs', name: 'data' }, '', 'data'],
    [{ type: 'container', name: 'plex' }, ' /mnt/user/restore ', '/mnt/user/restore'],
  ])('%j → %s', (item, custom, path) => {
    expect(restoreTargetFor(item, custom).path).toBe(path)
  })

  it('says where an unrecorded original path comes from instead of guessing', () => {
    expect(restoreTargetFor({ type: 'container', name: 'plex', settings: '{}' })).toEqual({
      path: null, description: "Each volume's original host path, read from the container's saved configuration",
    })
    expect(restoreTargetFor({ type: 'vm', name: 'win' }).path).toBeNull()
    expect(restoreTargetFor({ type: 'folder', name: 'x', settings: '{}' }).path).toBeNull()
  })
})

describe('clearingRestoreSummary (#700)', () => {
  it('lists every affected location and states the deletion plainly', () => {
    const { source, consequences } = clearingRestoreSummary([
      { jobName: 'Docker', pointDate: 'Oct 1', targets: [
        { name: 'plex', typeLabel: 'Container', path: null, description: 'Original volumes' },
        { name: 'media', typeLabel: 'Folder', path: '/mnt/user/media', description: '' },
      ] },
    ])
    expect(source).toEqual([
      { label: 'plex (Container), Docker, Oct 1', value: 'Original volumes' },
      { label: 'media (Folder), Docker, Oct 1', value: '/mnt/user/media' },
    ])
    expect(consequences[0]).toBe('Files at these locations that are not in the backup are deleted.')
    expect(consequences).toContain('Files that are in the backup are overwritten with the backed-up copy.')
  })
})

describe('deleteRestorePointSummary', () => {
  it('names the date, job and backup type', () => {
    const { source, consequences } = deleteRestorePointSummary(
      { jobName: 'Docker & Appdata', backup_type: 'full' }, 'Oct 1, 2026, 02:00')
    expect(source).toEqual([
      { label: 'Job', value: 'Docker & Appdata' },
      { label: 'Restore point', value: 'Oct 1, 2026, 02:00 (full)' },
    ])
    expect(consequences).toContain('Its backup files are deleted from storage.')
    expect(consequences).toContain('This cannot be undone.')
  })

  it('warns when newer restore points depend on it', () => {
    const { consequences } = deleteRestorePointSummary(
      { jobId: 3, backup_type: 'full', retention_preserved_for: 2 }, 'Oct 1')
    expect(consequences[0]).toBe('2 newer restore points depend on it and can no longer be restored.')
  })

  it('falls back to the job number when the name is unknown', () => {
    expect(deleteRestorePointSummary({ jobId: 3 }, 'Oct 1').source[0].value).toBe('Job #3')
  })
})
