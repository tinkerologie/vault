import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { filterQuery, readHashFilters, writeHashFilters } from './urlFilters.js'

const DEFAULTS = { status: 'all', job: '0', q: '' }

describe('filterQuery', () => {
  it('leaves out defaults and empty values, in a stable order', () => {
    expect(filterQuery({ q: 'nightly', status: 'failed', job: '0' }, DEFAULTS)).toBe('status=failed&q=nightly')
  })

  it('is empty when every filter is at its default', () => {
    expect(filterQuery({ status: 'all', job: '0', q: '' }, DEFAULTS)).toBe('')
  })

  it('keeps keys outside the defaults after the known ones', () => {
    expect(filterQuery({ review: 'outstanding', status: 'failed' }, DEFAULTS)).toBe('status=failed&review=outstanding')
  })

  it('treats numbers and booleans as text', () => {
    expect(filterQuery({ job: 3, review: false, open: true }, DEFAULTS)).toBe('job=3&open=true')
  })
})

describe('readHashFilters', () => {
  it('returns the query of the matching route', () => {
    const params = readHashFilters('/history', '#/history?status=failed&q=a%20b')
    expect(params?.get('status')).toBe('failed')
    expect(params?.get('q')).toBe('a b')
  })

  it('returns empty params for the route without a query', () => {
    expect([...(readHashFilters('/history', '#/history') ?? [])]).toEqual([])
  })

  it('returns null for another route', () => {
    expect(readHashFilters('/history', '#/logs?level=error')).toBeNull()
  })
})

describe('writeHashFilters', () => {
  /** @type {any} */
  let replaceState
  beforeEach(() => {
    replaceState = vi.fn()
    vi.stubGlobal('window', { location: { hash: '#/history' }, history: { state: { keep: 1 }, replaceState } })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('replaces the current entry instead of adding one', () => {
    writeHashFilters('/history', { status: 'failed', job: '0', q: '' }, DEFAULTS)
    expect(replaceState).toHaveBeenCalledWith({ keep: 1 }, '', '#/history?status=failed')
  })

  it('drops the query once every filter is back at its default', () => {
    window.location.hash = '#/history?status=failed'
    writeHashFilters('/history', DEFAULTS, DEFAULTS)
    expect(replaceState).toHaveBeenCalledWith({ keep: 1 }, '', '#/history')
  })

  it('does nothing when the URL already says the same', () => {
    window.location.hash = '#/history?status=failed'
    writeHashFilters('/history', { status: 'failed' }, DEFAULTS)
    expect(replaceState).not.toHaveBeenCalled()
  })

  it('never rewrites another page’s URL', () => {
    window.location.hash = '#/logs'
    writeHashFilters('/history', { status: 'failed' }, DEFAULTS)
    expect(replaceState).not.toHaveBeenCalled()
  })
})
