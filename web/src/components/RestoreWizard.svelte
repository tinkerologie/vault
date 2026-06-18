<script>
  import { onMount } from 'svelte'
  import { SvelteMap, SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { formatDate, formatBytes } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import Spinner from './Spinner.svelte'
  import RestorePointTimeline from './RestorePointTimeline.svelte'

  let { jobs = [], onrestore = () => {}, initialJobId = null } = $props()

  let step = $state(1)
  let selectedItems = $state(new SvelteMap()) // key: "type:name", value: item object
  let selectedPoint = $state(null)
  let restoreDestination = $state('')
  let showDestOverride = $state(false)
  let passphrase = $state('')
  let loading = $state(false)
  let allItems = $state([])
  let restorePoints = $state([])
  let loadingPoints = $state(false)
  let typeFilter = $state('all')
  let deletingRpId = $state(null)
  let confirmDeleteRpId = $state(null)

  // Restore progress
  let restoring = $state(false)

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
  // Per-item map: itemName -> { contents: TarIndex|null, selected: SvelteSet<string>,
  //                              loading: boolean, error: string, search: string, open: boolean }
  let picker = $state(new SvelteMap())

  function ensurePickerEntry(itemName) {
    if (!picker.has(itemName)) {
      picker.set(itemName, {
        contents: null,
        selected: new SvelteSet(),
        loading: false,
        error: '',
        search: '',
        open: false,
      })
    }
    return picker.get(itemName)
  }

  // updateEntry replaces the picker entry with a shallow clone + patch.
  // SvelteMap tracks set(); mutating a value in place after an `await`
  // boundary does NOT propagate (Svelte 5 only sees the synchronous
  // mutation). We always go through this helper to keep that contract.
  // The `selected` SvelteSet is preserved across clones so toggleFilePicked
  // / clearPickerSelection don't lose their reactive backing.
  function updateEntry(itemName, patch) {
    const cur = ensurePickerEntry(itemName)
    picker.set(itemName, { ...cur, ...patch })
  }

  async function togglePickerOpen(item) {
    const cur = ensurePickerEntry(item.name)
    const willOpen = !cur.open
    updateEntry(item.name, { open: willOpen })
    if (willOpen && !cur.contents && !cur.loading) {
      updateEntry(item.name, { loading: true, error: '' })
      try {
        const contents = await api.getRestorePointContents(selectedPoint.jobId, selectedPoint.id, item.name)
        updateEntry(item.name, { contents, loading: false })
      } catch (e) {
        updateEntry(item.name, { error: e?.message || 'failed to load file list', loading: false })
      }
    }
  }

  function toggleFilePicked(itemName, filePath) {
    const entry = picker.get(itemName)
    if (!entry) return
    if (entry.selected.has(filePath)) entry.selected.delete(filePath)
    else entry.selected.add(filePath)
    // SvelteSet is reactive on add/delete; touch the entry too so the
    // summary "X of Y selected" counter rerenders.
    updateEntry(itemName, {})
  }

  function selectAllFiltered(itemName) {
    const entry = picker.get(itemName)
    if (!entry?.contents) return
    for (const f of filteredFiles(entry)) entry.selected.add(f.path)
    updateEntry(itemName, {})
  }

  function clearPickerSelection(itemName) {
    const entry = picker.get(itemName)
    if (!entry) return
    entry.selected.clear()
    updateEntry(itemName, {})
  }

  function filteredFiles(entry) {
    if (!entry?.contents?.files) return []
    const q = entry.search.trim().toLowerCase()
    if (!q) return entry.contents.files
    return entry.contents.files.filter(f => f.path.toLowerCase().includes(q))
  }

  onMount(() => {
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_completed' && msg.run_type === 'restore') {
        restoring = false
      }
    })
    return unsub
  })

  // Gather all backed-up items across all jobs
  $effect(() => {
    gatherItems()
  })

  async function gatherItems() {
    loading = true
    try {
      const details = await Promise.all(
        jobs.map(j => api.getJob(j.id).catch(() => null))
      )
      const itemMap = new SvelteMap()
      for (const detail of details) {
        if (!detail?.items) continue
        for (const item of detail.items) {
          const key = `${item.item_type}:${item.item_name}`
          if (!itemMap.has(key)) {
            itemMap.set(key, {
              name: item.item_name,
              type: item.item_type,
              jobs: [],
            })
          }
          const job = jobs.find(j => j.id === detail.job.id)
          if (job) itemMap.get(key).jobs.push(job)
        }
      }
      allItems = Array.from(itemMap.values())
      // Auto-select items from a pre-selected job (e.g. quick restore from Dashboard)
      if (initialJobId && selectedItems.size === 0) {
        const jid = Number(initialJobId)
        for (const item of allItems) {
          if (item.jobs.some(j => j.id === jid)) {
            const key = `${item.type}:${item.name}`
            selectedItems.set(key, item)
          }
        }
      }
    } catch { /* ignore */ } finally {
      loading = false
    }
  }

  let filteredItems = $derived(
    typeFilter === 'all' ? allItems : allItems.filter(i => i.type === typeFilter)
  )

  let typeOptions = $derived.by(() => {
    const types = new Set(allItems.map(i => i.type))
    return ['all', ...types]
  })

  let selectedCount = $derived(selectedItems.size)

  function typeIcon(type) {
    switch (type) {
      case 'container': return 'M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4'
      case 'vm': return 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z'
      case 'folder': return 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z'
      case 'zfs': return 'M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7M4 7c0 2.21 3.582 4 8 4s8-1.79 8-4M4 7c0-2.21 3.582-4 8-4s8 1.79 8 4M4 12c0 2.21 3.582 4 8 4s8-1.79 8-4'
      default: return 'M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7'
    }
  }

  function typeColor(type) {
    switch (type) {
      case 'container': return 'text-blue-400'
      case 'vm': return 'text-purple-400'
      case 'folder': return 'text-amber-400'
      case 'zfs': return 'text-cyan-400'
      default: return 'text-text-muted'
    }
  }

  function toggleItem(item) {
    const key = `${item.type}:${item.name}`
    if (selectedItems.has(key)) {
      selectedItems.delete(key)
    } else {
      selectedItems.set(key, item)
    }
  }

  function isSelected(item) {
    return selectedItems.has(`${item.type}:${item.name}`)
  }

  function selectAll() {
    for (const item of filteredItems) {
      const key = `${item.type}:${item.name}`
      selectedItems.set(key, item)
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

      // Sort by date descending
      restorePoints = allPoints.sort((a, b) => new Date(b.created_at) - new Date(a.created_at))
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
    preflightResult = null
  }

  let needsPassphrase = $derived(selectedPoint?.encryption === 'age')

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
    restoring = true

    const items = selectedItemsArray.map(item => item.name)

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

    // Feature B: per-item partial restore. Build file_paths map from any
    // picker entries that have a non-empty selection. Items without an
    // active selection are restored in full (legacy behaviour).
    const filePaths = {}
    for (const [itemName, entry] of picker.entries()) {
      if (entry?.selected && entry.selected.size > 0) {
        filePaths[itemName] = Array.from(entry.selected)
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
      <!-- Type filter tabs + selection controls -->
      <div class="flex items-center justify-between mb-4 flex-wrap gap-2">
        <div class="flex items-center gap-2">
          {#each typeOptions as t (t)}
            <button type="button" onclick={() => typeFilter = t}
              class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors capitalize {typeFilter === t ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}">
              {t === 'all' ? 'All' : t + 's'}
            </button>
          {/each}
        </div>
        <div class="flex items-center gap-2">
          <button type="button" onclick={selectAll}
            class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
            Select All
          </button>
          {#if selectedCount > 0}
            <button type="button" onclick={clearSelection}
              class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
              Clear
            </button>
          {/if}
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each filteredItems as item (`${item.type}:${item.name}`)}
          {@const selected = isSelected(item)}
          <button type="button" onclick={() => toggleItem(item)}
            class="bg-surface-2 border rounded-xl p-4 text-left hover:shadow-sm transition-all group
              {selected ? 'border-vault ring-1 ring-vault/30' : 'border-border hover:border-vault/40'}">
            <div class="flex items-center gap-3 mb-2">
              <!-- Checkbox indicator -->
              <div class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors
                {selected ? 'bg-vault border-vault' : 'border-border group-hover:border-vault/40'}">
                {#if selected}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                {/if}
              </div>
              <div class="w-9 h-9 rounded-lg bg-surface-3 flex items-center justify-center group-hover:bg-vault/10 transition-colors">
                <svg aria-hidden="true" class="w-5 h-5 {typeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={typeIcon(item.type)}/></svg>
              </div>
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium text-text truncate">{item.name}</p>
                <p class="text-xs text-text-dim capitalize">{item.type}</p>
              </div>
            </div>
            <p class="text-xs text-text-dim">In {item.jobs.length} job{item.jobs.length !== 1 ? 's' : ''}: {item.jobs.map(j => j.name).join(', ')}</p>
          </button>
        {/each}
      </div>

      <!-- Selection summary + Next button -->
      <div class="flex items-center justify-between mt-4 pt-4 border-t border-border">
        <span class="text-sm text-text-muted">
          {#if selectedCount > 0}
            {selectedCount} item{selectedCount !== 1 ? 's' : ''} selected
          {:else}
            Select items to restore
          {/if}
        </span>
        <button type="button" onclick={proceedToStep2} disabled={selectedCount === 0}
          class="px-5 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
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
        {#each selectedItemsArray as item (`${item.type}:${item.name}`)}
          <div class="flex items-center gap-1.5 px-2.5 py-1 bg-surface-3 rounded-lg">
            <svg aria-hidden="true" class="w-4 h-4 {typeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={typeIcon(item.type)}/></svg>
            <span class="text-xs font-medium text-text">{item.name}</span>
            <span class="text-xs text-text-dim capitalize">({item.type})</span>
          </div>
        {/each}
      </div>
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
            {#if selectedCount === 1}
              {selectedItemsArray[0].name} ({selectedItemsArray[0].type})
            {:else}
              {selectedCount} items
            {/if}
          </span>
        </div>
        {#if selectedCount > 1}
          <div class="flex flex-wrap gap-1.5 justify-end">
            {#each selectedItemsArray as item (`${item.type}:${item.name}`)}
              <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text-dim">{item.name}</span>
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
          <span class="text-xs font-medium {chainHealthTone(selectedPoint)}">{chainHealthLabel(selectedPoint)}</span>
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
              onchange={() => { showDestOverride = false; restoreDestination = '' }} />
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
            <PathBrowser bind:value={restoreDestination} />
            <p class="text-xs text-text-dim mt-1">Files will be written under this path instead of their original location.</p>
          </div>
        {/if}
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
      {#each selectedItemsArray as item (`${item.type}:${item.name}`)}
        {@const entry = picker.get(item.name)}
        {@const sel = entry?.selected?.size || 0}
        {@const total = entry?.contents?.files?.length || 0}
        <details class="group bg-surface-2 border border-border rounded-xl"
          open={entry?.open || false}>
          <summary class="flex items-center justify-between gap-3 cursor-pointer select-none p-3 text-sm"
            onclick={(e) => { e.preventDefault(); togglePickerOpen(item) }}>
            <span class="flex items-center gap-2">
              <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              <span class="font-medium text-text">{item.name}</span>
              <span class="text-xs text-text-dim">({item.type})</span>
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
              {:else}
                <div class="flex items-center gap-2">
                  <input type="text" placeholder="Filter by path…" bind:value={entry.search}
                    class="flex-1 px-3 py-1.5 bg-surface-3 border border-border rounded-lg text-xs text-text placeholder-text-dim" />
                  <button type="button" onclick={() => selectAllFiltered(item.name)}
                    class="text-xs px-2 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text">Select all</button>
                  <button type="button" onclick={() => clearPickerSelection(item.name)}
                    class="text-xs px-2 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text">Clear</button>
                </div>
                <div class="max-h-64 overflow-y-auto border border-border rounded-lg bg-surface-3/30">
                  {#each filteredFiles(entry) as f (f.path)}
                    <label class="flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-surface-3 cursor-pointer">
                      <input type="checkbox" checked={entry.selected.has(f.path)}
                        onchange={() => toggleFilePicked(item.name, f.path)} class="accent-vault" />
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
      {/each}
    </div>

    <!-- Warning banner -->
    <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6 flex items-start gap-3">
      <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
      </svg>
      <div>
        <p class="text-sm font-medium text-warning">This will overwrite existing data</p>
        <p class="text-xs text-text-muted mt-0.5">Restoring will replace current files for
          {#if selectedCount === 1}
            <strong class="text-text">{selectedItemsArray[0].name}</strong>
          {:else}
            <strong class="text-text">{selectedCount} selected items</strong>
          {/if}
          with the backup version.
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
        disabled={restoring || selectedPoint?.chain_status === 'broken' || (needsPassphrase && !passphrase) || !(preflightResult?.ok && preflightFresh)}
        class="w-full sm:w-auto px-6 py-2.5 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2">
        {#if restoring}
          <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
          Restoring...
        {:else}
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
          Start Restore
        {/if}
      </button>
      {#if preflightResult && preflightFresh && !preflightResult.ok && !restoring && selectedPoint?.chain_status !== 'broken'}
        <button type="button"
          onclick={() => { if (window.confirm('Pre-flight checks did not all pass. Restore anyway?')) doRestore() }}
          class="text-xs text-text-dim hover:text-text underline cursor-pointer">Restore anyway</button>
      {/if}
    </div>
  {/if}
</div>
