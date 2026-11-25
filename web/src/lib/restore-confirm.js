// What the restore wizard's typed confirmations say (#700, #704). Plain JS so
// the wording is unit-tested under the node Vitest project.

import { effectiveItemType, parseConfig } from './utils.js'

function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`
}

const UNRECORDED_TARGETS = {
  container: "Each volume's original host path, read from the container's saved configuration",
  vm: 'The original disk image, domain XML and NVRAM paths, read from the backup',
  folder: 'The original folder path, read from the backup',
  flash: 'The original flash drive path, read from the backup',
}

/**
 * Where one item is restored to. An exact path when the wizard knows it; when
 * the path is only read from the backup at restore time, `path` is null and
 * `description` says so rather than guessing (#700).
 *
 * @param {{ type?: string, item_type?: string, name?: string, item_name?: string, settings?: any }} item
 * @param {string} [customDestination] the unit's custom destination, if chosen
 * @returns {{ path: string | null, description: string }}
 */
export function restoreTargetFor(item, customDestination = '') {
  const custom = (customDestination || '').trim()
  if (custom) return { path: custom, description: 'Custom destination' }
  const type = effectiveItemType(item)
  const name = item?.name || item?.item_name || ''
  const settings = parseConfig(item?.settings) || {}
  if ((type === 'folder' || type === 'flash') && typeof settings.path === 'string' && settings.path) {
    return { path: settings.path, description: 'Original location' }
  }
  if (type === 'plugin' && name) return { path: `/boot/config/plugins/${name}`, description: 'Original location' }
  if (type === 'zfs') return { path: settings.dataset || name, description: 'Original dataset' }
  return { path: null, description: UNRECORDED_TARGETS[type] || 'The original location, read from the backup' }
}

/**
 * The typed confirmation for a restore that clears its destination first.
 *
 * @param {{ jobName: string, pointDate: string, targets: { name: string, typeLabel: string, path: string | null, description: string }[] }[]} units
 *   only the units that clear their destination
 * @returns {{ source: { label: string, value: string }[], consequences: string[] }}
 */
export function clearingRestoreSummary(units) {
  return {
    source: units.flatMap((u) => u.targets.map((t) => ({
      label: `${t.name} (${t.typeLabel}), ${u.jobName}, ${u.pointDate}`,
      value: t.path || t.description,
    }))),
    consequences: [
      'Files at these locations that are not in the backup are deleted.',
      'Files that are in the backup are overwritten with the backed-up copy.',
      'This cannot be undone from Vault.',
    ],
  }
}

/**
 * The typed confirmation for deleting one Restore Point from the wizard.
 *
 * @param {{ jobName?: string, jobId?: number, job_id?: number, backup_type?: string, retention_preserved_for?: number }} point
 * @param {string} pointDate the point's formatted date
 * @returns {{ source: { label: string, value: string }[], consequences: string[] }}
 */
export function deleteRestorePointSummary(point, pointDate) {
  const dependents = point.retention_preserved_for || 0
  return {
    source: [
      { label: 'Job', value: point.jobName || `Job #${point.jobId ?? point.job_id}` },
      { label: 'Restore point', value: point.backup_type ? `${pointDate} (${point.backup_type})` : pointDate },
    ],
    consequences: [
      ...(dependents > 0 ? [`${plural(dependents, 'newer restore point')} depend${dependents === 1 ? 's' : ''} on it and can no longer be restored.`] : []),
      'Its backup files are deleted from storage.',
      'This cannot be undone.',
    ],
  }
}
