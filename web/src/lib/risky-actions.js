// What the typed confirmations for Vault's riskiest actions say (#525). The text
// follows the backend: restore-db reopens the database in place, purge cascades
// from job_runs to restore_points, and purge leaves the files on storage.

/**
 * @param {{ storageName: string, path: string, modified?: string }} backup
 * @returns {{ source: { label: string, value: string }[], consequences: string[] }}
 */
export function replaceDatabaseSummary({ storageName, path, modified = '' }) {
  return {
    source: [
      { label: 'Storage', value: storageName },
      { label: 'Backup file', value: path },
      ...(modified ? [{ label: 'Backup date', value: modified }] : []),
    ],
    consequences: [
      'The current Vault database (jobs, storage destinations, history and settings) is replaced with this backup copy.',
      'Changes made after this backup was taken are lost.',
      'This cannot be undone from the UI.',
      'Vault reopens the database. If reopening fails, restart Vault.',
    ],
  }
}

/** @param {{ ok?: boolean, checks?: { label: string, status: string, detail?: string }[] } | null | undefined} preflight */
function failedChecks(preflight) {
  const failed = (preflight?.checks ?? []).filter((c) => c.status === 'fail')
  if (failed.length === 0) return 'Pre-flight did not pass'
  return failed.map((c) => (c.detail ? `${c.label}: ${c.detail}` : c.label)).join('; ')
}

/**
 * @param {{ jobName: string, pointDate: string, destination: string, cleanDestination: boolean, preflight: any }[]} units
 * @returns {{ source: { label: string, value: string }[], consequences: string[] }}
 */
export function restoreAnywaySummary(units) {
  const failed = units.filter((u) => !u.preflight?.ok)
  const cleaned = units.filter((u) => u.cleanDestination).map((u) => u.jobName)
  return {
    source: [
      ...failed.map((u) => ({ label: `Failed checks: ${u.jobName}`, value: failedChecks(u.preflight) })),
      ...units.map((u) => ({ label: `Restoring: ${u.jobName}`, value: `${u.pointDate} to ${u.destination}` })),
    ],
    consequences: [
      units.length === 1
        ? 'The selected job is submitted even though its pre-flight checks failed.'
        : `All ${units.length} selected jobs are submitted, including ${failed.length} that failed pre-flight checks.`,
      'Existing data at the destination is overwritten.',
      ...(cleaned.length > 0 ? [`Files at the destination that are not in the backup are removed for: ${cleaned.join(', ')}.`] : []),
      'Failed checks can make the restore fail or leave incomplete data.',
    ],
  }
}

export const PURGE_HISTORY_SUMMARY = {
  source: [{ label: 'Scope', value: 'All jobs: the entire history, not only the filtered rows' }],
  consequences: [
    'All job-run history is deleted.',
    'All restore points are deleted, so Vault can no longer restore from them.',
    'Backup files stay on storage as orphans and must be re-imported.',
    'This cannot be undone.',
  ],
}
