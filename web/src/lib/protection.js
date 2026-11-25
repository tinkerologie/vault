// One protection classification for the Dashboard and Disaster Recovery, so both
// pages count the same items the same way (#708, #694). The server decides which
// job items are protected, out of date or pending (GET /health/summary); this
// adds discovered items in no job, which only discovery knows about.
//
//   protected    a recent enough Restore Point holds it (green)
//   out_of_date  its newest Restore Point is older than its schedule allows
//   pending      in a job, waiting for its first backup
//   unprotected  discovered, in no job
//   unknown      the summary failed to load, so nothing is known (never green)

/** @typedef {'protected'|'out_of_date'|'pending'|'unprotected'|'unknown'} ProtectionState */
/** @typedef {{ key: string, type: string, name: string, group: 'container'|'vm'|'folder'|'flash', source: any, state: ProtectionState, lastBackupAt: string|null }} ProtectedItem */

/**
 * The state of one item key ("type:name") in the health summary.
 * @param {string} key
 * @param {any} summary GET /health/summary, or null when it failed.
 * @returns {ProtectionState}
 */
export function protectionState(key, summary) {
  if (!summary) return 'unknown'
  if ((summary.protected_keys || []).includes(key)) {
    return (summary.out_of_date_keys || []).includes(key) ? 'out_of_date' : 'protected'
  }
  if ((summary.pending_keys || []).includes(key)) return 'pending'
  return 'unprotected'
}

const nameOf = (key) => key.slice(key.indexOf(':') + 1)

/**
 * @param {object} input
 * @param {any} input.summary GET /health/summary, or null when it failed.
 * @param {{ containers?: any[], vms?: any[], folders?: any[] }} [input.discovered] Discovery results.
 * @param {{ containers?: boolean, vms?: boolean, folders?: boolean }} [input.available] Whether each discovery succeeded.
 * @param {{ containers?: boolean, vms?: boolean, folders?: boolean, flash?: boolean }} [input.enabled] Which item types are tracked (Settings).
 */
export function classifyProtection({ summary, discovered = {}, available = {}, enabled = {} }) {
  const on = { containers: true, vms: true, folders: true, flash: true, ...enabled }
  const jobKeys = summary ? [...(summary.protected_keys || []), ...(summary.pending_keys || [])] : []
  const keysOfType = (type) => [...new Set(jobKeys.filter((key) => key.startsWith(`${type}:`)))]
  const lastBackups = summary?.item_last_backup_at || {}

  /** @returns {ProtectedItem} */
  const item = (type, group, name, source) => {
    const key = `${type}:${name}`
    return { key, type, name, group, source, state: protectionState(key, summary), lastBackupAt: lastBackups[key] ?? null }
  }
  const unique = (items) => [...new Map(items.map((it) => [it.key, it])).values()]

  // Discovery is the list of what exists, when it worked. When it didn't, the
  // job items are the best record left, so they still count.
  const byDiscovery = (type, list, isAvailable, isOn) => {
    if (!isOn) return []
    if (isAvailable) return unique((list || []).map((src) => item(type, type, src.name, src)))
    return keysOfType(type).map((key) => item(type, type, nameOf(key), { name: nameOf(key) }))
  }
  const container = byDiscovery('container', discovered.containers, available.containers, on.containers)
  const vm = byDiscovery('vm', discovered.vms, available.vms, on.vms)

  // Folder discovery only lists presets such as the Flash Drive; custom folders
  // exist only as job items, so those always count.
  const folder = []
  const flash = []
  const seenFolders = new Set()
  if (available.folders) {
    for (const src of discovered.folders || []) {
      const isFlash = src.settings?.preset === 'flash'
      if (!(isFlash ? on.flash : on.folders) || seenFolders.has(src.name)) continue
      seenFolders.add(src.name)
      ;(isFlash ? flash : folder).push(item('folder', isFlash ? 'flash' : 'folder', src.name, src))
    }
  }
  if (on.folders || (!available.folders && on.flash)) {
    for (const key of keysOfType('folder')) {
      const name = nameOf(key)
      if (seenFolders.has(name)) continue
      seenFolders.add(name)
      folder.push(item('folder', 'folder', name, { name }))
    }
  }

  const all = [...container, ...vm, ...folder, ...flash]
  const count = (state) => all.filter((it) => it.state === state).length
  return {
    known: Boolean(summary),
    groups: { container, vm, folder, flash },
    items: all,
    counts: {
      total: all.length,
      protected: count('protected'),
      outOfDate: count('out_of_date'),
      pending: count('pending'),
      unprotected: count('unprotected'),
    },
  }
}

/** Whole-number share of items that are protected and up to date. */
export function protectedPercent(counts) {
  return counts.total > 0 ? Math.round((counts.protected / counts.total) * 100) : 0
}

/**
 * Status dot colour per protection state (#710), shared by the Dashboard and
 * Disaster Recovery so a state looks the same on both. Unprotected and unknown
 * are neutral: green is only for verified protection, and an item waiting to be
 * added to a job is not yet a failure. Every dot sits beside its state's word.
 * @type {Readonly<Record<ProtectionState, string>>}
 */
export const PROTECTION_DOT = Object.freeze({
  protected: 'bg-success',
  out_of_date: 'bg-warning',
  pending: 'bg-info',
  unprotected: 'bg-surface-5',
  unknown: 'bg-surface-5',
})

/**
 * @param {ProtectionState | string} state
 * @returns {string}
 */
export function protectionDot(state) {
  return PROTECTION_DOT[/** @type {ProtectionState} */ (state)] || PROTECTION_DOT.unknown
}
