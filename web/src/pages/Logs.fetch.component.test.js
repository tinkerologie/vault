// Logs.svelte against the REAL unified log store (#509): only the HTTP
// boundary (api.js) and the WebSocket subscription are mocked, so these tests
// prove the production failure contract rather than a fixture's idea of it.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { flush, installScrollBox, offsetFromBottom, userScroll } from '../test-support/scroll-box.js'

const http = vi.hoisted(() => ({ getActivity: null }))

vi.mock('../lib/api.js', () => ({
  api: {
    getActivity: (...args) => http.getActivity(...args),
    getRunLogs: async () => ({ entries: [] }),
    purgeActivity: async () => ({}),
  },
  isReplicaMode: () => false,
}))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))

import Logs from './Logs.svelte'

const BATCH = 30 // the store's interactive page size

// One activity page, newest first (as the API returns it).
function page(fromId, count) {
  return Array.from({ length: count }, (_, i) => {
    const id = fromId - i
    return {
      id,
      created_at: new Date(Date.parse('2026-10-20T12:00:00Z') + id * 1000).toISOString(),
      level: 'info',
      category: 'backup',
      message: `Activity ${id}`,
      details: '',
    }
  })
}

let geo

beforeEach(() => {
  geo = installScrollBox({ clientHeight: 400, rowHeight: 20 })
})

afterEach(() => {
  geo.restore()
})

describe('Logs with the real store', () => {
  it('shows the error state when the older-page fetch is rejected', async () => {
    const calls = []
    http.getActivity = vi.fn(async (limit, category, beforeId) => {
      calls.push({ limit, beforeId })
      if (beforeId === undefined) return page(100, BATCH) // newest page: a full page, so hasMore
      // The background full-history load (silent) and the reader's scroll
      // fetch (not silent) both continue from the cursor; both fail.
      throw new Error('older page unavailable')
    })

    render(Logs)
    await flush()
    const box = document.querySelector('[role="log"]')
    expect(box).toBeTruthy()
    // The background load failed silently: no error, history still incomplete.
    expect(calls.map((c) => c.limit)).toEqual([BATCH, 1000])
    expect(screen.queryByText('older page unavailable')).toBeNull()

    await userScroll(geo, box, offsetFromBottom(geo, box, 100))
    await flush()

    expect(calls.at(-1)).toEqual({ limit: BATCH, beforeId: 71 })
    expect(screen.getByText('older page unavailable')).toBeTruthy()
    expect(document.querySelector('[role="log"]')).toBeNull()
  })

  it('shows the error state without a console when the first load fails', async () => {
    http.getActivity = vi.fn(async () => {
      throw new Error('daemon unavailable')
    })
    render(Logs)
    await flush()
    expect(screen.getByText('daemon unavailable')).toBeTruthy()
    expect(document.querySelector('[role="log"]')).toBeNull()
  })
})
