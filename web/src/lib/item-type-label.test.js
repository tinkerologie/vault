import { describe, expect, it } from 'vitest'
import { itemTypeSingularLabel, itemTypeLabel } from './utils.js'

describe('itemTypeSingularLabel (#705)', () => {
  it.each([
    ['container', 'Container'],
    ['containers', 'Container'],
    ['vm', 'VM'],
    ['folder', 'Folder'],
    ['flash', 'Flash drive'],
    ['plugin', 'Plugin'],
    ['zfs', 'ZFS dataset'],
    ['', 'Item'],
    ['mystery', 'Item'],
    [null, 'Item'],
  ])('%s is %s', (type, label) => {
    expect(itemTypeSingularLabel(type)).toBe(label)
  })

  it('leaves the plural filter labels unchanged', () => {
    expect(itemTypeLabel('container')).toBe('Containers')
  })
})
