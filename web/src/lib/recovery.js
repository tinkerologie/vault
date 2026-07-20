export function recoveryCounts({ plan, containers, vms, folders, available, enabled }) {
  const planItems = (plan?.steps || []).flatMap(step => step.items || [])
  const protectedKeys = new Set(
    planItems.filter(item => item.has_restore_point).map(item => `${item.type}:${item.name}`)
  )

  const countType = (type, liveItems, isAvailable, isEnabled) => {
    if (!isEnabled) return { total: 0, unprotected: 0 }
    const source = isAvailable ? liveItems : planItems.filter(item => item.type === type)
    const items = [...new Map(source.map(item => [item.name, item])).values()]
    return {
      total: items.length,
      unprotected: items.filter(item => !protectedKeys.has(`${type}:${item.name}`)).length,
    }
  }

  const containerCounts = countType('container', containers, available.containers, enabled.containers)
  const vmCounts = countType('vm', vms, available.vms, enabled.vms)

  let folderItems
  if (!enabled.folders && !enabled.flash) {
	folderItems = []
  } else {
	// Custom folders exist only in the persisted recovery plan; live folder
	// discovery exposes well-known presets such as Flash Drive. Keep configured
	// custom paths, but when discovery succeeds let it be authoritative for
	// presets so a removed flash device is not retained as a live item.
	const planFolders = planItems.filter(item => item.type === 'folder' && item.preset !== 'flash' && enabled.folders)
	if (!available.folders && enabled.flash) {
	  planFolders.push(...planItems.filter(item => item.type === 'folder' && item.preset === 'flash'))
	}
	const byName = new Map(planFolders.map(item => [item.name, item]))
	if (available.folders) {
	  for (const item of folders) {
		const included = item.settings?.preset === 'flash' ? enabled.flash : enabled.folders
		if (included && !byName.has(item.name)) byName.set(item.name, item)
	  }
	}
	folderItems = [...byName.values()]
  }
  const folderCounts = {
    total: folderItems.length,
    unprotected: folderItems.filter(item => !protectedKeys.has(`folder:${item.name}`)).length,
  }

  return {
    totalItems: containerCounts.total + vmCounts.total + folderCounts.total,
    totalUnprotected: containerCounts.unprotected + vmCounts.unprotected + folderCounts.unprotected,
  }
}
