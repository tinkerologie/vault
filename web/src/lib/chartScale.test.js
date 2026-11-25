import { describe, expect, it } from 'vitest'
import { barSlot, niceTicks, xLabelIndexes } from './chartScale.js'

describe('niceTicks', () => {
  it('picks the first step giving at most five intervals and rounds the top up', () => {
    expect(niceTicks(23, [1, 2, 5, 10])).toEqual({ step: 5, ceil: 25, ticks: [0, 5, 10, 15, 20, 25] })
  })

  it('falls back to the largest step', () => {
    expect(niceTicks(1000, [1, 2])).toEqual({ step: 2, ceil: 1000, ticks: Array.from({ length: 501 }, (_, i) => i * 2) })
  })
})

describe('barSlot', () => {
  it('centres each bar in its slot at 70% width, in percent', () => {
    expect(barSlot(0, 4)).toEqual({ x: 3.75, width: 17.5, center: 12.5 })
    expect(barSlot(3, 4).center).toBe(87.5)
  })

  it('handles an empty series', () => {
    expect(barSlot(0, 0)).toEqual({ x: 0, width: 0, center: 50 })
  })
})

describe('xLabelIndexes', () => {
  it('labels the first, middle and last bucket', () => {
    expect(xLabelIndexes(7)).toEqual([0, 3, 6])
  })

  it('skips the middle label for two buckets', () => {
    expect(xLabelIndexes(2)).toEqual([0, 1])
  })

  it('labels nothing without data', () => {
    expect(xLabelIndexes(0)).toEqual([])
    expect(xLabelIndexes(1)).toEqual([0])
  })
})
