// One wording for Job Run statuses (#694). The API keeps two spellings for a
// clean run ("success" from older rows, "completed" now) and two for a failed
// one; every page shows the same word, matching History's status filter.

const RUN_STATUS_LABELS = {
  completed: 'Completed',
  success: 'Completed',
  failed: 'Failed',
  error: 'Failed',
  partial: 'Partial',
  running: 'Running',
  skipped: 'Skipped',
  cancelled: 'Cancelled',
  pending: 'Queued',
  queued: 'Queued',
}

/** The label for a Job Run status, such as "Completed" for "success". */
export function runStatusLabel(status) {
  const key = String(status ?? '').toLowerCase()
  if (!key) return 'Unknown'
  return RUN_STATUS_LABELS[key] ?? key.charAt(0).toUpperCase() + key.slice(1)
}
