import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  listJobs: vi.fn(),
  listRestoreRequests: vi.fn(async () => ({ requests: [] })),
  getHistory: vi.fn(),
  getHistoryTrend: vi.fn(),
  getRunnerStatus: vi.fn(),
  listAnomalies: vi.fn(),
  acknowledgeRuns: vi.fn(),
}))
vi.mock('../lib/api.js', () => ({ api, isReplicaMode: () => false }))
vi.mock('../lib/router.svelte.js', () => ({ getRawHash: () => '/history?review=outstanding' }))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))

import History from './History.svelte'

it('keeps selected runs after a failed acknowledgement so the user can retry', async () => {
  const run = { id: 7, job_id: 1, status: 'failed', started_at: '2026-10-01T12:00:00Z' }
  api.listJobs.mockResolvedValue([{ id: 1, name: 'VM Backups' }])
  api.getHistory.mockResolvedValueOnce([run]).mockResolvedValue([{ ...run, acknowledged_at: '2026-10-01T12:01:00Z' }])
  api.getHistoryTrend.mockResolvedValue({ points: [] })
  api.getRunnerStatus.mockResolvedValue({ active: false, queue: [] })
  api.listAnomalies.mockResolvedValue({ anomalies: [] })
  api.acknowledgeRuns
    .mockRejectedValueOnce(new Error('Could not save review'))
    .mockResolvedValueOnce({ acknowledged: 1, skipped: 0 })

  render(History)
  const checkbox = await screen.findByRole('checkbox', { name: 'Select run 7 for acknowledgement' })
  await fireEvent.click(checkbox)
  const action = screen.getByRole('button', { name: 'Acknowledge selected' })
  await fireEvent.click(action)
  await screen.findByText('Could not save review')
  expect(checkbox.checked).toBe(true)

  await fireEvent.click(action)
  await screen.findByText('No matching runs')
  expect(api.acknowledgeRuns).toHaveBeenCalledTimes(2)
  expect(api.acknowledgeRuns).toHaveBeenLastCalledWith([7], '')
})
