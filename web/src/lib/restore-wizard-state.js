/**
 * Per-job state rules for the restore wizard (#702).
 *
 * A chosen Restore Point, its options and its file selection belong to one
 * job. Going back to change the item selection must keep that state for every
 * job whose selected items are unchanged, and reset only the jobs whose item
 * set changed. Plain JS so it runs under the node Vitest project.
 */

import { effectiveItemType, itemDisplayLabel } from './utils.js'

/**
 * Every item the wizard can restore: each job's current items plus the items
 * only its retained Restore Points still hold, such as a deleted container
 * (#669). One entry per type and name, linked to every job that has it.
 *
 * @param {Array<any>} jobs detailed jobs, optionally with historical_items
 * @returns {Array<any>}
 */
export function gatherRestorableItems(jobs) {
  const byKey = new Map()
  const add = (job, item, historical) => {
    const type = effectiveItemType(item)
    const key = `${type}:${itemDisplayLabel(item)}`
    let entry = byKey.get(key)
    if (!entry) {
      entry = {
        name: item.item_name,
        type,
        item_name: item.item_name,
        item_type: type,
        item_id: item.item_id,
        settings: item.settings,
        jobs: [],
        historicalJobIds: [],
        historical: true,
        lastBackupAt: null,
      }
      byKey.set(key, entry)
    }
    if (!entry.jobs.some((j) => j.id === job.id)) entry.jobs.push(job)
    if (historical) {
      entry.historicalJobIds.push(job.id)
      if (item.last_backup_at && (!entry.lastBackupAt || item.last_backup_at > entry.lastBackupAt)) entry.lastBackupAt = item.last_backup_at
    } else {
      entry.historical = false
      if (entry.settings == null) entry.settings = item.settings
    }
  }
  for (const job of jobs || []) {
    if (!job) continue
    for (const item of job.items || []) add(job, item, false)
    for (const item of job.historical_items || []) add(job, item, true)
  }
  return Array.from(byKey.values())
}

function jobIdOf(job) {
  return job?.id ?? job
}

/**
 * The sorted selected item keys of each job, joined into one comparable string.
 *
 * @param {Array<{ jobs?: Array<any> }>} selected
 * @param {(item: any) => string} keyFn
 * @returns {Map<any, string>}
 */
export function jobItemSignatures(selected, keyFn) {
  const keys = new Map()
  for (const item of selected || []) {
    for (const job of item.jobs || []) {
      const id = jobIdOf(job)
      if (!keys.has(id)) keys.set(id, [])
      keys.get(id).push(keyFn(item))
    }
  }
  const signatures = new Map()
  for (const [id, list] of keys) signatures.set(id, [...new Set(list)].sort().join('|'))
  return signatures
}

/**
 * Compares the current selection with the item sets recorded when each job's
 * state was made.
 *
 * @param {{ selected: Array<any>, recorded: Map<any, string>, keyFn: (item: any) => string }} input
 * @returns {{ kept: any[], cleared: any[], dropped: any[], added: any[], signatures: Map<any, string> }}
 *   kept: unchanged jobs; cleared: jobs whose item set changed; dropped: jobs
 *   with no selected item left; added: jobs with no recorded state yet.
 */
export function reconcileJobState({ selected, recorded, keyFn }) {
  const signatures = jobItemSignatures(selected, keyFn)
  const kept = []
  const cleared = []
  const added = []
  for (const [id, signature] of signatures) {
    if (!recorded.has(id)) added.push(id)
    else if (recorded.get(id) === signature) kept.push(id)
    else cleared.push(id)
  }
  const dropped = [...recorded.keys()].filter((id) => !signatures.has(id))
  return { kept, cleared, dropped, added, signatures }
}

/**
 * The names of the items a finished Restore Run did not restore, read from
 * its per-item results; null when the run records none (#701).
 *
 * @param {{ log?: string } | null | undefined} run
 * @returns {Set<string> | null}
 */
export function failedRestoreItemNames(run) {
  if (!run?.log) return null
  let items
  try {
    items = JSON.parse(run.log)
  } catch {
    return null
  }
  if (!Array.isArray(items)) return null
  return new Set(items.filter((i) => i && i.status !== 'ok' && i.name).map((i) => i.name))
}
