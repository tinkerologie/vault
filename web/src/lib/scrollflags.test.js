import { describe, it, expect } from 'vitest'
import { atTop, nearTop, nearBottom } from './scrollflags.js'

describe('atTop', () => {
  it('is at top within the edge band', () => {
    expect(atTop(0)).toBe(true)
    expect(atTop(47)).toBe(true)
  })

  it('is not at top past the edge band', () => {
    expect(atTop(48)).toBe(false)
    expect(atTop(200)).toBe(false)
  })
})

describe('nearTop', () => {
  it('is near top within the load-older band', () => {
    expect(nearTop(0)).toBe(true)
    expect(nearTop(199)).toBe(true)
  })

  it('is not near top past the load-older band', () => {
    expect(nearTop(200)).toBe(false)
    expect(nearTop(500)).toBe(false)
  })
})

describe('nearBottom', () => {
  it('is near bottom within the edge band', () => {
    expect(nearBottom(400, 500, 100)).toBe(true) // 500-400-100 = 0
    expect(nearBottom(360, 500, 100)).toBe(true) // 500-360-100 = 40 < 48
  })

  it('is not near bottom past the edge band', () => {
    expect(nearBottom(0, 500, 100)).toBe(false)   // 400
    expect(nearBottom(350, 500, 100)).toBe(false) // 50
  })
})
