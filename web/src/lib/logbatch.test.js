import { describe, it, expect } from 'vitest'
import { planBatch } from './logbatch.js'

describe('planBatch', () => {
  it('consumes the whole page in full when it fits the budget', () => {
    expect(planBatch([1, 1, 1, 1], 150)).toEqual({ fullCount: 4, split: 0 })
  })

  it('splits a mid-page terminal entry that overflows the budget', () => {
    // 40+40+40 = 120 consumed; the fourth 40-line entry overflows → split 30.
    expect(planBatch([40, 40, 40, 40, 40], 150)).toEqual({ fullCount: 3, split: 30 })
  })

  it('splits an oversized leading entry instead of blowing the budget', () => {
    expect(planBatch([200], 150)).toEqual({ fullCount: 0, split: 150 })
  })

  it('never defers plain activity rows (size 0) behind an oversized terminal', () => {
    // 166 run-log lines blow the budget alone; the zero-size (plain) entries
    // after it are always fully consumed — the caller materializes their rows
    // and defers only the terminal's run-log lines.
    expect(planBatch([166, 0, 0, 0], 150)).toEqual({ fullCount: 0, split: 150 })
  })

  it('splits the entry that overflows a partially-filled budget', () => {
    expect(planBatch([40, 40, 100], 150)).toEqual({ fullCount: 2, split: 70 })
  })

  it('treats a batch exactly at the budget as fully consumed (no split)', () => {
    expect(planBatch([75, 75, 1], 150)).toEqual({ fullCount: 2, split: 0 })
  })

  it('handles an empty page', () => {
    expect(planBatch([], 150)).toEqual({ fullCount: 0, split: 0 })
  })
})
