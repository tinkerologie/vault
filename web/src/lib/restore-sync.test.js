import { describe, it, expect, vi } from 'vitest'
import { hasTerminalOutcome, isRestoreActive, matchesJobId, outcomeFromCompletion, outcomeFromRun, shouldClearOnCompleted, shouldClearOnSnapshot, waitForRestoreOutcome } from './restore-sync.js'

describe('restore outcomes', () => {
  const run = { id: 7, job_id: 3, run_type: 'restore', items_done: 1, items_total: 2, items_failed: 1, size_bytes: 35 }
  const expected = { runId: '7', jobId: 3 }

  it.each(['completed', 'partial', 'failed', 'cancelled'])('accepts the terminal %s status', (status) => {
    expect(hasTerminalOutcome({ status })).toBe(true)
    expect(outcomeFromRun({ ...run, status }, expected)).toEqual({ status, done: 1, total: 2, failed: 1, bytes: 35 })
  })

  it.each([undefined, null, 'unknown', 'running', 'queued'])('waits on %s', (status) => {
    expect(hasTerminalOutcome({ status })).toBe(false)
    expect(outcomeFromRun({ ...run, status }, expected)).toBeNull()
  })

  it.each([{ id: 8 }, { job_id: 4 }, { run_type: 'backup' }])('discard a different run %j', (changes) => {
    expect(outcomeFromRun({ ...run, ...changes, status: 'completed' }, expected)).toBeNull()
  })

  it('accepts a terminal WebSocket message with no counters', () => {
    expect(outcomeFromCompletion({ run_type: 'restore', status: 'failed' }, 2))
      .toEqual({ status: 'failed', done: 0, total: 2, failed: 2, bytes: 0 })
  })

  it('preserves explicit zero counts in a terminal result', () => {
    const result = { ...run, status: 'failed', items_total: 0, items_done: 0, items_failed: 0 }
    expect(outcomeFromCompletion(result, 2)).toEqual({ status: 'failed', done: 0, total: 0, failed: 0, bytes: 35 })
  })

  it('backs off on API errors and nonterminal outcomes before returning the recorded result', async () => {
    vi.useFakeTimers()
    try {
      const getRun = vi.fn()
        .mockRejectedValueOnce(new Error('Unavailable'))
        .mockResolvedValueOnce({ ...run, status: 'running' })
        .mockResolvedValue({ ...run, status: 'partial' })
      const result = waitForRestoreOutcome(getRun, expected, () => true)

      await vi.advanceTimersByTimeAsync(999)
      expect(getRun).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(2000)
      expect(getRun).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(1)
      expect(getRun).toHaveBeenCalledTimes(3)
      expect(getRun).toHaveBeenLastCalledWith(expected.runId)
      await expect(result).resolves.toEqual({ status: 'partial', done: 1, total: 2, failed: 1, bytes: 35 })
    } finally {
      vi.useRealTimers()
    }
  })

  it('caps the retry delay and stops requesting when the run is superseded', async () => {
    vi.useFakeTimers()
    try {
      let current = true
      const getRun = vi.fn().mockResolvedValue({ ...run, id: 8, status: 'completed' })
      const result = waitForRestoreOutcome(getRun, expected, () => current)

      await vi.advanceTimersByTimeAsync(22999)
      expect(getRun).toHaveBeenCalledTimes(5)
      await vi.advanceTimersByTimeAsync(1)
      expect(getRun).toHaveBeenCalledTimes(6)
      current = false
      await vi.advanceTimersByTimeAsync(8000)

      await expect(result).resolves.toBeNull()
      expect(getRun).toHaveBeenCalledTimes(6)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('matchesJobId', () => {
  it('returns true when filter is null or undefined', () => {
    expect(matchesJobId(12, null)).toBe(true)
    expect(matchesJobId(12, undefined)).toBe(true)
  })

  it('returns false when targetJobId is null or undefined but filter is set', () => {
    expect(matchesJobId(null, 12)).toBe(false)
    expect(matchesJobId(undefined, 12)).toBe(false)
  })

  it('matches single number or string', () => {
    expect(matchesJobId(12, 12)).toBe(true)
    expect(matchesJobId('12', 12)).toBe(true)
    expect(matchesJobId(12, '12')).toBe(true)
    expect(matchesJobId(12, 13)).toBe(false)
  })

  it('matches Set or Map of job IDs', () => {
    const set = new Set([5, 10, '15'])
    expect(matchesJobId(5, set)).toBe(true)
    expect(matchesJobId('10', set)).toBe(true)
    expect(matchesJobId(15, set)).toBe(true)
    expect(matchesJobId(99, set)).toBe(false)
  })

  it('matches Array of job IDs', () => {
    const arr = [5, 10, '15']
    expect(matchesJobId(5, arr)).toBe(true)
    expect(matchesJobId('10', arr)).toBe(true)
    expect(matchesJobId(15, arr)).toBe(true)
    expect(matchesJobId(99, arr)).toBe(false)
  })

  it('returns false for unsupported filter types', () => {
    expect(matchesJobId(12, true)).toBe(false)
    expect(matchesJobId(12, {})).toBe(false)
  })
})

describe('isRestoreActive', () => {
  it('returns true when active, run_type is restore, and job matches', () => {
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 12 }, 12)).toBe(true)
  })

  it('returns true when active, run_type is restore, and job is in set of job IDs', () => {
    const jobs = new Set([12, 14])
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 12 }, jobs)).toBe(true)
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 14 }, jobs)).toBe(true)
  })

  it('returns false when job is not in set of job IDs (unrelated job)', () => {
    const jobs = new Set([12, 14])
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 99 }, jobs)).toBe(false)
  })

  it('returns true when active, run_type is restore, and restoringJobId is not set', () => {
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 12 }, null)).toBe(true)
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 12 }, undefined)).toBe(true)
  })

  it('returns false when status is null or undefined', () => {
    expect(isRestoreActive(null, 12)).toBe(false)
    expect(isRestoreActive(undefined, 12)).toBe(false)
  })

  it('returns false when active is false', () => {
    expect(isRestoreActive({ active: false, run_type: 'restore', job_id: 12 }, 12)).toBe(false)
  })

  it('returns false when run_type is not restore', () => {
    expect(isRestoreActive({ active: true, run_type: 'backup', job_id: 12 }, 12)).toBe(false)
  })

  it('returns false when job_id does not match restoringJobId', () => {
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 99 }, 12)).toBe(false)
  })
})

