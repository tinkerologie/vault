import { describe, expect, it } from 'vitest'
import { progressAnnouncement } from './progressAnnouncement.js'

const RUN = { run_id: 7, job_name: 'Nightly', run_type: 'backup' }

describe('progressAnnouncement', () => {
  it('says nothing while nothing runs', () => {
    expect(progressAnnouncement({ activeRun: null, running: false })).toBeNull()
  })

  it('announces the start', () => {
    expect(progressAnnouncement({ activeRun: RUN, running: true })).toEqual({ key: '7|started', text: 'Backup of Nightly started' })
  })

  it('keys a phase by its kind, so per-item staging stays quiet', () => {
    const a = progressAnnouncement({ activeRun: RUN, running: true, phaseMessage: 'Staging plex locally...' })
    const b = progressAnnouncement({ activeRun: RUN, running: true, phaseMessage: 'Staging sonarr locally...' })
    expect(a).toEqual({ key: '7|phase|Staging backups locally', text: 'Backup of Nightly: Staging backups locally' })
    expect(b?.key).toBe(a?.key)
  })

  it.each([
    ['Uploading 3 staged items to remote storage...', 'Uploading to remote storage'],
    ['Stopping 4 containers...', 'Stopping containers'],
    ['Restarting 4 containers...', 'Restarting containers'],
    ['Verifying archive...', 'Verifying archive'],
  ])('names the phase of %s', (message, phase) => {
    expect(progressAnnouncement({ activeRun: RUN, running: true, phaseMessage: message })?.text).toBe(`Backup of Nightly: ${phase}`)
  })

  it('does not change while only counts change', () => {
    const before = progressAnnouncement({ activeRun: RUN, running: true, failed: 0 })
    const after = progressAnnouncement({ activeRun: RUN, running: true, failed: 2 })
    expect(after?.key).toBe(before?.key)
  })

  it('announces the finish, with failures', () => {
    expect(progressAnnouncement({ activeRun: RUN, running: false })?.text).toBe('Backup of Nightly finished')
    expect(progressAnnouncement({ activeRun: RUN, running: false, failed: 1 })?.text).toBe('Backup of Nightly finished with 1 failed item')
    expect(progressAnnouncement({ activeRun: RUN, running: false, failed: 2 })?.text).toBe('Backup of Nightly finished with 2 failed items')
  })

  it('names a restore', () => {
    expect(progressAnnouncement({ activeRun: { ...RUN, run_type: 'restore' }, running: true })?.text).toBe('Restore of Nightly started')
  })
})
