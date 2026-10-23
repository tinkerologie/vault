import { describe, expect, it } from 'vitest'
import { cleanAbsolutePath, localStoragePathProblem } from './storagePath.js'

describe('localStoragePathProblem (#514)', () => {
  it.each([
    ['', 'Choose'],
    ['   ', 'Choose'],
    ['.', 'absolute'],
    ['backups', 'absolute'],
    ['/', 'protected'],
    ['/usr/local/emhttp', 'protected'],
    ['/usr/local/emhttp/plugins/', 'protected'],
    ['/boot', 'protected'],
    ['/boot/config', 'protected'],
    ['/etc', 'protected'],
    ['/var', 'protected'],
    ['/mnt/user/../../usr', 'protected'],
    ['/mnt', 'shared parent'],
    ['/mnt/user', 'shared parent'],
    ['/mnt/user/', 'shared parent'],
    ['/mnt/disks', 'shared parent'],
  ])('rejects %j', (path, want) => {
    expect(localStoragePathProblem(path)).toContain(want)
  })

  it.each([
    '/mnt/user/Documents/appdata-backup',
    '/mnt/user/backups/vault/',
    '/mnt/cache/vault',
    '/mnt/backup_pool',
    '/mnt/disks/usb1/vault',
  ])('accepts %j', (path) => {
    expect(localStoragePathProblem(path)).toBe('')
  })

  it('cleans paths lexically', () => {
    expect(cleanAbsolutePath('/a//b/./c/../d/')).toBe('/a/b/d')
    expect(cleanAbsolutePath('/..')).toBe('/')
  })
})
