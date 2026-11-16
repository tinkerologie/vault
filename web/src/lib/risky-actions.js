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

/**
 * The recovery wizard's wording for replacing a Vault that already holds settings.
 * A null `current` means the check failed, so it can't be assumed empty.
 *
 * @param {{ storageName: string, path: string, modified?: string }} backup
 * @param {{ jobs: number, storage: number } | null} current
 */
export function recoverReplaceSummary(backup, current) {
  const { source, consequences } = replaceDatabaseSummary(backup)
  const count = (n, one) => `${n} ${one}${n === 1 ? '' : 's'}`
  const holding = current
    ? `This Vault currently has ${count(current.jobs, 'job')} and ${count(current.storage, 'storage destination')}, and they are replaced.`
    : "Vault couldn't check what this server currently holds, so anything it has is replaced."
  return { source, consequences: [holding, ...consequences, 'Your backed-up files on storage are not touched.'] }
}

/** Whether recovery would overwrite something, so it needs a typed confirmation. */
export function recoverReplacesExisting(current) {
  return !current || current.jobs > 0 || current.storage > 0
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
    'Job-run history is deleted. Runs linked to Restore Requests stay in History.',
    'All restore points are deleted, so Vault can no longer restore from them.',
    'Backup files stay on storage as orphans and must be re-imported.',
    'This cannot be undone.',
  ],
}
