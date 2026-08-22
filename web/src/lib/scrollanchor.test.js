import { describe, it, expect } from 'vitest'
import { anchoredScrollTop } from './scrollanchor.js'

describe('anchoredScrollTop', () => {
  it('keeps scrollTop unchanged when the element does not move', () => {
    expect(anchoredScrollTop(500, 0, 0)).toBe(500)
  })

  it('adds the element shift when content above grows (prepend)', () => {
    // Element moves from 0 to 120px below the container top after a prepend:
    // to keep it visually pinned, scrollTop must increase by 120.
    expect(anchoredScrollTop(500, 0, 120)).toBe(620)
  })

  it('subtracts the element shift when content above shrinks', () => {
    expect(anchoredScrollTop(300, 150, 40)).toBe(190)
  })

  it('handles a negative net shift', () => {
    expect(anchoredScrollTop(80, 200, 0)).toBe(-120)
  })
})
