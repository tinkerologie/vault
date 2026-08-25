import { describe, it, expect } from 'vitest'
import { atTop, nearOldestEdge, nearBottom } from './scrollflags.js'

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

describe('nearOldestEdge', () => {
  it('is near the oldest edge within the load-older band', () => {
    expect(nearOldestEdge(400, 500, 100)).toBe(true)   // 0 px to go
    expect(nearOldestEdge(201, 500, 100)).toBe(true)   // 199 px to go
  })

  it('is not near the oldest edge past the load-older band', () => {
    expect(nearOldestEdge(200, 500, 100)).toBe(false)  // 200 px to go
    expect(nearOldestEdge(0, 500, 100)).toBe(false)    // 400 px to go
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
