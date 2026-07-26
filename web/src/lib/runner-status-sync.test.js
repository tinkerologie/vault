import { describe, it, expect } from 'vitest'
import { reconcileRunnerStatus, trackRunState } from './runner-status-sync.js'

const types = (msgs) => msgs.map((m) => m.type)

describe('reconcileRunnerStatus', () => {
  it('synthesises job_run_completed when a known-active run has finished', () => {
    // The case that matters after a hub eviction: the terminal event was the
    // message the hub discarded, and the progress UI only clears on it.
    const previous = { active: true, job_id: 15, run_id: 37, run_type: 'backup' }
    const msgs = reconcileRunnerStatus(previous, { active: false })

    expect(types(msgs)).toContain('job_run_completed')
    const done = msgs.find((m) => m.type === 'job_run_completed')
    expect(done).toMatchObject({ job_id: 15, run_id: 37, run_type: 'backup' })
  })

  it('synthesises job_run_started when a run appeared while disconnected', () => {
    // Baseline says nothing was running, so this is a genuine transition —
    // unlike a null baseline, where we are only discovering current state.
    const msgs = reconcileRunnerStatus({ active: false }, {
      active: true,
      job_id: 5,
      run_id: 9,
      job_name: 'Nightly',
      run_type: 'backup',
      items_total: 3,
    })

    expect(types(msgs)).toContain('job_run_started')
    expect(msgs.find((m) => m.type === 'job_run_started')).toMatchObject({
      job_id: 5,
      run_id: 9,
      items_total: 3,
    })
  })

  it('ends with the snapshot so it is authoritative', () => {
    // A synthetic job_run_started zeroes the progress store's counters and
    // elapsed timer; the snapshot must land after it to repopulate them.
    const msgs = reconcileRunnerStatus({ active: false }, { active: true, run_id: 1 })
    expect(msgs[msgs.length - 1].type).toBe('runner_status_snapshot')
    expect(types(msgs).indexOf('job_run_started')).toBeLessThan(
      types(msgs).indexOf('runner_status_snapshot'),
    )
  })

  it('emits completion then start when one run replaced another', () => {
    // Run A finished and run B started while disconnected. A boolean-only
    // check sees active on both sides and emits nothing, leaving consumers
    // showing A for the whole of B.
    const previous = { active: true, job_id: 1, run_id: 10, run_type: 'backup' }
    const snapshot = { active: true, job_id: 2, run_id: 11, run_type: 'backup' }
    const msgs = reconcileRunnerStatus(previous, snapshot)

    expect(types(msgs)).toContain('job_run_completed')
    expect(types(msgs)).toContain('job_run_started')
    expect(msgs.find((m) => m.type === 'job_run_completed')).toMatchObject({ run_id: 10 })
    expect(msgs.find((m) => m.type === 'job_run_started')).toMatchObject({ run_id: 11 })
    // Completion must precede the start so the store closes A before opening B.
    expect(types(msgs).indexOf('job_run_completed')).toBeLessThan(
      types(msgs).indexOf('job_run_started'),
    )
  })

  it('treats a missing run id as a new run, not the same one', () => {
    // undefined === undefined would make consecutive runs indistinguishable
    // and emit no transition at all.
    const msgs = reconcileRunnerStatus({ active: true }, { active: true })
    expect(types(msgs)).toContain('job_run_completed')
    expect(types(msgs)).toContain('job_run_started')
  })

  it('stays quiet when the same run is still going', () => {
    const same = { active: true, job_id: 1, run_id: 10 }
    const msgs = reconcileRunnerStatus(same, { ...same })
    expect(types(msgs)).toEqual(['runner_status_snapshot'])
  })

  it('does not synthesise a start with no baseline to transition from', () => {
    // First sync of a page opened mid-run: restoreFromStatus populates real
    // start time and counters from the snapshot, and a synthetic start would
    // immediately reset them to zero.
    const msgs = reconcileRunnerStatus(null, { active: true, run_id: 7 })
    expect(types(msgs)).not.toContain('job_run_started')
    expect(types(msgs)).toContain('runner_status_snapshot')
  })

  it('emits queue_update only when the queue actually changed', () => {
    const queue = [{ job_id: 1, job_name: 'a', queued_at: 't' }]
    expect(types(reconcileRunnerStatus({ queue }, { queue }))).not.toContain(
      'queue_update',
    )
    expect(types(reconcileRunnerStatus({ queue: [] }, { queue }))).toContain(
      'queue_update',
    )
  })

  it('does not invent a completion when nothing was running', () => {
    const msgs = reconcileRunnerStatus({ active: false }, { active: false })
    expect(types(msgs)).not.toContain('job_run_completed')
    expect(types(msgs)).not.toContain('job_run_started')
  })
})

describe('trackRunState', () => {
  it('records an active run so a later reconnect can detect its completion', () => {
    // Without this the baseline is only maintained by the polling path, and a
    // reconnect mid-run would have nothing to compare the snapshot against.
    const next = trackRunState(null, {
      type: 'job_run_started',
      job_id: 15,
      run_id: 37,
      run_type: 'backup',
    })
    expect(next).toMatchObject({ active: true, job_id: 15, run_id: 37 })

    const msgs = reconcileRunnerStatus(next, { active: false })
    expect(types(msgs)).toContain('job_run_completed')
  })

  it('clears the active run on completion', () => {
    const running = { active: true, job_id: 1, run_id: 2 }
    expect(trackRunState(running, { type: 'job_run_completed' })).toMatchObject({
      active: false,
    })
  })

  it('tracks the queue so a reconnect does not re-emit an unchanged one', () => {
    const queue = [{ job_id: 3, job_name: 'c', queued_at: 't' }]
    const next = trackRunState(null, { type: 'queue_update', queue })
    expect(types(reconcileRunnerStatus(next, { queue }))).not.toContain(
      'queue_update',
    )
  })

  it('passes through unrelated messages without disturbing the baseline', () => {
    const running = { active: true, job_id: 1, run_id: 2 }
    expect(trackRunState(running, { type: 'backup_progress', percent: 40 })).toBe(
      running,
    )
    expect(trackRunState(running, undefined)).toBe(running)
  })
})
