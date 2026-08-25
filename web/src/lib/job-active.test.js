import { describe, it, expect } from 'vitest'
import { isJobRunningOrQueued } from './job-active.js'

describe('isJobRunningOrQueued', () => {
  const cases = [
    {
      name: 'shows active only while the run is actually executing',
      progress: { running: true, activeRun: { job_id: 5 }, queue: [] },
      jobId: 5,
      expected: true,
    },
    {
      name: 'clears the instant running goes false, even if activeRun lingers',
      // The regression: after job_run_completed, activeRun sticks around for
      // the Dashboard overlay's 5s grace, but the row must stop showing Cancel now.
      progress: { running: false, activeRun: { job_id: 5 }, queue: [] },
      jobId: 5,
      expected: false,
    },
    {
      name: 'still shows Cancel for a queued job',
      progress: { running: false, activeRun: null, queue: [{ job_id: 7 }] },
      jobId: 7,
      expected: true,
    },
    {
      name: 'ignores a different active run',
      progress: { running: true, activeRun: { job_id: 5 }, queue: [] },
      jobId: 9,
      expected: false,
    },
    {
      name: 'handles a missing queue',
      progress: { running: false, activeRun: null, queue: undefined },
      jobId: 1,
      expected: false,
    },
  ]

  it.each(cases)('$name', ({ progress, jobId, expected }) => {
    expect(isJobRunningOrQueued(progress, jobId)).toBe(expected)
  })
})
