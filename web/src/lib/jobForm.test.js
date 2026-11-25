import { describe, it, expect } from 'vitest'
import { configuredSections, readFormMode, writeFormMode, nameFieldError, editLinkFor, parseEditLink } from './jobForm.js'

const defaults = {
  retention_count: 5, retention_days: 30, keep_latest: 0, keep_daily: 0, keep_weekly: 0, keep_monthly: 0, keep_yearly: 0,
  verify_schedule: '', full_backup_schedule: '', pre_script: '', post_script: '', notify_on: 'failure', verify_backup: true,
  retry_max_override: '', retry_delays_override: '', anomaly_sensitivity: '', max_parallel_uploads: 3,
  defer_remote_upload: false, adaptive_enabled: false, auto_include_containers: false, items: [],
}

describe('configuredSections', () => {
  it('marks nothing for a default job', () => {
    expect(Object.values(configuredSections(defaults)).some(Boolean)).toBe(false)
  })

  it('marks each section that differs from its default', () => {
    const got = configuredSections({
      ...defaults,
      retention_days: 14, keep_weekly: 4, verify_schedule: '0 4 * * 0', full_backup_schedule: '0 4 1 * *',
      post_script: '/boot/x.sh', notify_on: 'always', verify_backup: false, retry_max_override: '2',
      anomaly_sensitivity: 'strict', max_parallel_uploads: 1, defer_remote_upload: true, adaptive_enabled: true,
      auto_include_containers: true,
      items: [
        { item_type: 'vm', settings: '{"restore_verify_mode":"tcp"}' },
        { item_type: 'container', settings: { excluded_mounts: ['/data'] } },
        { item_type: 'folder', settings: '{"exclude_paths":["*.log"]}' },
      ],
    })
    expect(Object.entries(got).filter(([, v]) => !v)).toEqual([])
  })

  it('tolerates malformed item settings', () => {
    const got = configuredSections({ ...defaults, items: [{ item_type: 'container', settings: '{bad' }, { item_type: 'vm' }] })
    expect(got.containers).toBe(false)
    expect(got.vmVerify).toBe(false)
  })
})

describe('form mode preference', () => {
  it('round-trips through storage and ignores junk', () => {
    const data = {}
    const store = { getItem: (k) => data[k] ?? null, setItem: (k, v) => { data[k] = v } }
    expect(readFormMode(store)).toBe('')
    writeFormMode('full', store)
    expect(readFormMode(store)).toBe('full')
    data['vault.jobForm'] = 'weird'
    expect(readFormMode(store)).toBe('')
  })

  it('survives storage that throws', () => {
    const store = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
    expect(readFormMode(store)).toBe('')
    expect(() => writeFormMode('steps', store)).not.toThrow()
  })
})

describe('nameFieldError', () => {
  it('maps name errors to the field', () => {
    expect(nameFieldError('a job with this name already exists')).toMatch(/already exists/)
    expect(nameFieldError('name is required')).toBe('Enter a job name.')
    expect(nameFieldError('a storage destination with this name already exists', 'destination')).toMatch(/A destination/)
  })

  it('leaves other errors alone', () => {
    expect(nameFieldError('storage destination not found')).toBeNull()
    expect(nameFieldError('name must not contain slashes')).toBeNull()
    expect(nameFieldError('')).toBeNull()
    expect(nameFieldError(undefined)).toBeNull()
  })
})

describe('edit links to a job item (#711)', () => {
  it('round-trips a job and item through the hash', () => {
    const link = editLinkFor(12, 'plex media')
    expect(link).toBe('/jobs?edit=12&item=plex+media')
    expect(parseEditLink(link)).toEqual({ jobId: 12, item: 'plex media' })
  })

  it('works without an item', () => {
    expect(editLinkFor(3)).toBe('/jobs?edit=3')
    expect(parseEditLink('/jobs?edit=3')).toEqual({ jobId: 3, item: '' })
  })

  it('ignores links that are not a valid job edit', () => {
    for (const raw of ['/jobs', '/jobs?edit=', '/jobs?edit=abc', '/jobs?edit=-4', '/jobs?edit=1.5', '/history?edit=3', '', undefined]) {
      expect(parseEditLink(raw)).toBeNull()
    }
  })
})

describe('form mode with blocked storage', () => {
  it('reads and writes nothing, without throwing, when localStorage itself throws', () => {
    const desc = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new DOMException('blocked', 'SecurityError') } })
    try {
      expect(readFormMode()).toBe('')
      expect(() => writeFormMode('full')).not.toThrow()
    } finally {
      if (desc) Object.defineProperty(globalThis, 'localStorage', desc)
      else delete globalThis.localStorage
    }
  })
})
