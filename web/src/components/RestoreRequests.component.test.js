import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/svelte'
import RestoreRequestList from './RestoreRequestList.svelte'
import RestoreRequestReview from './RestoreRequestReview.svelte'

const api = vi.hoisted(() => ({
  listRestoreRequests: vi.fn(),
  getRestoreRequest: vi.fn(),
}))
vi.mock('../lib/api.js', () => ({ api }))

const timestamp = '2026-11-11T01:00:00Z'
const request = {
  id: 'first',
  status: 'pending',
  requester_name: 'Recovery advisor',
  job_name: 'Folder backups',
  item: { item_name: 'Documents', item_type: 'folder' },
  restore_point: { id: 5, created_at: timestamp },
  membership: 'confirmed',
  expires_at: '2026-11-12T01:00:00Z',
  target_choice: 'original',
  resolved_path: '/mnt/user/documents',
  verification: { status: 'passed', mode: 'deep', completed_at: timestamp },
  chain: { status: 'confirmed', steps: [
    { restore_point_id: 4, backup_type: 'full', membership: 'confirmed', data_required: true, artifacts: 'confirmed' },
    { restore_point_id: 5, backup_type: 'incremental', membership: 'confirmed', data_required: true, artifacts: 'confirmed' },
  ] },
  effective_write_mode: 'merge',
  effects: ['overwrite_matching_entries', 'keep_unrelated_entries', 'prune_replayed_entries'],
  warnings: ['target_too_shallow_to_clear'],
  eligible_for_approval: true,
}

beforeEach(() => vi.resetAllMocks())

it('shows an empty pending state and refreshes the review links', async () => {
  api.listRestoreRequests.mockResolvedValueOnce({ requests: [] })
    .mockResolvedValue({ requests: [request] })
  render(RestoreRequestList)
  await screen.findByText('No pending Restore Requests.')
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh requests' }))
  const link = await screen.findByRole('link', { name: 'Documents · Folder backups' })
  expect(link.getAttribute('href')).toBe('#/restore?request=first')
})

it('shows expired request history, retries an error, and loads the next cursor', async () => {
  const expired = { ...request, status: 'expired', terminal_at: timestamp }
  const withdrawn = { ...request, id: 'second', status: 'withdrawn', item: { ...request.item, item_name: 'Pictures' } }
  api.listRestoreRequests.mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValueOnce({ requests: [expired], next_before: 'first' })
    .mockResolvedValueOnce({ requests: [withdrawn] })
  render(RestoreRequestList, { history: true })
  await screen.findByText('Could not load Restore Requests.')
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh requests' }))
  await screen.findByText('expired')
  await fireEvent.click(screen.getByRole('button', { name: 'Load more requests' }))
  await screen.findByText('withdrawn')
  expect(api.listRestoreRequests).toHaveBeenLastCalledWith({ status: 'withdrawn,expired', before: 'first' })
  expect(screen.getByRole('link', { name: 'Documents · Folder backups' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Pictures · Folder backups' })).toBeTruthy()
})

it('shows the exact review effects and Backup Chain without starting a restore', async () => {
  api.getRestoreRequest.mockResolvedValue(request)
  render(RestoreRequestReview, { requestId: 'first' })
  const review = screen.getByRole('region', { name: 'Restore Request review' })
  await within(review).findByText('/mnt/user/documents')
  expect(within(review).getByText('Recovery advisor')).toBeTruthy()
  expect(within(review).getByText(/Overwrite matching entries/)).toBeTruthy()
  expect(within(review).getByText(/Keep existing entries/)).toBeTruthy()
  expect(within(review).getByText(/If Vault can read the final file listing and the earlier Backup Chain indexes/)).toBeTruthy()
  expect(within(review).getByText(/Vault would use merge/)).toBeTruthy()
  expect(within(review).getByText('passed')).toBeTruthy()
  expect(within(review).getByText(/#4 · full/)).toBeTruthy()
  expect(within(review).queryByRole('button')).toBeNull()
})

it('shows evidence gaps, write effects and the retained terminal reason', async () => {
  api.getRestoreRequest.mockResolvedValue({
    ...request,
    status: 'expired',
    terminal_reason: 'approval_window_elapsed',
    target_choice: 'alternate',
    resolved_path: undefined,
    verification: { status: 'none' },
    effective_write_mode: 'replace',
    effects: ['clear_target_contents', 'write_backed_up_entries'],
    warnings: ['unverified_backup'],
    eligible_for_approval: false,
    ineligibility_reasons: ['chain_unconfirmed'],
  })
  render(RestoreRequestReview, { requestId: 'expired' })
  await screen.findByText('Ineligible for approval')
  expect(screen.getByText('Alternate location')).toBeTruthy()
  expect(screen.getByText('Unavailable')).toBeTruthy()
  expect(screen.getByText(/Clear all existing target contents/)).toBeTruthy()
  expect(screen.getByText(/no recorded Verification/)).toBeTruthy()
  expect(screen.getByText(/Backup Chain could not be confirmed/)).toBeTruthy()
  expect(screen.getByText('The 24-hour review window elapsed.')).toBeTruthy()
})

it.each([
  [403, 'Operator access required to review this Restore Request.'],
  [404, 'Restore Request not found.'],
  [502, 'Could not load this Restore Request.'],
])('reports a failed request lookup with status %d', async (status, expected) => {
  api.getRestoreRequest.mockRejectedValue(Object.assign(new Error('unavailable'), { status }))
  render(RestoreRequestReview, { requestId: 'unavailable' })
  expect((await screen.findByRole('alert')).textContent).toBe(expected)
})
