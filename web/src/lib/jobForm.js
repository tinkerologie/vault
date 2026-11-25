// Pure helpers for the job editor (Jobs.svelte).

/** @param {{ settings?: unknown }} item */
function itemSettings(item) {
  if (!item?.settings) return {}
  if (typeof item.settings === 'object') return /** @type {Record<string, any>} */ (item.settings)
  try {
    return JSON.parse(/** @type {string} */ (item.settings)) || {}
  } catch {
    return {}
  }
}

const nonEmpty = (v) => Array.isArray(v) && v.length > 0

/**
 * Which collapsed advanced sections hold something other than the defaults, so
 * the editor can mark them "Configured" (#712). Keys match the sections in
 * Jobs.svelte step 3.
 *
 * @param {Record<string, any>} form
 * @returns {Record<string, boolean>}
 */
export function configuredSections(form) {
  const items = Array.isArray(form.items) ? form.items : []
  const settingsOf = (type) => items.filter((i) => i.item_type === type).map(itemSettings)
  const blank = (v) => String(v ?? '').trim() === ''
  return {
    retention: Number(form.retention_count) !== 5 || Number(form.retention_days) !== 30,
    ltr: ['keep_latest', 'keep_daily', 'keep_weekly', 'keep_monthly', 'keep_yearly'].some((k) => Number(form[k] || 0) > 0),
    verifySchedule: !blank(form.verify_schedule),
    fullSchedule: !blank(form.full_backup_schedule),
    scripts: !blank(form.pre_script) || !blank(form.post_script),
    notifications: (form.notify_on || 'failure') !== 'failure',
    verification: form.verify_backup === false,
    retry: !blank(form.retry_max_override) || !blank(form.retry_delays_override),
    anomaly: !blank(form.anomaly_sensitivity),
    concurrency: Number(form.max_parallel_uploads || 3) !== 3,
    deferred: !!form.defer_remote_upload,
    adaptive: !!form.adaptive_enabled,
    autoInclude: !!form.auto_include_containers,
    vmVerify: settingsOf('vm').some((s) => s.restore_verify_mode && s.restore_verify_mode !== 'running'),
    containers: settingsOf('container').some(
      (s) => nonEmpty(s.exclude_paths) || nonEmpty(s.excluded_mounts) || nonEmpty(s.included_mounts) || s.database_dump === true,
    ),
    folders: settingsOf('folder').some((s) => nonEmpty(s.exclude_paths)),
  }
}

const FORM_MODE_KEY = 'vault.jobForm'

/**
 * The editor layout the user last picked: 'full' or 'steps', or '' when they
 * never chose. Storage can be unavailable (private windows, blocked site data).
 * @param {Storage | undefined} [store]
 */
export function readFormMode(store = globalThis.localStorage) {
  try {
    const v = store?.getItem(FORM_MODE_KEY)
    return v === 'full' || v === 'steps' ? v : ''
  } catch {
    return ''
  }
}

/** @param {'full' | 'steps'} mode @param {Storage | undefined} [store] */
export function writeFormMode(mode, store = globalThis.localStorage) {
  try {
    store?.setItem(FORM_MODE_KEY, mode)
  } catch {
    // A convenience only: the editor still works without it.
  }
}

/**
 * Maps a save error about the name to a message for under the Name field, so
 * it isn't only a toast (#688). Returns null for any other error. Matches the
 * server's "a job with this name already exists" and "name is required".
 * @param {string} message
 * @param {string} noun e.g. "job" or "destination"
 * @returns {string | null}
 */
export function nameFieldError(message, noun = 'job') {
  const text = String(message || '')
  if (!/name/i.test(text)) return null
  if (/(already exists|duplicate|unique|taken)/i.test(text)) {
    return `A ${noun} with this name already exists. Choose a different name.`
  }
  if (/(required|empty|blank)/i.test(text)) return `Enter a ${noun} name.`
  return null
}

/**
 * Hash path that opens a job's editor, optionally at one item's exclusions,
 * e.g. from a History error that suggests excluding a path (#711).
 * @param {number} jobId
 * @param {string} [item]
 */
export function editLinkFor(jobId, item = '') {
  const params = new URLSearchParams({ edit: String(jobId) })
  if (item) params.set('item', item)
  return `/jobs?${params}`
}

/**
 * Reads an edit link written by editLinkFor. Returns null for anything that
 * isn't a Jobs page link with a positive whole job id.
 * @param {string | undefined} rawHash
 * @returns {{ jobId: number, item: string } | null}
 */
export function parseEditLink(rawHash) {
  if (!rawHash) return null
  const [path, query = ''] = rawHash.split('?')
  if (path !== '/jobs') return null
  const params = new URLSearchParams(query)
  const edit = params.get('edit') || ''
  if (!/^[1-9]\d*$/.test(edit)) return null
  return { jobId: Number(edit), item: params.get('item') || '' }
}
