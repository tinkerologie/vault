import { describe, expect, it } from 'vitest'
import { errorHint } from './errorHint.js'

describe('errorHint', () => {
  it('explains the Docker socket case and names the path to exclude', () => {
    const hint = errorHint('backup chunked: backup volume /var/run/docker.sock: folder: open source root: open /run/docker.sock: no such device or address')
    expect(hint?.kind).toBe('special-file')
    expect(hint?.cause).toBe("The Docker socket can't be backed up.")
    expect(hint?.action).toContain('/var/run/docker.sock')
    expect(hint?.excludePath).toBe('/var/run/docker.sock')
  })

  it('treats other sockets and special files the same way', () => {
    const hint = errorHint('backup volume /mnt/user/appdata/app/run.sock: open /mnt/user/appdata/app/run.sock: no such device or address')
    expect(hint?.kind).toBe('special-file')
    expect(hint?.cause).toBe("A socket or special file can't be backed up.")
    expect(hint?.excludePath).toBe('/mnt/user/appdata/app/run.sock')
  })

  it('maps permission denied', () => {
    const hint = errorHint('open /mnt/user/appdata/plex/db: permission denied')
    expect(hint?.kind).toBe('permission')
    expect(hint?.excludePath).toBeUndefined()
  })

  it('maps a full disk', () => {
    expect(errorHint('write /mnt/backup/x.tar: no space left on device')?.kind).toBe('no-space')
  })

  it.each([
    'dial tcp 10.0.0.2:22: connect: connection refused',
    'dial tcp 10.0.0.2:445: connect: no route to host',
    'read tcp 10.0.0.2:443: i/o timeout',
    'dial tcp: lookup nas: host is unreachable',
  ])('maps an unreachable destination: %s', (raw) => {
    expect(errorHint(raw)?.kind).toBe('unreachable')
  })

  it('maps a missing item', () => {
    expect(errorHint('stat /mnt/user/old: no such file or directory')?.kind).toBe('missing')
    expect(errorHint('container "web" not found')?.kind).toBe('missing')
  })

  it('returns null for errors it does not know, so the raw text shows', () => {
    expect(errorHint('checksum mismatch in chunk 42')).toBeNull()
    expect(errorHint('')).toBeNull()
    expect(errorHint(undefined)).toBeNull()
  })
})
