import { describe, it, expect } from 'vitest'
import { isRestoreActive, shouldClearOnCompleted, shouldClearOnSnapshot } from './restore-sync.js'

describe('isRestoreActive', () => {
  it('returns true when active, run_type is restore, and job matches', () => {
    expect(isRestoreActive({ active: true, run_type: 'restore', job_id: 12 }, 12)).toBe(true)
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
})
