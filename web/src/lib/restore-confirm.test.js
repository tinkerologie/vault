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

describe('restoreTargetFor with server write targets (#716)', () => {
  const plexPreview = {
    name: 'plex', type: 'container', status: 'resolved', write_targets: [
      { kind: 'volume', path: '/mnt/user/appdata/plex', mount: '/config', clearable: true },
      { kind: 'volume', path: '/mnt/user', mount: '/media' },
      { kind: 'file', path: '/mnt/user/appdata/plex/hook.sh', mount: '/hook.sh' },
      { kind: 'template', path: '/boot/config/plugins/dockerMan/templates-user/my-plex.xml' },
    ],
  }

  it('lists every exact host path the restore writes, from the server', () => {
    expect(restoreTargetFor({ type: 'container', name: 'plex' }, '', plexPreview)).toEqual({
      path: null,
      description: 'Exact host paths from the saved configuration',
      paths: [
        { path: '/mnt/user/appdata/plex', label: 'Volume /config', effect: 'clearable' },
        { path: '/mnt/user', label: 'Volume /media', effect: 'merge' },
        { path: '/mnt/user/appdata/plex/hook.sh', label: 'File /hook.sh', effect: 'overwrite' },
        { path: '/boot/config/plugins/dockerMan/templates-user/my-plex.xml', label: 'Unraid template', effect: 'overwrite' },
      ],
    })
  })

  it('prefers the server paths over the custom destination they were remapped under', () => {
    const preview = { name: 'win', type: 'vm', status: 'resolved', write_targets: [
      { kind: 'disk', path: '/mnt/user/restore/vdisk1.img', mount: 'vda' },
      { kind: 'nvram', path: '/mnt/user/restore/win_VARS.fd' },
      { kind: 'domain', name: 'win' },
    ] }
    const target = restoreTargetFor({ type: 'vm', name: 'win' }, '/mnt/user/restore', preview)
    expect(target.paths).toEqual([
      { path: '/mnt/user/restore/vdisk1.img', label: 'Disk vda', effect: 'overwrite' },
      { path: '/mnt/user/restore/win_VARS.fd', label: 'NVRAM', effect: 'overwrite' },
    ])
    expect(target.domain).toBe('win')
  })

  it('keeps the description, with why, when the server could not resolve the paths', () => {
    const preview = { name: 'win', type: 'vm', status: 'unavailable', reason: 'vm_host_unavailable', write_targets: [] }
    expect(restoreTargetFor({ type: 'vm', name: 'win' }, '', preview)).toEqual({
      path: null,
      description: 'The original disk image, domain XML and NVRAM paths, read from the backup',
      unresolved: 'Exact paths could not be checked: the VM host is unavailable.',
    })
    const locked = { status: 'unavailable', reason: 'backup_metadata_locked', write_targets: [] }
    expect(restoreTargetFor({ type: 'container', name: 'plex' }, '', locked).unresolved)
      .toBe('Enter the encryption passphrase to see the exact paths.')
    const other = { status: 'unavailable', reason: 'chain_unconfirmed', write_targets: [] }
    expect(restoreTargetFor({ type: 'container', name: 'plex' }, '', other).unresolved)
      .toBe('Exact paths could not be checked before the restore.')
    expect(restoreTargetFor({ type: 'container', name: 'plex' }, '/mnt/user/restore', other)).toMatchObject({
      path: '/mnt/user/restore', description: 'Custom destination',
    })
  })

  it('ignores a preview for folders, whose path the wizard already knows', () => {
    const item = { type: 'folder', name: 'media', settings: '{"path":"/mnt/user/media"}' }
    expect(restoreTargetFor(item, '', plexPreview)).toEqual({ path: '/mnt/user/media', description: 'Original location' })
  })
})

describe('clearingRestoreSummary (#700)', () => {
  it('lists each exact container and VM path in the typed confirmation (#716)', () => {
    const { source } = clearingRestoreSummary([
      { jobName: 'Docker', pointDate: 'Oct 1', targets: [
        { name: 'plex', typeLabel: 'Container', path: null, description: 'Exact host paths', paths: [
          { path: '/mnt/user/appdata/plex', label: 'Volume /config', effect: 'clearable' },
          { path: '/boot/config/plugins/dockerMan/templates-user/my-plex.xml', label: 'Unraid template', effect: 'overwrite' },
        ] },
        { name: 'win', typeLabel: 'VM', path: null, description: 'Exact host paths', domain: 'win', paths: [
          { path: '/mnt/user/domains/win/vdisk1.img', label: 'Disk vda', effect: 'overwrite' },
        ] },
      ] },
    ])
    expect(source).toEqual([
      { label: 'plex (Container), Docker, Oct 1: Volume /config', value: '/mnt/user/appdata/plex' },
      { label: 'plex (Container), Docker, Oct 1: Unraid template', value: '/boot/config/plugins/dockerMan/templates-user/my-plex.xml' },
      { label: 'win (VM), Docker, Oct 1: Disk vda', value: '/mnt/user/domains/win/vdisk1.img' },
      { label: 'win (VM), Docker, Oct 1: Domain definition', value: 'win' },
    ])
  })

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
