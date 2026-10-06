/**
 * Restore plan calculation and coverage rules for the restore wizard.
 *
 * Extracted as pure JS so it is testable under Vitest without DOM or Svelte runtime.
 */

export const REASON_NOT_IN_JOB = 'item is not in this job'
export const REASON_NOT_IN_POINT = 'this point did not capture the item'
export const STATUS_LEGACY_NOT_RECORDED = 'Item list not recorded for this backup'

/**
 * Safely parse JSON metadata from a restore point.
 * @param {string|object|null|undefined} meta
 * @returns {object}
 */
export function parsePointMetadata(meta) {
  if (!meta) return {}
  if (typeof meta === 'object') return meta
  try {
    return JSON.parse(meta)
  } catch {
    return {}
  }
}

/**
 * Extract item membership from restore point metadata.
 * Mirrors the wizard and backend rule: item_sizes or item_manifests define membership.
 * If neither exists or both are empty, membership is unknown (legacy point).
 *
 * @param {object} point
 * @returns {{ known: boolean, itemNames: Set<string> }}
 */
export function getPointMembership(point) {
  const meta = parsePointMetadata(point?.metadata)
  const sizes = meta.item_sizes && typeof meta.item_sizes === 'object' ? Object.keys(meta.item_sizes) : []
  const manifests = meta.item_manifests && typeof meta.item_manifests === 'object' ? Object.keys(meta.item_manifests) : []
  const itemNames = new Set([...sizes, ...manifests])

  return {
    known: itemNames.size > 0,
    itemNames,
  }
}

/**
 * Checks item coverage for a single restore point against a set of selected items.
 *
 * @param {object} point restore point (with jobId or job_id, metadata, etc.)
 * @param {Array<object>} selectedItems items selected in Step 1
 * @returns {{
 *   pointId: number|string,
 *   jobId: number|string,
 *   isLegacy: boolean,
 *   coveredItems: Array<object>,
 *   coveredNames: Array<string>,
 *   missingItems: Array<{ item: object, name: string, reason: string, reasonCode: string }>,
 *   coverageCount: number,
 *   totalSelected: number,
 *   coversAll: boolean
 * }}
 */
export function getPointCoverage(point, selectedItems = []) {
  const pointJobId = point?.jobId ?? point?.job_id
  const { known, itemNames } = getPointMembership(point)
  const isLegacy = !known

  const coveredItems = []
  const coveredNames = []
  const missingItems = []

  for (const item of selectedItems) {
    const itemName = item.name || item.item_name
    // Check if the item belongs to the job that produced this restore point
    const inJob = Array.isArray(item.jobs) && item.jobs.some(j => (j.id ?? j) === pointJobId)

    if (!inJob) {
      missingItems.push({
        item,
        name: itemName,
        reason: REASON_NOT_IN_JOB,
        reasonCode: 'not_in_job',
      })
      continue
    }

    if (isLegacy) {
      // Legacy restore point with unknown membership: permissive fallback for items in its job
      coveredItems.push(item)
      coveredNames.push(itemName)
    } else if (itemNames.has(itemName)) {
      coveredItems.push(item)
      coveredNames.push(itemName)
    } else {
      missingItems.push({
        item,
        name: itemName,
        reason: REASON_NOT_IN_POINT,
        reasonCode: 'not_in_point',
      })
    }
  }

  return {
    pointId: point?.id,
    jobId: pointJobId,
    isLegacy,
    coveredItems,
    coveredNames,
    missingItems,
    coverageCount: coveredItems.length,
    totalSelected: selectedItems.length,
    coversAll: coveredItems.length === selectedItems.length && selectedItems.length > 0,
  }
}

/**
 * Normalises chosen points input into a Map<jobId, point>.
 * @param {Map<any, any>|Array<object>|object} chosenPoints
 * @returns {Map<number|string, object>}
 */
