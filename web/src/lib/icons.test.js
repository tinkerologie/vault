import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { ICONS, PAGE_ICONS, ITEM_TYPE_ICONS, STORAGE_TYPE_ICONS, STORAGE_TYPE_COLORS, storageTypeIcon } from './icons.js'
import { ITEM_TYPE_ICONS as UTILS_ITEM_ICONS, itemTypeIcon } from './utils.js'

const SRC = new URL('..', import.meta.url).pathname
const read = (p) => readFileSync(SRC + p, 'utf8')

describe('icon module (#698)', () => {
  it('gives every icon a single path', () => {
    for (const map of [ICONS, PAGE_ICONS, ITEM_TYPE_ICONS, STORAGE_TYPE_ICONS]) {
      for (const [name, d] of Object.entries(map)) expect(d, name).toMatch(/^M[\d.]/)
    }
  })

  it('gives each page its own icon', () => {
    const paths = Object.values(PAGE_ICONS)
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('is where the shared item type icons come from', () => {
    expect(UTILS_ITEM_ICONS).toBe(ITEM_TYPE_ICONS)
    expect(itemTypeIcon('containers')).toBe(ITEM_TYPE_ICONS.container)
  })

  it('drives the sidebar and the palette from the same page icons', () => {
    const app = read('App.svelte')
    for (const path of Object.keys(PAGE_ICONS)) expect(app).toContain(`icon: PAGE_ICONS['${path}']`)
    // The palette takes its page commands, icons included, from the shell (#691).
    expect(read('components/CommandPalette.svelte')).toMatch(/icon: page\.icon/)
  })

  it('does not reuse a page icon for an unrelated palette command', () => {
    const palette = read('components/CommandPalette.svelte')
    expect(palette).not.toMatch(/icon: ICONS\.shield/)
    expect(palette).not.toMatch(/icon: ICONS\.clock/)
  })
})

describe('storage type icons (#710)', () => {
  it('gives every storage type a distinct icon and a colour', () => {
    const types = ['local', 'sftp', 'smb', 'nfs', 'webdav', 's3']
    expect(Object.keys(STORAGE_TYPE_ICONS).sort()).toEqual([...types].sort())
    expect(new Set(types.map((t) => STORAGE_TYPE_ICONS[t])).size).toBe(types.length)
    for (const t of types) expect(STORAGE_TYPE_COLORS[t]).toMatch(/^text-hue-/)
  })

  it('does not draw Local Path as a cloud or S3 and NFS as plain folders', () => {
    expect(STORAGE_TYPE_ICONS.local).not.toBe(STORAGE_TYPE_ICONS.webdav)
    expect(STORAGE_TYPE_ICONS.s3).not.toBe(ICONS.folder)
    expect(STORAGE_TYPE_ICONS.nfs).not.toBe(ICONS.folder)
    expect(STORAGE_TYPE_ICONS.sftp).toBe(ICONS.terminal)
  })

  it('falls back to the Local Path icon', () => {
    expect(storageTypeIcon('s3')).toBe(STORAGE_TYPE_ICONS.s3)
    expect(storageTypeIcon('ftp')).toBe(STORAGE_TYPE_ICONS.local)
    expect(storageTypeIcon(undefined)).toBe(STORAGE_TYPE_ICONS.local)
  })

  it('is the one map the storage page and form use', () => {
    for (const file of ['pages/Storage.svelte', 'components/StorageForm.svelte']) {
      const text = read(file)
      expect(text, file).toContain('const storageIcons = STORAGE_TYPE_ICONS')
      expect(text, file).toContain('const storageColors = STORAGE_TYPE_COLORS')
    }
  })
})
