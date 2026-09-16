<script>
  import { onMount } from 'svelte'
  import { SvelteMap, SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { getProgress, restoreFromStatus } from '../lib/progress.svelte.js'
  import { isRestoreActive } from '../lib/restore-sync.js'
  import { formatDate, formatBytes, itemDisplayLabel, itemTypeIcon, itemTypeColor, itemTypeLabel, effectiveItemType, commonItemType } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import Spinner from './Spinner.svelte'
  import RestorePointTimeline from './RestorePointTimeline.svelte'
  import Tooltip from './Tooltip.svelte'

  let { jobs = [], onrestore = () => {}, initialJobId = null, initialType = null, initialName = null } = $props()

  let step = $state(1)
  let selectedItems = $state(new SvelteMap()) // key: "type:name", value: item object
  let autoSelectApplied = false // deep-link auto-select is one-time per mount
  let selectedPoint = $state(null)
  let restoreDestination = $state('')
  let showDestOverride = $state(false)
  // Clear the destination before extracting, rather than merging the backup
  // over whatever is already there (issue #321). Default on, because the
  // warning below has always promised exactly that.
  let cleanDestination = $state(true)
  // A custom-destination container restore stops, removes, and recreates the
  // live container and rewrites its volume mappings. That happened with no
  // warning at all; it is now opt-in per restore (issue #336).
  let acknowledgeContainerRemap = $state(false)
  let passphrase = $state('')
  let loading = $state(false)
  let allItems = $state([])
  let restorePoints = $state([])
  let loadingPoints = $state(false)
  let typeFilter = $state('all')
  let deletingRpId = $state(null)
  let confirmDeleteRpId = $state(null)

  // Restore progress and live logs
  const progress = getProgress()
  let restoring = $state(false)
  let restoringJobId = $state(null)
  let restoreOutcome = $state(null)
  let restoreLogs = $state([])
  let restoreLogsRunId = $state(null)
  let logContainerEl = $state(null)

  let isRestoreRunning = $derived(
    restoring ||
    (progress.running && progress.activeRun?.run_type === 'restore' && (!restoringJobId || progress.activeRun.job_id === restoringJobId))
  )

  function scrollToLogBottom() {
    if (logContainerEl) {
      setTimeout(() => {
        if (logContainerEl) {
          logContainerEl.scrollTop = logContainerEl.scrollHeight
        }
      }, 50)
    }
  }

  function timeOnly(ts) {
    if (!ts) return ''
    const d = new Date(ts)
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  function resetWizard() {
    step = 1
    selectedItems.clear()
    selectedPoint = null
    restoreOutcome = null
    restoreLogs = []
    restoreLogsRunId = null
  }

  async function reconcileRestoring() {
    try {
      const status = await api.getRunnerStatus()
      restoreFromStatus(status)
      if (status?.active && status.run_type === 'restore') {
        restoring = true
        restoringJobId = status.job_id
        if (status.run_id && status.run_id !== restoreLogsRunId) {
          restoreLogsRunId = status.run_id
          api.getRunLogs(status.run_id, { limit: 500, tail: true }).then(res => {
            if (res?.entries) {
              restoreLogs = res.entries
              scrollToLogBottom()
            }
          }).catch(() => {})
        }
      } else if (restoring && !isRestoreActive(status, restoringJobId)) {
        restoring = false
      }
    } catch {
      // Leaves state unchanged on any API error, so a transient failure does not
      // incorrectly re-enable the button.
    }
  }

  // Pre-flight: cheap go/no-go checks run before a restore. The result is only
  // trusted while the inputs that produced it are unchanged (preflightFresh):
  // change the passphrase or destination and the gate re-arms automatically.
  let preflightResult = $state(null)
  let preflightRunning = $state(false)
  let preflightSig = $state('')

  // Signature of the inputs that affect a restore's pre-flight outcome.
  let restoreSig = $derived(JSON.stringify({
    p: passphrase,
    o: showDestOverride,
    d: showDestOverride ? restoreDestination : '',
  }))
  // True when the current result reflects the current inputs.
  let preflightFresh = $derived(preflightResult != null && preflightSig === restoreSig)

  async function runPreflight() {
    if (!selectedPoint) return
    const sigAtRun = restoreSig
    preflightRunning = true
    preflightResult = null
    try {
      const payload = {}
      if (showDestOverride && restoreDestination.trim()) payload.destination = restoreDestination.trim()
      if (passphrase) payload.passphrase = passphrase
      preflightResult = await api.preflightRestore(selectedPoint.jobId, selectedPoint.id, payload)
    } catch (e) {
      preflightResult = { ok: false, checks: [{ id: 'error', label: 'Pre-flight could not run', status: 'fail', detail: e.message || 'request failed' }] }
    } finally {
      preflightSig = sigAtRun
      preflightRunning = false
    }
  }

  // Partial-restore file picker (Feature B).
  // Per-item map: itemKey -> { contents: TarIndex|null, selected: SvelteSet<string>,
  //                             loading: boolean, error: string, search: string, open: boolean }
  let picker = $state(new SvelteMap())

  function ensurePickerEntry(item) {
    const key = itemKey(item)
    if (!picker.has(key)) {
      picker.set(key, {
        contents: null,
        selected: new SvelteSet(),
        loading: false,
        error: '',
        search: '',
        open: false,
      })
    }
    return picker.get(key)
  }

  // updateEntry replaces the picker entry with a shallow clone + patch.
  // SvelteMap tracks set(); mutating a value in place after an `await`
  // boundary does NOT propagate (Svelte 5 only sees the synchronous
  // mutation). We always go through this helper to keep that contract.
  // The `selected` SvelteSet is preserved across clones so toggleFilePicked
  // / clearPickerSelection don't lose their reactive backing.
  function updateEntry(item, patch) {
    const key = itemKey(item)
    const cur = ensurePickerEntry(item)
    picker.set(key, { ...cur, ...patch })
  }

  // Partial (per-file) restore only exists for item types whose backups are
  // tar archives with an index sidecar. VM and ZFS backups are whole-image
  // artefacts, so offering the picker would just 404 on the missing sidecar.
  function supportsFilePicker(type) {
    return type === 'container' || type === 'folder' || type === 'plugin'
  }

  async function togglePickerOpen(item) {
    const cur = ensurePickerEntry(item)
    const willOpen = !cur.open
    updateEntry(item, { open: willOpen })
    if (willOpen && !cur.contents && !cur.loading) {
      updateEntry(item, { loading: true, error: '' })
      try {
        const contents = await api.getRestorePointContents(selectedPoint.jobId, selectedPoint.id, item.name)
        updateEntry(item, { contents, loading: false })
      } catch (e) {
        updateEntry(item, { error: e?.message || 'failed to load file list', loading: false })
      }
    }
  }

  function toggleFilePicked(item, filePath) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry) return
    if (entry.selected.has(filePath)) entry.selected.delete(filePath)
    else entry.selected.add(filePath)
    // SvelteSet is reactive on add/delete; touch the entry too so the
    // summary "X of Y selected" counter rerenders.
    updateEntry(item, {})
  }

  function selectAllFiltered(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry?.contents) return
    for (const f of filteredFiles(entry)) entry.selected.add(f.path)
    updateEntry(item, {})
  }

  function clearPickerSelection(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry) return
    entry.selected.clear()
    updateEntry(item, {})
  }

  function filteredFiles(entry) {
    if (!entry?.contents?.files) return []
    const q = entry.search.trim().toLowerCase()
    if (!q) return entry.contents.files
    return entry.contents.files.filter(f => f.path.toLowerCase().includes(q))
  }

  onMount(() => {
    reconcileRestoring()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_started') {
        if (msg.run_type === 'restore' && (!restoringJobId || msg.job_id === restoringJobId)) {
          restoring = true
          restoreOutcome = null
          if (msg.run_id) {
            restoreLogsRunId = msg.run_id
            restoreLogs = []
          }
        }
      } else if (msg.type === 'job_run_completed') {
        if (msg.run_type === 'restore' && (!restoringJobId || msg.job_id === restoringJobId)) {
          restoring = false
          restoreOutcome = {
            status: msg.status,
            done: msg.items_done || 0,
            total: msg.items_total || 0,
            failed: msg.items_failed || 0,
            sizeBytes: msg.size_bytes || 0,
          }
          if (restoreLogsRunId) {
            api.getRunLogs(restoreLogsRunId, { limit: 500, tail: true }).then(res => {
              if (res?.entries) {
                restoreLogs = res.entries
                scrollToLogBottom()
              }
            }).catch(() => {})
          }
        }
      } else if (msg.type === 'runner_status_snapshot') {
        if (!isRestoreActive(msg.status, restoringJobId)) {
          restoring = false
        }
      } else if (msg.type === 'run_log' && msg.entry) {
        if (restoreLogsRunId && msg.entry.run_id === restoreLogsRunId) {
          if (!restoreLogs.some(e => e.id === msg.entry.id)) {
            restoreLogs = [...restoreLogs, msg.entry]
            scrollToLogBottom()
          }
        }
      }
    })
    return unsub
  })

  $effect(() => {
    const run = progress.activeRun
    if (run && run.run_type === 'restore' && run.run_id && run.run_id !== restoreLogsRunId) {
      restoreLogsRunId = run.run_id
      api.getRunLogs(run.run_id, { limit: 500, tail: true }).then(res => {
        if (res?.entries) {
          restoreLogs = res.entries
          scrollToLogBottom()
        }
      }).catch(() => {})
    }
  })

  function itemKey(item) {
    return `${effectiveItemType(item)}:${itemDisplayLabel(item)}`
  }

  // Gather all backed-up items across all jobs
  $effect(() => {
    gatherItems()
  })

  function gatherItems() {
	loading = true
	try {
	  const itemMap = new SvelteMap()
	  for (const detail of jobs) {
		if (!detail?.items) continue
        for (const item of detail.items) {
          const effType = effectiveItemType(item)
          const key = `${effType}:${itemDisplayLabel(item)}`
          if (!itemMap.has(key)) {
            itemMap.set(key, {
              name: item.item_name,
              type: effType,
              item_name: item.item_name,
              item_type: effType,
              item_id: item.item_id,
              settings: item.settings,
              jobs: [],
            })
          }
		  itemMap.get(key).jobs.push(detail)
        }
      }
      allItems = Array.from(itemMap.values())
      // Deep-link auto-select runs once per mount so a later gatherItems refresh
      // (e.g. a WS event) can't re-select items the user has since cleared.
      if (!autoSelectApplied && jobs.length > 0) {
        autoSelectApplied = true
        // Auto-select items from a pre-selected job (e.g. quick restore from Dashboard)
        if (initialJobId) {
          const jid = Number(initialJobId)
          for (const item of allItems) {
            if (item.jobs.some(j => j.id === jid)) {
              selectedItems.set(itemKey(item), item)
            }
          }
        }
        // Auto-select a specific item from a type+name deep-link (Dashboard restore
        // buttons and the command palette). Only matches items that are actually in
        // a backup job, so unknown/never-backed-up names just land on the picker.
        if (initialType && initialName && selectedItems.size === 0) {
          const item = allItems.find(i =>
            `${i.type}:${i.name}` === `${initialType}:${initialName}` ||
            itemKey(i) === `${initialType}:${initialName}` ||
            (initialType === 'folder' && i.type === 'flash' && i.name === initialName)
          )
          if (item) selectedItems.set(itemKey(item), item)
        }
      }
    } catch { /* ignore */ } finally {
      loading = false
    }
  }

  let searchQuery = $state('')
  let sortBy = $state('alpha-asc')

  let filteredItems = $derived.by(() => {
    let items = typeFilter === 'all' ? allItems : allItems.filter(i => i.type === typeFilter)
    if (searchQuery.trim()) {
      const q = searchQuery.trim().toLowerCase()
      items = items.filter(i => {
        const label = itemDisplayLabel(i).toLowerCase()
        const name = (i.name || '').toLowerCase()
        const type = (i.type || '').toLowerCase()
        const jobNames = i.jobs.map(j => j.name || '').join(' ').toLowerCase()
        return label.includes(q) || name.includes(q) || type.includes(q) || jobNames.includes(q)
      })
    }
    const sorted = [...items]
    switch (sortBy) {
      case 'alpha-desc':
        sorted.sort((a, b) => itemDisplayLabel(b).localeCompare(itemDisplayLabel(a), undefined, { sensitivity: 'base' }))
        break
      case 'type':
        sorted.sort((a, b) => a.type.localeCompare(b.type) || itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
      case 'jobs':
        sorted.sort((a, b) => b.jobs.length - a.jobs.length || itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
      case 'alpha-asc':
      default:
        sorted.sort((a, b) => itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
    }
    return sorted
  })

  let typeOptions = $derived.by(() => {
    const types = new Set(allItems.map(i => i.type))
    const canonicalOrder = ['all', 'container', 'vm', 'folder', 'flash', 'plugin', 'zfs']
    return canonicalOrder.filter(t => t === 'all' || types.has(t))
  })

  let selectedCount = $derived(selectedItems.size)

  function toggleItem(item) {
    const key = itemKey(item)
    if (selectedItems.has(key)) {
      selectedItems.delete(key)
    } else {
      selectedItems.set(key, item)
    }
  }

  function isSelected(item) {
    return selectedItems.has(itemKey(item))
  }

  function selectAll() {
    for (const item of filteredItems) {
      selectedItems.set(itemKey(item), item)
    }
  }

  function clearSelection() {
    selectedItems.clear()
  }

  async function proceedToStep2() {
    if (selectedItems.size === 0) return
    step = 2
    loadingPoints = true
    restorePoints = []
    try {
      // Collect all jobs that contain any of the selected items
      const selectedArr = Array.from(selectedItems.values())
      const relevantJobIds = new SvelteSet()
      for (const item of selectedArr) {
        for (const job of item.jobs) {
          relevantJobIds.add(job.id)
        }
      }

      // Fetch restore points from all relevant jobs in parallel
      const jobMap = new Map(Array.from(relevantJobIds).map(id => [id, jobs.find(j => j.id === id)]))
      const pointsByJob = await Promise.all(
        Array.from(relevantJobIds).map(jobId =>
          api.getRestorePoints(jobId).catch(() => []).then(points => ({
            jobId,
            points: points || []
          }))
        )
      )

      // Flatten and enrich restore points
      const allPoints = []
      for (const { jobId, points } of pointsByJob) {
        const job = jobMap.get(jobId)
        for (const p of points) {
          allPoints.push({ ...p, jobName: job?.name, jobId: jobId, encryption: job?.encryption })
        }
      }

      // Only show restore points that actually contain every selected item.
      // A restore point records its membership in metadata.item_sizes /
      // item_manifests; an item added to a job after a backup ran is NOT in
      // that backup, so showing it would be misleading and the restore would
      // fail. Legacy restore points with no recorded membership are kept
      // (unknown → fall back to whole-archive behaviour).
      const selectedNames = Array.from(selectedItems.values()).map(i => i.name)
      const containsSelected = (rp) => {
        if (!selectedArr.every(item => item.jobs.some(j => j.id === rp.jobId))) {
          return false
        }
        const meta = parseMetadata(rp.metadata)
        const names = new Set([
          ...Object.keys(meta.item_sizes || {}),
          ...Object.keys(meta.item_manifests || {}),
        ])
        if (names.size === 0) return true // legacy: membership unknown
        return selectedNames.every(n => names.has(n))
      }

      // Sort by date descending
      restorePoints = allPoints.filter(containsSelected).sort((a, b) => new Date(b.created_at) - new Date(a.created_at))
    } catch { /* ignore */ } finally {
      loadingPoints = false
    }
  }

  function selectPoint(point) {
    selectedPoint = point
    step = 3
    passphrase = ''
    restoreDestination = ''
    showDestOverride = false
    cleanDestination = true
    acknowledgeContainerRemap = false
    preflightResult = null
    picker.clear()
  }

  let needsPassphrase = $derived(selectedPoint?.encryption === 'age')

  // Only a container is stopped, removed, and recreated by a restore, so only
  // a container needs the remap acknowledgement (issue #336).
  let hasContainerItem = $derived(selectedItemsArray.some(item => item.type === 'container'))

  // The acknowledgement is only meaningful while a custom destination is
  // selected with a container in the set; anywhere else it must not block the
  // button, and it resets so re-entering the choice asks again.
  let needsRemapAcknowledgement = $derived(hasContainerItem && showDestOverride)

  // A partial restore writes only the files the user picked, so clearing the
  // destination would delete everything they did not pick. The backend
  // declines to clear in that case; the wizard says so rather than leaving a
  // ticked box that does nothing (issue #321).
  let hasPartialSelection = $derived(
    Array.from(picker.entries()).some(([key, entry]) => {
      const item = selectedItems.get(key)
      return item && entry?.selected && entry.selected.size > 0 && item.jobs?.some(j => j.id === selectedPoint?.jobId)
    })
  )

  function parseMetadata(meta) {
    if (!meta) return {}
    try { return JSON.parse(meta) } catch { return {} }
  }

  /** Calculate the total size of only the selected items from a restore point's metadata. */
  function selectedRestoreSize(rp) {
    const meta = parseMetadata(rp.metadata)
    const itemSizes = meta.item_sizes
    if (!itemSizes) return rp.size_bytes
    const selectedNames = new Set(Array.from(selectedItems.values()).map(i => i.name))
    let total = 0
    for (const [name, size] of Object.entries(itemSizes)) {
      if (selectedNames.has(name)) total += size
    }
    return total || rp.size_bytes
  }

  let selectedItemsArray = $derived(Array.from(selectedItems.values()))

  let step3ItemsArray = $derived(
    selectedPoint
      ? selectedItemsArray.filter(item => item.jobs.some(j => j.id === selectedPoint.jobId))
      : selectedItemsArray
  )
  let step3Count = $derived(step3ItemsArray.length)

  // Overwrite-banner target (issue #205 / E5). Only ever show a concrete path
  // when it's actually known — otherwise say "original location" honestly.
  //   { path }     → show this exact path
  //   { original } → say the restore goes to its original location
  //   null         → show no location clause (multi-item / indeterminate)
  // folder/flash items store their filesystem path as the item name. Containers
  // and VMs have no single persisted source path (appdata can live off
  // /mnt/user; container/VM restores may touch several mounts), so we do NOT
  // fabricate a /mnt/user/appdata path for them.
  let restoreTargetPath = $derived.by(() => {
    if (showDestOverride) {
      const dest = restoreDestination.trim()
      // Blank custom destination → backend falls back to original location.
      return dest ? { path: dest } : { original: true }
    }
    const currentCount = selectedPoint ? step3Count : selectedCount
    if (currentCount !== 1) return null
    const item = selectedPoint ? step3ItemsArray[0] : selectedItemsArray[0]
    if (!item) return null
    if (item.type === 'folder' || item.type === 'flash') return { path: itemDisplayLabel(item) }
    return { original: true }
  })
  // The newest restore point is the recommended one to restore from.
  let recommendedRpId = $derived(restorePoints[0]?.id ?? null)

  function chainDependencies(rp) {
    return Math.max(0, (rp?.chain_depth || 1) - 1)
  }

  function chainHealthTone(rp) {
    if (rp?.chain_status === 'broken') return 'text-danger'
    if (chainDependencies(rp) > 0) return 'text-info'
    return 'text-success'
  }

  function chainHealthLabel(rp) {
    if (!rp) return 'Unknown'
    if (rp.chain_status === 'broken') return 'Broken chain'
    if (chainDependencies(rp) > 0) return `Needs ${chainDependencies(rp)} earlier backup${chainDependencies(rp) === 1 ? '' : 's'}`
    return 'Standalone'
  }

  function retentionPreservedMessage(rp) {
    const count = rp?.retention_preserved_for || 0
    return `Kept because ${count} newer restore point${count === 1 ? '' : 's'} still depend on it.`
  }

  function doRestore() {
    if (selectedItems.size === 0 || !selectedPoint) return
    // Belt and braces alongside the disabled buttons: recreating a live
    // container is not something to fall into (issue #336).
    if (needsRemapAcknowledgement && !acknowledgeContainerRemap) return
    restoring = true
    restoringJobId = selectedPoint.jobId
    restoreOutcome = null
    restoreLogs = []
    restoreLogsRunId = null

    const items = Array.from(new Set(step3ItemsArray.map(item => item.name)))

    const payload = {
      restore_point_id: selectedPoint.id,
      items,
    }
    if (showDestOverride && restoreDestination.trim()) {
      payload.destination = restoreDestination.trim()
    }
    if (passphrase) {
      payload.passphrase = passphrase
    }
    payload.clean_destination = cleanDestination && !hasPartialSelection

    // Feature B: per-item partial restore. Build file_paths map from any
    // picker entries that have a non-empty selection. Items without an
    // active selection are restored in full (legacy behaviour).
    const filePaths = {}
    for (const [key, entry] of picker.entries()) {
      const item = selectedItems.get(key)
      if (item && entry?.selected && entry.selected.size > 0) {
        if (item.jobs.some(j => j.id === selectedPoint.jobId)) {
          filePaths[item.name] = Array.from(entry.selected)
        }
      }
    }
    if (Object.keys(filePaths).length > 0) {
      payload.file_paths = filePaths
    }

    onrestore(selectedPoint.jobId, payload)
  }

  function goBack() {
    if (step === 3) { step = 2; selectedPoint = null }
    else if (step === 2) { step = 1; restorePoints = [] }
  }

  async function deleteRestorePoint(rp) {
    if (confirmDeleteRpId !== rp.id) {
      confirmDeleteRpId = rp.id
      return
    }
    confirmDeleteRpId = null
    deletingRpId = rp.id
    try {
      await api.deleteRestorePoint(rp.job_id, rp.id)
      restorePoints = restorePoints.filter(p => p.id !== rp.id)
    } catch (e) {
      console.error('Failed to delete restore point', e)
    } finally {
      deletingRpId = null
    }
  }
</script>

<div>
  <!-- Step indicator -->
  <div class="flex items-center gap-2 mb-6">
    {#each [{n:1, label:'Select Items'}, {n:2, label:'Choose Version'}, {n:3, label:'Restore'}] as s (s.n)}
      <button type="button" onclick={() => { if (s.n < step) { if (s.n === 1) { step = 1; selectedPoint = null } else if (s.n === 2) { step = 2; selectedPoint = null } } }}
        class="flex items-center gap-2 {s.n <= step ? '' : 'opacity-40'}">
        <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n < step ? 'bg-vault text-white' : s.n === step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
          {#if s.n < step}
            <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
          {:else}
            {s.n}
          {/if}
        </div>
        <span class="text-xs font-medium {s.n === step ? 'text-text' : 'text-text-muted'} hidden sm:inline">{s.label}</span>
      </button>
      {#if s.n < 3}
        <div class="flex-1 h-px {s.n < step ? 'bg-vault' : 'bg-border'}"></div>
      {/if}
    {/each}
  </div>

  <!-- Step 1: What to restore (multi-select) -->
  {#if step === 1}
    {#if loading}
      <Spinner text="Loading backed-up items..." />
    {:else if allItems.length === 0}
      <div class="text-center py-12">
        <div class="mb-3 opacity-30"><svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4"/></svg></div>
        <p class="text-sm text-text-muted">No backed-up items found. Run a backup first.</p>
      </div>
    {:else}
      <!-- Type filter tabs + selection & search controls -->
      <div class="flex flex-col gap-3 mb-4">
        <div class="flex items-center justify-between flex-wrap gap-2">
          <!-- Type filter tabs -->
          <div class="flex items-center gap-1.5 flex-wrap">
            {#each typeOptions as t (t)}
              <button type="button" onclick={() => typeFilter = t}
                class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors flex items-center gap-1.5 {typeFilter === t ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}">
                {#if t !== 'all'}
                  <svg aria-hidden="true" class="w-3.5 h-3.5 {typeFilter === t ? 'text-white' : itemTypeColor(t)}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(t)}/>
                  </svg>
                {/if}
                {itemTypeLabel(t)}
              </button>
            {/each}
          </div>
          <!-- Select All / Clear -->
          <div class="flex items-center gap-2 shrink-0">
            <button type="button" onclick={selectAll}
              class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
              Select All
            </button>
            {#if selectedCount > 0}
              <button type="button" onclick={clearSelection}
                class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
                Clear ({selectedCount})
              </button>
            {/if}
          </div>
        </div>

        <!-- Search and Sort row -->
        <div class="flex items-center justify-between gap-3 flex-wrap sm:flex-nowrap">
          <div class="relative flex-1 min-w-[200px]">
            <svg aria-hidden="true" class="w-4 h-4 text-text-dim absolute left-3 top-1/2 -translate-y-1/2 pointer-events-none" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
            </svg>
            <input
              type="text"
              bind:value={searchQuery}
              placeholder="Search items or jobs..."
              class="w-full pl-9 pr-8 py-1.5 text-xs bg-surface-2 border border-border rounded-lg text-text placeholder-text-dim focus:outline-none focus:border-vault"
            />
            {#if searchQuery}
              <button
                type="button"
                onclick={() => searchQuery = ''}
                class="absolute right-2.5 top-1/2 -translate-y-1/2 text-text-dim hover:text-text"
                title="Clear search"
              >
                <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                </svg>
              </button>
            {/if}
          </div>

          <div class="flex items-center gap-2 shrink-0 text-xs text-text-muted">
            <span>Sort:</span>
            <select
              bind:value={sortBy}
              class="bg-surface-2 border border-border rounded-lg px-2.5 py-1.5 text-xs text-text focus:outline-none focus:border-vault cursor-pointer"
            >
              <option value="alpha-asc">Name (A → Z)</option>
              <option value="alpha-desc">Name (Z → A)</option>
              <option value="type">Type</option>
              <option value="jobs">Most Backed Up</option>
            </select>
          </div>
        </div>
      </div>

      {#if filteredItems.length === 0}
        <div class="text-center py-12 bg-surface-2 border border-border rounded-xl">
          <p class="text-sm text-text-muted">No items match your filter.</p>
        </div>
      {:else}
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2.5">
          {#each filteredItems as item (itemKey(item))}
            {@const selected = isSelected(item)}
            <button type="button" onclick={() => toggleItem(item)}
              class="bg-surface-2 border rounded-xl p-3 text-left hover:shadow-sm transition-all group
                {selected ? 'border-vault ring-1 ring-vault/30 bg-surface-2/90' : 'border-border hover:border-vault/40'}">
              <div class="flex items-center gap-2.5 mb-1.5">
                <!-- Checkbox indicator -->
                <div class="w-4.5 h-4.5 rounded border-2 flex items-center justify-center shrink-0 transition-colors
                  {selected ? 'bg-vault border-vault' : 'border-border group-hover:border-vault/40'}">
                  {#if selected}
                    <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                  {/if}
                </div>
                <div class="w-7 h-7 rounded-lg bg-surface-3 flex items-center justify-center shrink-0 group-hover:bg-vault/10 transition-colors">
                  <svg aria-hidden="true" class="w-4 h-4 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
                </div>
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</p>
                </div>
                <span class="px-1.5 py-0.5 text-[10px] uppercase font-semibold tracking-wider rounded bg-surface-3 shrink-0 {itemTypeColor(item.type)}">
                  {itemTypeLabel(item.type)}
                </span>
              </div>
              <p class="text-[11px] text-text-dim truncate pl-7">In {item.jobs.length} job{item.jobs.length !== 1 ? 's' : ''}: {item.jobs.map(j => j.name).join(', ')}</p>
            </button>
          {/each}
        </div>
      {/if}

      <!-- Selection summary + Floating Next button -->
      <div class="sticky bottom-4 z-20 mt-6 p-3.5 bg-surface-2/95 backdrop-blur-md border border-border rounded-xl shadow-lg flex items-center justify-between transition-all">
        <div class="flex items-center gap-3">
          <div class="w-2.5 h-2.5 rounded-full {selectedCount > 0 ? 'bg-vault animate-pulse' : 'bg-text-dim'}"></div>
          <span class="text-sm font-medium text-text">
            {#if selectedCount > 0}
              <span class="text-vault font-semibold">{selectedCount}</span> item{selectedCount !== 1 ? 's' : ''} selected
            {:else}
              Select items to restore
            {/if}
          </span>
        </div>
        <button type="button" onclick={proceedToStep2} disabled={selectedCount === 0}
          class="px-5 py-2 text-sm font-semibold text-white bg-vault hover:bg-vault-dark rounded-lg transition-all shadow-sm disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2">
          Next
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
        </button>
      </div>
    {/if}

  <!-- Step 2: Which version -->
  {:else if step === 2}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to items
      </button>
      <div class="flex items-center gap-3 mt-2 flex-wrap">
        {#each selectedItemsArray as item (itemKey(item))}
          <div class="flex items-center gap-1.5 px-2.5 py-1 bg-surface-3 rounded-lg min-w-0">
            <svg aria-hidden="true" class="w-4 h-4 shrink-0 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
            <span class="text-xs font-medium text-text truncate max-w-[200px]" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
            <span class="text-xs text-text-dim capitalize shrink-0">({itemTypeLabel(item.type)})</span>
          </div>
        {/each}
      </div>
      <p class="text-sm text-text-muted mt-3">
        Select a restore point to restore from. Each entry represents a saved backup version showing its archive size and estimated restore size. The activity graph highlights backup density across days.
      </p>
    </div>

    {#if loadingPoints}
      <Spinner text="Loading restore points..." />
    {:else if restorePoints.length === 0}
      <div class="text-center py-12">
        <div class="mb-3 opacity-30"><svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/></svg></div>
        <p class="text-sm text-text-muted">No restore points found for the selected items.</p>
      </div>
    {:else}
      <RestorePointTimeline
        points={restorePoints}
        selectedId={selectedPoint?.id ?? null}
        recommendedId={recommendedRpId}
        onSelect={(rp) => { confirmDeleteRpId = null; selectPoint(rp) }}
        onDelete={deleteRestorePoint}
        deletingId={deletingRpId}
        confirmDeleteId={confirmDeleteRpId}
        sizeFor={selectedRestoreSize}
        itemType={commonItemType(selectedItemsArray)}
      />
      <p class="text-xs text-text-dim mt-3 text-center">{restorePoints.length} restore point{restorePoints.length !== 1 ? 's' : ''}</p>
    {/if}

  <!-- Step 3: Restore options -->
  {:else if step === 3}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to versions
      </button>
    </div>

    <!-- Summary card -->
    <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6">
      <h3 class="text-sm font-semibold text-text mb-3">Restore Summary</h3>
      <div class="space-y-2 text-sm">
        <div class="flex justify-between">
          <span class="text-text-muted">Items</span>
          <span class="text-text font-medium">
            {#if step3Count === 1}
              {itemDisplayLabel(step3ItemsArray[0])} ({step3ItemsArray[0].type})
            {:else}
              {step3Count} items
            {/if}
          </span>
        </div>
        {#if step3Count > 1}
          <div class="flex flex-wrap gap-1.5 justify-end">
            {#each step3ItemsArray as item (itemKey(item))}
              <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text-dim truncate max-w-[150px]" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
            {/each}
          </div>
        {/if}
        <div class="flex justify-between">
          <span class="text-text-muted">Restore Point</span>
          <span class="text-text">{formatDate(selectedPoint.created_at)}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Size</span>
          <span class="text-text">{formatBytes(selectedPoint ? selectedRestoreSize(selectedPoint) : 0)}{selectedPoint && selectedRestoreSize(selectedPoint) !== selectedPoint.size_bytes ? ' (selected items)' : ''}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Backup Type</span>
          <span class="text-text uppercase text-xs">{selectedPoint.backup_type}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Chain Health</span>
          <span class="inline-flex items-center gap-1">
            <span class="text-xs font-medium {chainHealthTone(selectedPoint)}">{chainHealthLabel(selectedPoint)}</span>
            {#if selectedPoint?.chain_status !== 'broken' && chainDependencies(selectedPoint) > 0}
              <Tooltip text="Chain length is {selectedPoint.chain_depth}. Restoring replays the base full backup plus intermediate backups in this chain." />
            {/if}
          </span>
        </div>
      </div>
    </div>

    {#if selectedPoint?.chain_status === 'broken'}
      <div class="bg-danger/10 border border-danger/30 rounded-xl p-4 mb-4 flex items-start gap-3">
        <svg aria-hidden="true" class="w-5 h-5 text-danger shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-7.938 4h15.876c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L2.33 16c-.77 1.333.192 3 1.732 3z"/>
        </svg>
        <div>
          <p class="text-sm font-medium text-danger">Restore chain is broken</p>
          <p class="text-xs text-text-muted mt-0.5">{selectedPoint.chain_warning}</p>
        </div>
      </div>
    {:else if chainDependencies(selectedPoint) > 0}
      <div class="bg-info/10 border border-info/30 rounded-xl p-4 mb-4 flex items-start gap-3">
        <svg aria-hidden="true" class="w-5 h-5 text-info shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M12 22C6.477 22 2 17.523 2 12S6.477 2 12 2s10 4.477 10 10-4.477 10-10 10z"/>
        </svg>
        <div>
          <p class="text-sm font-medium text-info">Restore will replay the full chain</p>
          <p class="text-xs text-text-muted mt-0.5">This point depends on {chainDependencies(selectedPoint)} earlier backup{chainDependencies(selectedPoint) === 1 ? '' : 's'} and Vault will stage them before restoring.</p>
          {#if selectedPoint?.base_full_created_at}
            <p class="text-xs text-text-muted mt-0.5">Based on the full backup from {formatDate(selectedPoint.base_full_created_at)}{selectedPoint.base_full_size_bytes ? ` (${formatBytes(selectedPoint.base_full_size_bytes)})` : ''}.</p>
          {/if}
        </div>
      </div>
    {/if}

    {#if selectedPoint?.retention_preserved}
      <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-4 flex items-start gap-3">
        <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
        </svg>
        <div>
          <p class="text-sm font-medium text-warning">Retention is preserving this restore point</p>
          <p class="text-xs text-text-muted mt-0.5">{retentionPreservedMessage(selectedPoint)}</p>
        </div>
      </div>
    {/if}

    <!-- Options -->
    <div class="space-y-5 mb-6">
      <!-- Destination: original (default) or a custom path. -->
      <div>
        <p class="text-sm font-medium text-text-muted mb-2">Restore destination</p>
        <div class="space-y-2">
          <label class="flex items-center gap-2 cursor-pointer text-sm text-text">
            <input type="radio" name="rw_dest" class="accent-vault" checked={!showDestOverride}
              onchange={() => { showDestOverride = false; restoreDestination = ''; acknowledgeContainerRemap = false }} />
            Restore to original location
          </label>
          <label class="flex items-center gap-2 cursor-pointer text-sm text-text">
            <input type="radio" name="rw_dest" class="accent-vault" checked={showDestOverride}
              onchange={() => { showDestOverride = true }} />
            Custom destination
          </label>
        </div>
        {#if showDestOverride}
          <div class="mt-2">
            <PathBrowser bind:value={restoreDestination} label="Custom restore destination" />
            <p class="text-xs text-text-dim mt-1">Files will be written under this path instead of their original location.</p>
          </div>
          {#if hasContainerItem}
            <div class="mt-3 bg-warning/10 border border-warning/30 rounded-xl p-4">
              <p class="text-sm font-medium text-warning">This also changes the live container</p>
              <p class="text-xs text-text-muted mt-1">
                Restoring a container to a custom destination stops it, removes it, and recreates it with its
                volume mappings pointed at the new location. The Unraid template is rewritten to match, so the
                Docker page's Edit form shows the same mappings. The container does not go back to its original
                paths on its own.
              </p>
              <label class="flex items-start gap-2 cursor-pointer text-sm text-text mt-3">
                <input type="checkbox" class="accent-vault mt-0.5" bind:checked={acknowledgeContainerRemap} />
                <span>I understand the live container will be recreated and remapped</span>
              </label>
            </div>
          {/if}
        {/if}
      </div>

      <!-- Replace vs merge (issue #321). -->
      <div>
        <p class="text-sm font-medium text-text-muted mb-2">Existing files at the destination</p>
        <label class="flex items-start gap-2 cursor-pointer text-sm text-text">
          <input type="checkbox" class="accent-vault mt-0.5" bind:checked={cleanDestination}
            disabled={hasPartialSelection} />
          <span>
            Clear the destination first
            <span class="block text-xs text-text-dim mt-0.5">
              {#if hasPartialSelection}
                Not available for a partial restore — clearing the destination would delete the files you did not select.
              {:else if cleanDestination}
                Anything at the destination that is not in this backup is deleted, so the restored result matches the backup exactly.
              {:else}
                The backup is written over the existing files. Anything not in the backup is left where it is.
              {/if}
            </span>
          </span>
        </label>
      </div>

      <!-- Passphrase -->
      {#if needsPassphrase}
        <div>
          <label for="rw_passphrase" class="block text-sm font-medium text-text-muted mb-2">Encryption Passphrase</label>
          <input id="rw_passphrase" type="password" autocomplete="off" bind:value={passphrase}
            placeholder="Enter the passphrase used to encrypt these backups"
            class="w-full sm:w-96 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
          <p class="text-xs text-text-dim mt-1">This backup uses age encryption. A passphrase is required to decrypt.</p>
        </div>
      {/if}
    </div>

    <!-- Per-item file picker (Feature B). Folder/plugin/container-volume
         items now expose a "Restore specific files…" disclosure that
         loads the tar index sidecar and lets the user pick which entries
         to extract. Items with an empty selection restore in full. -->
    <div class="mb-6 space-y-3">
      {#each step3ItemsArray as item (itemKey(item))}
        {@const entry = picker.get(itemKey(item))}
        {@const sel = entry?.selected?.size || 0}
        {@const total = entry?.contents?.files?.length || 0}
        {@const emptyContents = !!entry?.contents && total === 0}
        {#if !supportsFilePicker(item.type)}
          <div class="bg-surface-2 border border-border rounded-xl p-3 text-sm flex items-center justify-between gap-3">
            <span class="flex items-center gap-2 min-w-0">
              <span class="font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
              <span class="text-xs text-text-dim shrink-0">({item.type})</span>
            </span>
            <span class="text-xs text-text-muted shrink-0">
              {item.type === 'vm' ? 'Restored in full (disk images, domain XML, NVRAM)' : 'Restored in full'}
            </span>
          </div>
        {:else}
        <details class="group bg-surface-2 border border-border rounded-xl"
          open={entry?.open || false}>
          <summary class="flex items-center justify-between gap-3 cursor-pointer select-none p-3 text-sm"
            onclick={(e) => { e.preventDefault(); togglePickerOpen(item) }}>
            <span class="flex items-center gap-2 min-w-0">
              <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              <span class="font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
              <span class="text-xs text-text-dim shrink-0">({item.type})</span>
            </span>
            <span class="text-xs text-text-muted">
              {#if sel > 0}
                {sel} of {total} files selected
              {:else if total > 0}
                Restore all {total} files
              {:else if entry?.error}
                <span class="text-danger">{entry.error}</span>
              {:else if entry?.loading}
                Loading…
              {:else if emptyContents}
                No restorable files
              {:else}
                Click to browse contents
              {/if}
            </span>
          </summary>
          {#if entry?.open}
            <div class="border-t border-border p-3 space-y-2">
              {#if entry.loading}
                <p class="text-xs text-text-muted">Loading file list…</p>
              {:else if entry.error}
                <p class="text-xs text-danger">{entry.error}</p>
                <p class="text-xs text-text-muted">This restore point may have been produced before partial restore was added; whole-archive extract will run instead.</p>
              {:else if !entry.contents}
                <p class="text-xs text-text-muted">No contents loaded.</p>
              {:else if emptyContents}
                <!-- Distinct from an empty filter result: this backup captured
                     no restorable file for the item at all, so there is
                     nothing to filter. Most often a container whose volumes
                     were all excluded, anonymous, or virtual. -->
                <p class="text-xs text-text-muted">This backup captured no restorable files for this item.</p>
                {#if item.type === 'container'}
                  <p class="text-xs text-text-dim">Every volume was excluded by the job, anonymous, or a virtual path. Restoring still recreates the container from its saved configuration and image.</p>
                {/if}
              {:else}
                <div class="flex items-center gap-2">
                  <input type="text" placeholder="Filter by path…" bind:value={entry.search}
                    class="flex-1 px-3 py-1.5 bg-surface-3 border border-border rounded-lg text-xs text-text placeholder-text-dim" />
                  <button type="button" onclick={() => selectAllFiltered(item)}
                    class="text-xs px-2 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text">Select all</button>
                  <button type="button" onclick={() => clearPickerSelection(item)}
                    class="text-xs px-2 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text">Clear</button>
                </div>
                <div class="max-h-64 overflow-y-auto border border-border rounded-lg bg-surface-3/30">
                  {#each filteredFiles(entry) as f (f.path)}
                    <label class="flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-surface-3 cursor-pointer">
                      <input type="checkbox" checked={entry.selected.has(f.path)}
                        onchange={() => toggleFilePicked(item, f.path)} class="accent-vault" />
                      <span class="font-mono text-text flex-1 truncate" title={f.path}>{f.path}</span>
                      {#if f.is_dir}
                        <span class="text-text-dim">dir</span>
                      {:else}
                        <span class="text-text-dim">{formatBytes(f.size)}</span>
                      {/if}
                    </label>
                  {/each}
                  {#if filteredFiles(entry).length === 0}
                    <p class="px-3 py-2 text-xs text-text-dim">No files match "{entry.search}"</p>
                  {/if}
                </div>
                {#if sel > 0}
                  <p class="text-xs text-info">Restoring {sel} selected file{sel === 1 ? '' : 's'} only. Clear to restore everything.</p>
                {/if}
              {/if}
            </div>
          {/if}
        </details>
        {/if}
      {/each}
    </div>

    <!-- Warning banner -->
    <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6 flex items-start gap-3">
      <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
      </svg>
      <div>
        <p class="text-sm font-medium text-warning">
          {cleanDestination && !hasPartialSelection ? 'This will delete and replace existing data' : 'This will overwrite existing data'}
        </p>
        <p class="text-xs text-text-muted mt-0.5">Restoring will replace current files for
          {#if selectedCount === 1}
            <strong class="text-text" title={itemDisplayLabel(selectedItemsArray[0])}>{itemDisplayLabel(selectedItemsArray[0])}</strong>
          {:else}
            <strong class="text-text">{selectedCount} selected items</strong>
          {/if}
          {#if restoreTargetPath?.path}
            at <code class="text-text font-mono break-all">{restoreTargetPath.path}</code>
          {:else if restoreTargetPath?.original}
            at its original location
          {/if}
          with the backup version.
          {#if cleanDestination && !hasPartialSelection}
            Everything else already at that location is deleted first.
          {:else}
            Files that are not in the backup are left where they are.
          {/if}
        </p>
      </div>
    </div>

    <!-- Pre-flight checks -->
    <div class="bg-surface-2 border border-border rounded-xl p-4 mb-4">
      <div class="flex items-center justify-between gap-3">
        <div>
          <p class="text-sm font-medium text-text">Pre-flight checks</p>
          <p class="text-xs text-text-dim mt-0.5">Confirm the backup can be restored before starting.</p>
        </div>
        <button type="button" onclick={runPreflight} disabled={preflightRunning || (needsPassphrase && !passphrase)}
          class="text-xs px-3 py-1.5 rounded-lg border border-border text-text-muted hover:text-text hover:border-vault/40 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer inline-flex items-center gap-1.5 shrink-0">
          {#if preflightRunning}
            <svg class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
            Checking…
          {:else}
            {preflightResult && preflightFresh ? 'Re-check' : 'Run checks'}
          {/if}
        </button>
      </div>
      {#if preflightResult && !preflightFresh}
        <p class="mt-3 text-xs text-text-dim">Inputs changed since the last check. Run the checks again.</p>
      {:else if preflightResult}
        <ul class="mt-3 space-y-1.5">
          {#each preflightResult.checks as c (c.id)}
            <li class="flex items-start gap-2 text-xs">
              <span class="mt-0.5 shrink-0 {c.status === 'ok' ? 'text-success' : c.status === 'fail' ? 'text-danger' : c.status === 'warn' ? 'text-warning' : 'text-text-dim'}">
                {#if c.status === 'ok'}
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="passed"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                {:else if c.status === 'fail'}
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="failed"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M6 18L18 6M6 6l12 12"/></svg>
                {:else if c.status === 'warn'}
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="warning"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/></svg>
                {:else}
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="skipped"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18 12H6"/></svg>
                {/if}
              </span>
              <span class="text-text">{c.label}</span>
              {#if c.detail}<span class="text-text-dim">· {c.detail}</span>{/if}
            </li>
          {/each}
        </ul>
        {#if !preflightResult.ok}
          <p class="text-xs text-danger mt-2">Resolve the failing checks above, then re-check before restoring.</p>
        {/if}
      {/if}
    </div>

    <!-- Restore -->
    <div class="flex items-center gap-4">
      <button type="button" onclick={doRestore}
        disabled={isRestoreRunning || selectedPoint?.chain_status === 'broken' || (needsPassphrase && !passphrase) || (needsRemapAcknowledgement && !acknowledgeContainerRemap) || !(preflightResult?.ok && preflightFresh)}
        class="w-full sm:w-auto px-6 py-2.5 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2 cursor-pointer">
        {#if isRestoreRunning}
          <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
          Restoring...
        {:else}
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
          Start Restore
        {/if}
      </button>
      {#if preflightResult && preflightFresh && !preflightResult.ok && !isRestoreRunning && selectedPoint?.chain_status !== 'broken'}
        <button type="button"
          disabled={needsRemapAcknowledgement && !acknowledgeContainerRemap}
          onclick={() => { if (window.confirm('Pre-flight checks did not all pass. Restore anyway?')) doRestore() }}
          class="text-xs text-text-dim hover:text-text underline cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:no-underline">Restore anyway</button>
      {:else if !isRestoreRunning && !(preflightResult && preflightFresh) && selectedPoint?.chain_status !== 'broken'}
        <p class="text-xs text-text-dim">Run the pre-flight checks above to enable Start Restore.</p>
      {/if}
    </div>

    <!-- Live Restore Progress Bar -->
    {#if isRestoreRunning}
      {@const progressItems = Object.entries(progress.itemProgress)}
      {@const activeItemPct = progressItems.reduce((maxPct, [, info]) => info.status === 'running' ? Math.max(maxPct, info.percent || 0) : maxPct, 0)}
      {@const overallPct = progress.overallTotal > 0 ? Math.min(100, Math.round((((progress.overallDone + progress.overallFailed) + (activeItemPct / 100)) / progress.overallTotal) * 100)) : activeItemPct}
      {@const elapsedStr = progress.elapsedSec >= 3600 ? `${Math.floor(progress.elapsedSec / 3600)}h ${Math.floor((progress.elapsedSec % 3600) / 60)}m` : progress.elapsedSec >= 60 ? `${Math.floor(progress.elapsedSec / 60)}m ${progress.elapsedSec % 60}s` : `${progress.elapsedSec}s`}

      <div class="bg-surface-2 border border-vault/30 rounded-xl p-4 mt-5" role="status" aria-live="polite">
        <div class="flex items-center gap-2 mb-3">
          <div class="w-2.5 h-2.5 rounded-full bg-vault animate-pulse shrink-0"></div>
          <span class="text-xs font-semibold uppercase tracking-wider text-text-muted">Restore in progress</span>
          {#if progress.activeRun?.job_name}
            <span class="ml-auto text-xs px-2 py-0.5 rounded-full bg-vault/15 text-vault font-medium truncate max-w-[45%]">{progress.activeRun.job_name}</span>
          {/if}
        </div>
        <div class="flex items-center justify-between text-xs text-text-muted mb-1.5">
          <span>Overall progress</span>
          <span class="font-mono text-text-dim tabular-nums font-medium">{overallPct}%</span>
        </div>
        <div class="w-full h-2.5 bg-surface-4 rounded-full overflow-hidden">
          <div class="h-full rounded-full transition-all duration-300 {overallPct < 100 ? 'shimmer-bar' : 'bg-vault'}" style="width: {overallPct}%"></div>
        </div>
        <div class="flex items-center justify-between text-xs text-text-dim mt-2 tabular-nums">
          <span>{progress.overallDone}/{progress.overallTotal} items · {elapsedStr}</span>
          {#if progress.overallFailed > 0}
            <span class="text-danger font-medium">{progress.overallFailed} failed</span>
          {/if}
        </div>
        {#if progress.currentItem}
          <p class="text-xs text-text-dim mt-2 truncate">
            Restoring: <span class="text-text font-medium">{progress.currentItem.name}</span>
            {#if progress.currentItem.item_type} <span class="text-text-muted">({progress.currentItem.item_type})</span>{/if}
          </p>
        {/if}
        {#if progress.phaseMessage}
          <p class="text-xs text-warning animate-pulse mt-1.5">{progress.phaseMessage}</p>
        {/if}
      </div>
    {/if}

    <!-- Outcome Banner -->
    {#if restoreOutcome}
      <div class="mt-5 p-4 rounded-xl border flex items-start justify-between gap-3
        {restoreOutcome.status === 'completed' ? 'bg-success/10 border-success/30' : restoreOutcome.status === 'partial' ? 'bg-warning/10 border-warning/30' : 'bg-danger/10 border-danger/30'}">
        <div class="flex items-start gap-3">
          {#if restoreOutcome.status === 'completed'}
            <svg aria-hidden="true" class="w-5 h-5 text-success shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-success">Restore completed successfully</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.done}/{restoreOutcome.total} items restored{restoreOutcome.sizeBytes ? ` · ${formatBytes(restoreOutcome.sizeBytes)}` : ''}</p>
            </div>
          {:else if restoreOutcome.status === 'partial'}
            <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-warning">Restore completed with errors</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.done}/{restoreOutcome.total} items restored, {restoreOutcome.failed} failed{restoreOutcome.sizeBytes ? ` · ${formatBytes(restoreOutcome.sizeBytes)}` : ''}</p>
            </div>
          {:else}
            <svg aria-hidden="true" class="w-5 h-5 text-danger shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-danger">Restore failed</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.failed} of {restoreOutcome.total} items failed to restore</p>
            </div>
          {/if}
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <button type="button" onclick={resetWizard}
            class="text-xs px-3 py-1.5 rounded-lg bg-surface-3 hover:bg-surface-4 text-text font-medium transition-colors cursor-pointer">
            Done
          </button>
          <button type="button" onclick={() => { restoreOutcome = null }}
            title="Dismiss notification" aria-label="Dismiss notification"
            class="p-1 text-text-dim hover:text-text rounded transition-colors cursor-pointer">
            <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
          </button>
        </div>
      </div>
    {/if}

    <!-- Live Restore Logs -->
    {#if restoreLogs.length > 0}
      <div class="bg-surface-2 border border-border rounded-xl p-4 mt-4">
        <div class="flex items-center justify-between mb-2.5">
          <div class="flex items-center gap-2">
            <svg aria-hidden="true" class="w-4 h-4 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/>
            </svg>
            <span class="text-xs font-semibold text-text">Restore Log</span>
            <span class="text-[10px] px-1.5 py-0.5 rounded-full bg-surface-3 text-text-dim font-medium tabular-nums">{restoreLogs.length}</span>
          </div>
          {#if isRestoreRunning}
            <span class="flex items-center gap-1.5 text-[11px] text-vault font-medium">
              <span class="w-1.5 h-1.5 rounded-full bg-vault animate-ping"></span>
              Live
            </span>
          {/if}
        </div>
        <div bind:this={logContainerEl} class="max-h-56 overflow-y-auto bg-surface-1 border border-border rounded-lg p-3 font-mono text-xs space-y-1.5 select-text">
          {#each restoreLogs as entry (entry.id)}
            <div class="flex items-baseline gap-2">
              <span class="text-text-dim text-[10px] shrink-0 tabular-nums">{timeOnly(entry.ts)}</span>
              <span class="text-[10px] px-1 py-0.5 rounded uppercase font-semibold shrink-0 {entry.level === 'error' ? 'bg-danger/20 text-danger' : entry.level === 'warn' ? 'bg-warning/20 text-warning' : 'bg-surface-3 text-text-dim'}">{entry.level}</span>
              <span class="text-text-muted break-all flex-1 {entry.level === 'error' ? 'text-danger' : ''}">{entry.message}</span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</div>
