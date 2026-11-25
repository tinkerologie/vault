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

const WRITE_TARGET_LABELS = {
  volume: (t) => `Volume ${t.mount || ''}`.trim(),
  file: (t) => `File ${t.mount || ''}`.trim(),
  template: () => 'Unraid template',
  disk: (t) => `Disk ${t.mount || ''}`.trim(),
  nvram: () => 'NVRAM',
}

const UNRESOLVED_REASONS = {
  backup_metadata_locked: 'Enter the encryption passphrase to see the exact paths.',
  passphrase_required: 'Enter the encryption passphrase to see the exact paths.',
  vm_host_unavailable: 'Exact paths could not be checked: the VM host is unavailable.',
  storage_unavailable: 'Exact paths could not be checked: the backup storage is unavailable.',
}

/**
 * The exact host paths the server resolved for one container or VM restore
 * (write-targets `items[].write_targets`, #716). `effect` is `clearable` for a
 * volume that clearing empties first, `merge` for a volume too shallow to
 * clear, and `overwrite` for a file replaced with the backed-up copy.
 *
 * @param {{ status?: string, reason?: string, write_targets?: { kind: string, path?: string, mount?: string, name?: string, clearable?: boolean }[] }} preview
 */
function serverTargets(preview, fallback) {
  if (preview.status !== 'resolved') {
    return { ...fallback, unresolved: UNRESOLVED_REASONS[preview.reason] || 'Exact paths could not be checked before the restore.' }
  }
  const targets = preview.write_targets || []
  const paths = targets.filter((t) => t.path && WRITE_TARGET_LABELS[t.kind]).map((t) => ({
    path: t.path,
    label: WRITE_TARGET_LABELS[t.kind](t),
    effect: t.kind === 'volume' ? (t.clearable ? 'clearable' : 'merge') : 'overwrite',
  }))
  const domain = targets.find((t) => t.kind === 'domain')?.name
  return {
    path: null,
    description: preview.type === 'vm' ? 'Exact host paths from the backed-up VM' : 'Exact host paths from the saved configuration',
    paths,
    ...(domain ? { domain } : {}),
  }
}

/**
 * Where one item is restored to. An exact path when the wizard knows it; when
 * the path is only read from the backup at restore time, `path` is null and
 * `description` says so rather than guessing (#700). For a container or VM,
 * a resolved write-targets preview supplies the exact `paths` (#716).
 *
 * @param {{ type?: string, item_type?: string, name?: string, item_name?: string, settings?: any }} item
 * @param {string} [customDestination] the unit's custom destination, if chosen
 * @param {object} [preview] the server's write targets for this item
 * @returns {{ path: string | null, description: string, paths?: { path: string, label: string, effect: string }[], domain?: string, unresolved?: string }}
 */
export function restoreTargetFor(item, customDestination = '', preview = null) {
  const type = effectiveItemType(item)
  const custom = (customDestination || '').trim()
  if (preview && (type === 'container' || type === 'vm')) {
    const fallback = custom
      ? { path: custom, description: 'Custom destination' }
      : { path: null, description: UNRECORDED_TARGETS[type] }
    return serverTargets({ type, ...preview }, fallback)
  }
  if (custom) return { path: custom, description: 'Custom destination' }
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
    source: units.flatMap((u) => u.targets.flatMap((t) => {
      const where = `${u.jobName}, ${u.pointDate}`
      if (!t.paths) return [{ label: `${t.name} (${t.typeLabel}), ${where}`, value: t.path || t.description }]
      return [
        ...t.paths.map((p) => ({ label: `${t.name} (${t.typeLabel}), ${where}: ${p.label}`, value: p.path })),
        ...(t.domain ? [{ label: `${t.name} (${t.typeLabel}), ${where}: Domain definition`, value: t.domain }] : []),
      ]
    })),
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
