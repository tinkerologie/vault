// The Anomalies page pages on anomaly id (#587). A live update can show an
// older row before "Load more" reaches it; the next page must not add it twice.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({ listAnomalies: vi.fn() }))
const ws = vi.hoisted(() => ({ listener: null }))
vi.mock('../lib/api.js', () => ({ api, isReplicaMode: () => false }))
vi.mock('../lib/ws.svelte.js', () => ({
  onWsMessage: (fn) => { ws.listener = fn; return () => {} },
}))

import Anomalies from './Anomalies.svelte'

const row = (id) => ({
  id, severity: 'warning', state: 'open', summary: `anomaly ${id}`, detector: 'test',
  scope_kind: 'job', scope_id: 1, first_seen_at: '2026-01-01T00:00:00Z', last_seen_at: '2026-01-01T00:00:00Z',
})

describe('Anomalies load more', () => {
  it('does not repeat a row a live update already added', async () => {
    api.listAnomalies
      .mockResolvedValueOnce({ anomalies: [row(3)], next_cursor: 'v2:3' })
      .mockResolvedValueOnce({ anomalies: [row(2), row(1)], next_cursor: '' })
    render(Anomalies)
    await screen.findByLabelText('Select anomaly 3')

    ws.listener({ type: 'anomaly.updated', data: row(1) })
    await fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
    await screen.findByLabelText('Select anomaly 2')

    // The first page uses the same id order as the pages after it.
    expect(api.listAnomalies).toHaveBeenNthCalledWith(1, expect.objectContaining({ order: 'id', cursor: undefined }))
    expect(api.listAnomalies).toHaveBeenLastCalledWith(expect.objectContaining({ order: 'id', cursor: 'v2:3' }))
    expect(screen.getAllByLabelText('Select anomaly 1')).toHaveLength(1)
  })
})
