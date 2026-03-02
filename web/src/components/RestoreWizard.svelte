<script>
  import { api } from '../lib/api.js'
  import { formatDate, relTime, formatBytes, statusBadge } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import Spinner from './Spinner.svelte'

  let { jobs = [], onrestore = () => {} } = $props()

  let step = $state(1)
  let selectedItem = $state(null)
  let selectedPoint = $state(null)
  let restoreDestination = $state('')
  let showDestOverride = $state(false)
  let passphrase = $state('')
  let loading = $state(false)
  let allItems = $state([])
  let restorePoints = $state([])
  let loadingPoints = $state(false)
  let typeFilter = $state('all')

  // Restore progress
  let restoring = $state(false)
  let restoreStatus = $state('')
  let restoreLog = $state('')

  // Gather all backed-up items across all jobs
  $effect(() => {
    gatherItems()
  })

  async function gatherItems() {
    loading = true
    try {
      const details = await Promise.all(
        jobs.filter(j => j.enabled !== false).map(j => api.getJob(j.id).catch(() => null))
      )
      const itemMap = new Map()
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
    } catch { /* ignore */ } finally {
      loading = false
    }
  }

  let filteredItems = $derived(
    typeFilter === 'all' ? allItems : allItems.filter(i => i.type === typeFilter)
  )

  let typeOptions = $derived(() => {
    const types = new Set(allItems.map(i => i.type))
    return ['all', ...types]
  })

  function typeIcon(type) {
    switch (type) {
      case 'container': return 'M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4'
      case 'vm': return 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z'
      case 'folder': return 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z'
      default: return 'M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7'
    }
  }

  function typeColor(type) {
    switch (type) {
      case 'container': return 'text-blue-400'
      case 'vm': return 'text-purple-400'
      case 'folder': return 'text-amber-400'
      default: return 'text-text-muted'
    }
  }

  async function selectItem(item) {
    selectedItem = item
    step = 2
    loadingPoints = true
    restorePoints = []
    try {
      const allPoints = []
      for (const job of item.jobs) {
        const points = await api.getRestorePoints(job.id).catch(() => [])
        for (const p of (points || [])) {
          allPoints.push({ ...p, jobName: job.name, jobId: job.id, encryption: job.encryption })
        }
      }
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
  }

  let needsPassphrase = $derived(selectedPoint?.encryption === 'age')

  function parseMetadata(meta) {
    if (!meta) return {}
    try { return JSON.parse(meta) } catch { return {} }
  }

  function doRestore() {
    if (!selectedItem || !selectedPoint) return
    restoring = true
    restoreStatus = 'running'
    restoreLog = 'Restoring...'

    const payload = {
      restore_point_id: selectedPoint.id,
      item_name: selectedItem.name,
      item_type: selectedItem.type,
    }
    if (showDestOverride && restoreDestination.trim()) {
      payload.destination = restoreDestination.trim()
    }
    if (passphrase) {
      payload.passphrase = passphrase
    }

    onrestore(selectedPoint.jobId, payload)
  }

  function goBack() {
    if (step === 3) { step = 2; selectedPoint = null }
    else if (step === 2) { step = 1; selectedItem = null; restorePoints = [] }
  }
</script>

<div>
  <!-- Step indicator -->
  <div class="flex items-center gap-2 mb-6">
    {#each [{n:1, label:'Select Item'}, {n:2, label:'Choose Version'}, {n:3, label:'Restore'}] as s}
      <button type="button" onclick={() => { if (s.n < step) { if (s.n === 1) { step = 1; selectedItem = null; selectedPoint = null } else if (s.n === 2) { step = 2; selectedPoint = null } } }}
        class="flex items-center gap-2 {s.n <= step ? '' : 'opacity-40'}">
        <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n < step ? 'bg-vault text-white' : s.n === step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
          {#if s.n < step}
            <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
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

  <!-- Step 1: What to restore -->
  {#if step === 1}
    {#if loading}
      <Spinner text="Loading backed-up items..." />
    {:else if allItems.length === 0}
      <div class="text-center py-12">
        <p class="text-4xl mb-3">📦</p>
        <p class="text-sm text-text-muted">No backed-up items found. Run a backup first.</p>
      </div>
    {:else}
      <!-- Type filter tabs -->
      <div class="flex items-center gap-2 mb-4">
        {#each typeOptions() as t}
          <button type="button" onclick={() => typeFilter = t}
            class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors capitalize {typeFilter === t ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}">
            {t === 'all' ? 'All' : t + 's'}
          </button>
        {/each}
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each filteredItems as item}
          <button type="button" onclick={() => selectItem(item)}
            class="bg-surface-2 border border-border rounded-xl p-4 text-left hover:border-vault/40 hover:shadow-sm transition-all group">
            <div class="flex items-center gap-3 mb-2">
              <div class="w-9 h-9 rounded-lg bg-surface-3 flex items-center justify-center group-hover:bg-vault/10 transition-colors">
                <svg class="w-5 h-5 {typeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={typeIcon(item.type)}/></svg>
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
    {/if}

  <!-- Step 2: Which version -->
  {:else if step === 2}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors">
        <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to items
      </button>
      <div class="flex items-center gap-3 mt-2">
        <svg class="w-5 h-5 {typeColor(selectedItem.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={typeIcon(selectedItem.type)}/></svg>
        <h3 class="text-base font-semibold text-text">{selectedItem.name}</h3>
        <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text-dim capitalize">{selectedItem.type}</span>
      </div>
    </div>

    {#if loadingPoints}
      <Spinner text="Loading restore points..." />
    {:else if restorePoints.length === 0}
      <div class="text-center py-12">
        <p class="text-4xl mb-3">🕐</p>
        <p class="text-sm text-text-muted">No restore points found for this item.</p>
      </div>
    {:else}
      <div class="space-y-2">
        {#each restorePoints as rp, i}
          {@const meta = parseMetadata(rp.metadata)}
          {@const isRecommended = i === 0 && (rp.status === 'completed' || rp.status === 'success')}
          <button type="button" onclick={() => selectPoint(rp)}
            class="w-full bg-surface-2 border rounded-xl p-4 text-left hover:shadow-sm transition-all
              {isRecommended ? 'border-vault/40 hover:border-vault/60' : 'border-border hover:border-vault/30'}">
            <div class="flex items-center justify-between mb-2">
              <div class="flex items-center gap-2">
                <span class="text-xs px-2 py-0.5 rounded-full font-medium uppercase bg-vault/10 text-vault">{rp.backup_type}</span>
                {#if isRecommended}
                  <span class="text-xs px-2 py-0.5 rounded-full bg-success/15 text-success font-medium">Recommended</span>
                {/if}
                {#if meta.verified}
                  <span class="text-xs px-2 py-0.5 rounded-full bg-info/15 text-info font-medium">Verified</span>
                {/if}
              </div>
              <span class="text-xs text-text-dim">{relTime(rp.created_at)}</span>
            </div>
            <div class="flex items-center gap-4 text-xs text-text-dim">
              <span>{formatBytes(rp.size_bytes)}</span>
              <span>Run #{rp.job_run_id}</span>
              <span class="text-text-muted">{rp.jobName}</span>
              {#if meta.items}
                <span>{meta.items} items</span>
              {/if}
            </div>
          </button>
        {/each}
      </div>
      <p class="text-xs text-text-dim mt-3 text-center">{restorePoints.length} restore point{restorePoints.length !== 1 ? 's' : ''}</p>
    {/if}

  <!-- Step 3: Restore options -->
  {:else if step === 3}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors">
        <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to versions
      </button>
    </div>

    <!-- Summary card -->
    <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6">
      <h3 class="text-sm font-semibold text-text mb-3">Restore Summary</h3>
      <div class="space-y-2 text-sm">
        <div class="flex justify-between">
          <span class="text-text-muted">Item</span>
          <span class="text-text font-medium">{selectedItem.name} ({selectedItem.type})</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Restore Point</span>
          <span class="text-text">{formatDate(selectedPoint.created_at)}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Size</span>
          <span class="text-text">{formatBytes(selectedPoint.size_bytes)}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-text-muted">Backup Type</span>
          <span class="text-text uppercase text-xs">{selectedPoint.backup_type}</span>
        </div>
      </div>
    </div>

    <!-- Options -->
    <div class="space-y-5 mb-6">
      <!-- Destination override -->
      <div>
        <div class="flex items-center gap-2 mb-2">
          <label class="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" bind:checked={showDestOverride} class="sr-only peer" />
            <div class="w-9 h-5 bg-surface-4 peer-checked:bg-vault rounded-full peer after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full"></div>
          </label>
          <span class="text-sm text-text-muted">Override restore destination</span>
        </div>
        {#if showDestOverride}
          <PathBrowser bind:value={restoreDestination} />
          <p class="text-xs text-text-dim mt-1">Leave empty to restore to the original location.</p>
        {/if}
      </div>

      <!-- Passphrase -->
      {#if needsPassphrase}
        <div>
          <label for="rw_passphrase" class="block text-sm font-medium text-text-muted mb-2">Encryption Passphrase</label>
          <input id="rw_passphrase" type="password" bind:value={passphrase}
            placeholder="Enter the passphrase used to encrypt these backups"
            class="w-full sm:w-96 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
          <p class="text-xs text-text-dim mt-1">This backup uses age encryption. A passphrase is required to decrypt.</p>
        </div>
      {/if}
    </div>

    <!-- Warning banner -->
    <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6 flex items-start gap-3">
      <svg class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
      </svg>
      <div>
        <p class="text-sm font-medium text-warning">This will overwrite existing data</p>
        <p class="text-xs text-text-muted mt-0.5">Restoring will replace current files for <strong class="text-text">{selectedItem.name}</strong> with the backup version.</p>
      </div>
    </div>

    <!-- Restore button -->
    <button type="button" onclick={doRestore} disabled={restoring || (needsPassphrase && !passphrase)}
      class="w-full sm:w-auto px-6 py-2.5 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2">
      {#if restoring}
        <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
        Restoring...
      {:else}
        <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
        Start Restore
      {/if}
    </button>
  {/if}
</div>