describe('shouldClearOnCompleted', () => {
  it('returns true when run_type is restore and job matches', () => {
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 5 }, 5)).toBe(true)
  })

  it('returns true when run_type is restore and job is in set of job IDs', () => {
    const jobs = new Set([5, 6])
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 5 }, jobs)).toBe(true)
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 6 }, jobs)).toBe(true)
  })

  it('returns false when job is an unrelated job not in set of job IDs', () => {
    const jobs = new Set([5, 6])
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 99 }, jobs)).toBe(false)
  })

  it('returns true when run_type is restore and restoringJobId is null or undefined', () => {
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 5 }, null)).toBe(true)
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 5 }, undefined)).toBe(true)
  })

  it('returns false when run_type is not restore', () => {
    expect(shouldClearOnCompleted({ run_type: 'backup', job_id: 5 }, 5)).toBe(false)
    expect(shouldClearOnCompleted({ run_type: 'verify', job_id: 5 }, 5)).toBe(false)
    expect(shouldClearOnCompleted({}, 5)).toBe(false)
    expect(shouldClearOnCompleted(null, 5)).toBe(false)
  })

  it('returns false when job_id does not match restoringJobId', () => {
    expect(shouldClearOnCompleted({ run_type: 'restore', job_id: 10 }, 5)).toBe(false)
  })
})

describe('shouldClearOnSnapshot', () => {
  it('returns true when status is missing or falsy', () => {
    expect(shouldClearOnSnapshot(null, 5)).toBe(true)
    expect(shouldClearOnSnapshot(undefined, 5)).toBe(true)
  })

  it('returns true when status active is false', () => {
    expect(shouldClearOnSnapshot({ active: false, run_type: 'restore', job_id: 5 }, 5)).toBe(true)
  })

  it('returns true when status run_type is not restore', () => {
    expect(shouldClearOnSnapshot({ active: true, run_type: 'backup', job_id: 5 }, 5)).toBe(true)
  })

  it('returns true when restoringJobId is set and status job_id does not match', () => {
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 88 }, 5)).toBe(true)
  })

  it('returns false when restore is actively running for matching job', () => {
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 5 }, 5)).toBe(false)
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 5 }, null)).toBe(false)
  })

  it('returns false when status job_id is one of multiple active jobs in set', () => {
    const jobs = new Set([5, 6])
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 5 }, jobs)).toBe(false)
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 6 }, jobs)).toBe(false)
    expect(shouldClearOnSnapshot({ active: true, run_type: 'restore', job_id: 99 }, jobs)).toBe(true)
  })
})
