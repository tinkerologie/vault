import { describe, expect, it } from 'vitest'
import { tabFromKey } from './tabs.js'

const ids = ['a', 'b', 'c']

describe('tabFromKey', () => {
  it('moves with the arrows and wraps at both ends', () => {
    expect(tabFromKey({ key: 'ArrowRight' }, ids, 'a')).toBe('b')
    expect(tabFromKey({ key: 'ArrowRight' }, ids, 'c')).toBe('a')
    expect(tabFromKey({ key: 'ArrowLeft' }, ids, 'b')).toBe('a')
    expect(tabFromKey({ key: 'ArrowLeft' }, ids, 'a')).toBe('c')
  })

  it('jumps with Home and End', () => {
    expect(tabFromKey({ key: 'Home' }, ids, 'c')).toBe('a')
    expect(tabFromKey({ key: 'End' }, ids, 'a')).toBe('c')
  })

  it('ignores other keys and modified keys', () => {
    expect(tabFromKey({ key: 'Enter' }, ids, 'a')).toBeNull()
    expect(tabFromKey({ key: 'ArrowDown' }, ids, 'a')).toBeNull()
    for (const mod of ['altKey', 'ctrlKey', 'metaKey', 'shiftKey']) {
      expect(tabFromKey({ key: 'ArrowRight', [mod]: true }, ids, 'a')).toBeNull()
    }
  })
})