function normalizeChosenPoints(chosenPoints) {
  if (chosenPoints instanceof Map) {
    return chosenPoints
  }
  const map = new Map()
  if (Array.isArray(chosenPoints)) {
    for (const p of chosenPoints) {
      if (p) {
        const jid = p.jobId ?? p.job_id
        if (jid != null) map.set(jid, p)
      }
    }
  } else if (chosenPoints && typeof chosenPoints === 'object') {
    for (const [k, v] of Object.entries(chosenPoints)) {
      if (v) {
        const jid = v.jobId ?? v.job_id ?? Number(k)
        map.set(jid, v)
      }
    }
  }
  return map
}

/**
 * Generates the default unique map key for a backup item based on type and name.
 *
 * @param {object} item
 * @returns {string}
 */
export function defaultItemKey(item) {
  const type = item?.type || item?.item_type || ''
  const name = item?.name || item?.item_name || ''
  return `${type}:${name}`
}

/**
 * Assign each selected item to exactly one chosen point.
 * If several chosen points cover the item, assign it to the newest point (by created_at).
 *
 * @param {Array<object>} selectedItems
 * @param {Map<any, any>|Array<object>|object} chosenPoints
 * @param {Function} [keyFn]
 * @returns {{
 *   assignments: Map<string, { item: object, point: object, jobId: number|string }>,
 *   uncovered: Array<object>,
 *   isComplete: boolean,
 *   units: Array<{ jobId: number|string, jobName: string, point: object, items: Array<object> }>
 * }}
 */
export function assignItemsToChosenPoints(
  selectedItems = [],
  chosenPoints = new Map(),
  keyFn = defaultItemKey
) {
  const pointsMap = normalizeChosenPoints(chosenPoints)
  const assignments = new Map() // key: itemKey, value: { item, point, jobId }
  const uncovered = []

  for (const item of selectedItems) {
    const itemKey = typeof keyFn === 'function' ? keyFn(item) : defaultItemKey(item)

    // Find all chosen points covering this item
    const eligible = []
    for (const [jobId, point] of pointsMap.entries()) {
      const coverage = getPointCoverage(point, [item])
      if (coverage.coverageCount > 0) {
        eligible.push({ jobId, point })
      }
    }

    if (eligible.length === 0) {
      uncovered.push(item)
    } else {
      // Pick the newest point
      eligible.sort((a, b) => {
        const timeA = new Date(a.point.created_at || 0).getTime()
        const timeB = new Date(b.point.created_at || 0).getTime()
        return timeB - timeA
      })
      const chosen = eligible[0]
      assignments.set(itemKey, {
        item,
        point: chosen.point,
        jobId: chosen.jobId,
      })
    }
  }

  // Build units for each chosen point that has assigned items
  const unitsByJobId = new Map()
  for (const [, { item, point, jobId }] of assignments.entries()) {
    if (!unitsByJobId.has(jobId)) {
      unitsByJobId.set(jobId, {
        jobId,
        jobName: point.jobName || point.job_name || `Job #${jobId}`,
        point,
        items: [],
      })
    }
    unitsByJobId.get(jobId).items.push(item)
  }

  const units = Array.from(unitsByJobId.values())
  const isComplete = selectedItems.length > 0 && uncovered.length === 0

  return {
    assignments,
    uncovered,
    isComplete,
    units,
  }
}

/**
 * Builds restore units from the assignments.
 *
 * @param {Array<object>} selectedItems
 * @param {Map<any, any>|Array<object>|object} chosenPoints
 * @returns {Array<{ jobId: number|string, jobName: string, point: object, items: Array<object> }>}
 */
export function buildRestoreUnits(selectedItems = [], chosenPoints = new Map()) {
  return assignItemsToChosenPoints(selectedItems, chosenPoints).units
}

/**
 * Checks if the plan covers all selected items.
 *
 * @param {Array<object>} selectedItems
 * @param {Map<any, any>|Array<object>|object} chosenPoints
 * @returns {boolean}
 */
export function isPlanComplete(selectedItems = [], chosenPoints = new Map()) {
  return assignItemsToChosenPoints(selectedItems, chosenPoints).isComplete
}
