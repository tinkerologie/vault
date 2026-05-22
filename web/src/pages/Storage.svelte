<script>
  import { onMount } from 'svelte'
  import { SvelteMap, SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { formatBytes, formatDate, parseConfig, relTime } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import PathBrowser from '../components/PathBrowser.svelte'
  import Tooltip from '../components/Tooltip.svelte'

  let loading = $state(true)
  let destinations = $state([])
  let showModal = $state(false)
  let editing = $state(null)
  let testing = $state(null)
  let saving = $state(false)
  let testResults = $state(new SvelteMap())
  let toast = $state({ message: '', type: 'info', key: 0 })
  let confirmDelete = $state({ show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 })
  let depCounts = $state(new SvelteMap())

  // Import state
  let showImport = $state(false)
  let importStorageId = $state(0)
  let importStorageName = $state('')
  let importBasePath = $state('')
  let scanning = $state(false)
  let scannedBackups = $state([])
  let selectedBackups = $state(new SvelteSet())
  let vaultDBInfo = $state(null)
  let importing = $state(false)

  let form = $state(defaultForm())

  // Per-destination dedup stats, polled every 30s for dedup-enabled
  // destinations. Keyed by destination ID. cleanupBusy / verifyBusy track
  // per-card button busy states so we can disable them while a request is
  // in flight.
  let dedupStats = $state(new SvelteMap())
  let cleanupBusy = $state(new SvelteSet())

  function defaultForm() {
    return {
      name: '',
      type: 'local',
      config: { path: '' },
      dedup_enabled: false,
    }
  }

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(() => {
    loadData()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_completed' || msg.type === 'import_completed') {
        loadData()
      }
      // Refresh the affected card immediately after a GC run instead of
      // waiting for the 30s poll cycle.
      if (msg.type === 'dedup_gc_complete' && msg.destination) {
        refreshOneDedupStats(msg.destination)
      }
    })
    // Refresh dedup stats every 30s for dedup-enabled destinations.
    const pollHandle = setInterval(refreshDedupStats, 30000)
    return () => { unsub(); clearInterval(pollHandle) }
  })

  async function refreshDedupStats() {
    const targets = destinations.filter(d => d.dedup_enabled)
    if (targets.length === 0) return
    const next = new SvelteMap(dedupStats)
    await Promise.all(targets.map(async (d) => {
      try {
        next.set(d.id, await api.dedupStats(d.id))
      } catch (e) {
        // 404 is expected briefly before the first backup creates the repo.
        // Keep any previous stats; don't clear on transient failure.
      }
    }))
    dedupStats = next
  }

  async function refreshOneDedupStats(id) {
    try {
      const s = await api.dedupStats(id)
      const next = new SvelteMap(dedupStats)
      next.set(id, s)
      dedupStats = next
    } catch { /* ignore */ }
  }

  async function runCleanup(id) {
    if (cleanupBusy.has(id)) return
    cleanupBusy.add(id)
    cleanupBusy = new SvelteSet(cleanupBusy)
    try {
      await api.runDedupGC(id)
      showToast('Cleanup started — refreshing stats…', 'info')
      // Best-effort refresh in 2s; the WS dedup_gc_complete event will
      // catch up the card if the GC takes longer.
      setTimeout(async () => {
        await refreshOneDedupStats(id)
        cleanupBusy.delete(id)
        cleanupBusy = new SvelteSet(cleanupBusy)
      }, 2000)
    } catch (e) {
      cleanupBusy.delete(id)
      cleanupBusy = new SvelteSet(cleanupBusy)
      showToast(`Cleanup failed: ${e.message}`, 'error')
    }
  }

  async function loadData() {
    loading = true
    try {
      destinations = (await api.listStorage()) || []
      // Load dependent job counts for each storage
      const counts = new SvelteMap()
      await Promise.all(destinations.map(async (d) => {
        try {
          const result = await api.getDependentJobs(d.id)
          counts.set(d.id, result?.job_count || 0)
        } catch { /* ignore */ counts.set(d.id, 0) }
      }))
      depCounts = counts
      refreshDedupStats()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
    }
  }

  function openCreate() {
    editing = null
    form = defaultForm()
    showModal = true
  }

  function openEdit(dest) {
    editing = dest
    const cfg = parseConfig(dest.config)
    // Migrate legacy "path" → "base_path" for SFTP/SMB configs.
    if ((dest.type === 'sftp' || dest.type === 'smb') && cfg.base_path === undefined) {
      cfg.base_path = cfg.path ?? ''
    }
    form = {
      name: dest.name,
      type: dest.type,
      config: cfg,
      dedup_enabled: !!dest.dedup_enabled,
    }
    showModal = true
  }

  async function saveStorage() {
    if (saving) return
    saving = true
    try {
      const payload = {
        name: form.name,
        type: form.type,
        config: JSON.stringify(form.config),
        // Top-level: stored as its own column on storage_destinations.
        // Immutable after creation (UI gates the toggle when editing).
        dedup_enabled: !!form.dedup_enabled,
      }
      if (editing) {
        await api.updateStorage(editing.id, payload)
        showToast('Storage updated successfully', 'success')
      } else {
        await api.createStorage(payload)
        showToast('Storage created successfully', 'success')
      }
      showModal = false
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  async function deleteStorage(id, name) {
    // Check for dependent jobs before showing the dialog.
    let jobCount = 0
    try {
      const result = await api.getDependentJobs(id)
      jobCount = result?.job_count || 0
    } catch { /* ignore */ }
    confirmDelete = { show: true, id, name, deleteFiles: false, jobCount }
  }

  async function doDeleteStorage() {
    const { id, deleteFiles, jobCount } = confirmDelete
    confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 }
    try {
      await api.deleteStorage(id, { deleteFiles, force: jobCount > 0 })
      showToast(deleteFiles ? 'Storage and backup files deleted' : 'Storage deleted (backup files kept)', 'success')
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function openImport(id, name) {
    importStorageId = id
    importStorageName = name
    importBasePath = ''
    scannedBackups = []
    selectedBackups = new SvelteSet()
    vaultDBInfo = null
    showImport = true
    await scanStorage()
  }

  async function scanStorage() {
    scanning = true
    try {
      const results = await api.scanStorage(importStorageId, importBasePath)
      scannedBackups = results?.backups || []
      vaultDBInfo = results?.vault_db || null
      selectedBackups = new SvelteSet(scannedBackups.map((_b, i) => i))
    } catch (e) {
      showToast(`Scan failed: ${e.message}`, 'error')
    } finally {
      scanning = false
    }
  }

  function toggleBackup(idx) {
    if (selectedBackups.has(idx)) selectedBackups.delete(idx)
    else selectedBackups.add(idx)
  }

  function toggleAllBackups() {
    if (selectedBackups.size === scannedBackups.length) {
      selectedBackups.clear()
    } else {
      selectedBackups.clear()
      for (let i = 0; i < scannedBackups.length; i++) {
        selectedBackups.add(i)
      }
    }
  }

  async function doImport() {
    importing = true
    try {
      const backups = scannedBackups.filter((_b, i) => selectedBackups.has(i))
      const result = await api.importBackups(importStorageId, backups)
      showToast(`Imported ${result.imported} of ${result.total} backups`, 'success')
      showImport = false
    } catch (e) {
      showToast(`Import failed: ${e.message}`, 'error')
    } finally {
      importing = false
    }
  }

  async function doRestoreDB(storagePath) {
    if (!confirm('This will replace the current Vault database with the backup copy. All current data will be lost. The daemon will need to be restarted. Continue?')) return
    try {
      const result = await api.restoreDB(importStorageId, storagePath)
      showToast(result.message || 'Database restored. Please restart Vault.', 'success')
      showImport = false
    } catch (e) {
      showToast(`Restore failed: ${e.message}`, 'error')
    }
  }

  function formatSize(bytes) {
    if (!bytes || bytes === 0) return '—'
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0
    let size = bytes
    while (size >= 1024 && i < units.length - 1) { size /= 1024; i++ }
    return `${size.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
  }

  async function testConnection(id) {
    testing = id
    try {
      const result = await api.testStorage(id)
      testResults.set(id, result)
      if (result.success) {
        showToast('Connection successful!', 'success')
      } else {
        showToast(`Connection failed: ${result.error}`, 'error')
      }
    } catch (e) {
      showToast(e.message, 'error')
      testResults.set(id, { success: false, error: e.message })
    } finally {
      testing = null
    }
  }

  function onTypeChange(event) {
    const nextType = event?.currentTarget?.value || form.type
    const defaults = {
      local: { path: '' },
      sftp: { host: '', port: 22, user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      smb: { host: '', share: '', user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      nfs: { host: '', export: '', base_path: '', version: '4', options: '', bandwidth_limit_mbps: 0 },
      webdav: { url: '', username: '', password: '', base_path: '', insecure_skip_verify: false, timeout_seconds: 0, stall_timeout_seconds: 300, chunk_size_mb: 0, bandwidth_limit_mbps: 0 },
      s3: { bucket: '', region: '', access_key: '', secret_key: '', endpoint: '', base_path: '', force_path_style: false, upload_timeout_minutes: 0, part_size_mb: 0, bandwidth_limit_mbps: 0 },
    }
    // Reassign the full form object so Svelte always re-renders the keyed
    // config block when switching destination type.
    form = {
      ...form,
      type: nextType,
      config: defaults[nextType] || {},
    }
  }

  const storageIcons = {
    local: 'M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z',
    sftp: 'M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z',
    smb: 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z',
    nfs: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
    webdav: 'M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z',
    s3: 'M5 19a2 2 0 01-2-2V7a2 2 0 012-2h3.586a1 1 0 01.707.293l1.414 1.414A1 1 0 0011.414 7H19a2 2 0 012 2v8a2 2 0 01-2 2H5z',
  }

  const storageColors = {
    local: 'text-blue-400',
    sftp: 'text-emerald-400',
    smb: 'text-purple-400',
    nfs: 'text-amber-400',
    webdav: 'text-cyan-400',
    s3: 'text-orange-400',
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Storage Destinations</h1>
      <p class="text-sm text-text-muted mt-1">Configure where your backups are stored</p>
    </div>
    {#if destinations.length > 0}
      <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
        Add Storage
      </button>
    {/if}
  </div>

  {#if loading}
    <Spinner text="Loading storage..." />
  {:else if destinations.length === 0}
    <EmptyState title="No storage destinations" subtitle="Required before creating jobs" description="Add a storage destination to start backing up your data." actionLabel="Add Storage" onaction={() => openCreate()}>
      {#snippet iconSlot()}
        <svg aria-hidden="true" class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={storageIcons.local}/></svg>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 stagger">
      {#each destinations as dest (dest.id)}
        {@const cfg = parseConfig(dest.config)}
        {@const tr = testResults.get(dest.id)}
        {@const jobCount = depCounts.get(dest.id) || 0}
        <div class="bg-surface-2 border border-border rounded-xl p-5 hover:border-vault/30 hover:shadow-sm transition-all">
          <div class="flex items-start justify-between mb-3">
            <div class="flex items-center gap-3">
              <div class="w-10 h-10 rounded-lg bg-surface-3 flex items-center justify-center">
                <svg aria-hidden="true" class="w-5 h-5 {storageColors[dest.type] || 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={storageIcons[dest.type] || storageIcons.local}/>
                </svg>
              </div>
              <div>
                <h2 class="text-sm font-semibold text-text">{dest.name}</h2>
                <span class="text-xs text-text-dim uppercase">{dest.type}</span>
              </div>
            </div>
            <div class="flex items-center gap-1">
              <button onclick={() => openImport(dest.id, dest.name)} class="flex items-center gap-1 px-2 py-1.5 text-xs font-medium text-text-muted hover:text-vault hover:bg-vault/10 rounded-lg transition-colors" title="Import Backups" aria-label="Import backups">
                <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12"/></svg>
                Import
              </button>
              <button onclick={() => openEdit(dest)} class="p-1.5 text-text-muted hover:text-text hover:bg-surface-3 rounded-lg transition-colors" title="Edit" aria-label="Edit storage">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/></svg>
              </button>
              <button onclick={() => deleteStorage(dest.id, dest.name)} class="p-1.5 text-text-muted hover:text-danger hover:bg-danger/10 rounded-lg transition-colors" title="Delete" aria-label="Delete storage">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
              </button>
            </div>
          </div>

          <!-- Config summary -->
          <div class="text-xs text-text-dim space-y-1 mb-4">
            {#if dest.type === 'local'}
              <p>Path: {cfg.path || '—'}</p>
            {:else if dest.type === 'sftp'}
              <p>Host: {cfg.host || '—'}:{cfg.port || 22}</p>
              <p>Path: {cfg.base_path || cfg.path || '/'}</p>
            {:else if dest.type === 'smb'}
              <p>Share: \\{cfg.host || '—'}\{cfg.share || '—'}</p>
              {#if cfg.base_path || cfg.path}<p>Path: {cfg.base_path || cfg.path}</p>{/if}
            {:else if dest.type === 'nfs'}
              <p class="text-xs text-text-muted truncate">{cfg.host}:{cfg.export}</p>
            {:else if dest.type === 'webdav'}
              <p class="text-xs text-text-muted truncate">{cfg.url || '—'}</p>
              {#if cfg.base_path}<p>Path: {cfg.base_path}</p>{/if}
            {:else if dest.type === 's3'}
              <p>Bucket: {cfg.bucket || '—'}</p>
              <p>Region: {cfg.region || '—'}</p>
              {#if cfg.endpoint}<p class="truncate">Endpoint: {cfg.endpoint}</p>{/if}
            {/if}
          </div>

          {#if dest.dedup_enabled}
            {@const s = dedupStats.get(dest.id)}
            <div class="border-t border-border pt-3 mb-3 text-xs space-y-1">
              {#if s}
                <div class="flex justify-between">
                  <span class="text-text-muted">Dedup</span>
                  <span class="font-medium text-text">{(s.dedup_ratio || 1).toFixed(1)}× ({formatBytes(s.logical_bytes)} → {formatBytes(s.physical_bytes)})</span>
                </div>
                <div class="flex justify-between text-text-dim">
                  <span>Chunks · Packs</span>
                  <span>{(s.total_chunks ?? 0).toLocaleString()} · {(s.total_packs ?? 0).toLocaleString()}</span>
                </div>
                <div class="flex justify-between text-text-dim">
                  <span>Wasted</span>
                  <span>
                    {formatBytes(s.wasted_bytes_estimate)}
                    {#if s.last_gc_at && s.last_gc_at !== '0001-01-01T00:00:00Z'}
                      <span class="text-text-dim">· last cleanup {relTime(s.last_gc_at)}</span>
                    {/if}
                  </span>
                </div>
                <div class="pt-1">
                  <button
                    type="button"
                    onclick={() => runCleanup(dest.id)}
                    disabled={cleanupBusy.has(dest.id)}
                    class="px-2.5 py-1 text-xs rounded-md bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text disabled:opacity-50 transition-colors"
                  >
                    {cleanupBusy.has(dest.id) ? 'Cleaning…' : 'Run cleanup'}
                  </button>
                </div>
              {:else}
                <div class="text-text-dim italic">Dedup repo not initialised yet — first backup populates it.</div>
              {/if}
            </div>
          {/if}

          <div class="flex items-center justify-between gap-3 pt-3 border-t border-border">
            <div class="flex items-center gap-2 min-w-0">
              <span class="text-xs text-text-dim whitespace-nowrap">{formatDate(dest.created_at)}</span>
              {#if jobCount > 0}
                <span class="text-xs px-2.5 py-1 rounded-full bg-vault/10 text-vault font-medium whitespace-nowrap">{jobCount} job{jobCount !== 1 ? 's' : ''}</span>
              {:else}
                <span class="text-xs text-text-dim whitespace-nowrap">No jobs</span>
              {/if}
              {#if dest.last_health_check_at}
                {#if dest.last_health_check_status === 'ok'}
                  <span class="text-xs px-2.5 py-1 rounded-full bg-success/10 text-success font-medium whitespace-nowrap" title={`Last health check: ${formatDate(dest.last_health_check_at)}`}>Healthy</span>
                {:else if dest.last_health_check_status === 'failed'}
                  <span class="text-xs px-2.5 py-1 rounded-full bg-danger/10 text-danger font-medium whitespace-nowrap" title={dest.last_health_check_error || 'health check failed'}>Unhealthy</span>
                {/if}
              {/if}
            </div>
            <button
              onclick={() => testConnection(dest.id)}
              disabled={testing === dest.id}
              class="text-xs px-2.5 py-1 rounded-full font-medium transition-colors whitespace-nowrap min-w-[88px] text-center inline-flex items-center justify-center gap-1
                {tr
                  ? (tr.success ? 'bg-success/10 text-success hover:bg-success/20' : 'bg-danger/10 text-danger hover:bg-danger/20')
                  : 'bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text'}"
            >
              {#if testing === dest.id}
                Testing…
              {:else if tr}
                {#if tr.success}<svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg> Connected{:else}<svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg> Failed{/if}
              {:else}
                Test
              {/if}
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Create/Edit Modal -->
<Modal show={showModal} title={editing ? 'Edit Storage' : 'Add Storage'} onclose={() => showModal = false}>
  <form onsubmit={(e) => { e.preventDefault(); saveStorage() }} class="space-y-5">
    <div>
      <label for="sname" class="block text-sm font-medium text-text-muted mb-1.5">Name</label>
      <input id="sname" type="text" bind:value={form.name} required
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="My Backup Target" />
    </div>

    <div>
      <label for="stype" class="block text-sm font-medium text-text-muted mb-1.5">Type</label>
      <select id="stype" value={form.type} onchange={onTypeChange}
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
        <option value="local">Local Path</option>
        <option value="sftp">SFTP</option>
        <option value="smb">SMB / CIFS</option>
        <option value="nfs">NFS</option>
        <option value="webdav">WebDAV</option>
        <option value="s3">S3 / S3-Compatible</option>
      </select>
    </div>

    <!-- Dynamic config fields per type -->
    {#key form.type}
    {#if form.type === 'local'}
      <div>
        <span class="block text-sm font-medium text-text-muted mb-1.5">Path</span>
        <PathBrowser bind:value={form.config.path} />
      </div>
    {:else if form.type === 'sftp'}
      <div class="grid grid-cols-3 gap-3">
        <div class="col-span-2">
          <label for="cfg_host" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
          <input id="cfg_host" type="text" bind:value={form.config.host}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="192.168.1.100" />
        </div>
        <div>
          <label for="cfg_port" class="block text-sm font-medium text-text-muted mb-1.5">Port</label>
          <input id="cfg_port" type="number" min="1" max="65535" bind:value={form.config.port}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" placeholder="22" />
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="cfg_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
          <input id="cfg_user" type="text" bind:value={form.config.user}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="cfg_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
          <input id="cfg_pass" type="password" autocomplete="off" bind:value={form.config.password}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="cfg_spath" class="block text-sm font-medium text-text-muted mb-1.5">Remote Path <Tooltip text="Absolute path on the SFTP server where Vault will store backups. The directory must exist and the user must have write permission." /></label>
        <input id="cfg_spath" type="text" bind:value={form.config.base_path}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="/backups/vault" />
      </div>
    {:else if form.type === 'smb'}
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="cfg_smbhost" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
          <input id="cfg_smbhost" type="text" bind:value={form.config.host}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="192.168.1.100" />
        </div>
        <div>
          <label for="cfg_share" class="block text-sm font-medium text-text-muted mb-1.5">Share <Tooltip text="The top-level SMB share name as configured on the server (e.g. Backups). Use the Path field below for a sub-folder within the share." /></label>
          <input id="cfg_share" type="text" bind:value={form.config.share}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="Backups" />
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="cfg_smbuser" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
          <input id="cfg_smbuser" type="text" bind:value={form.config.user}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="cfg_smbpass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
          <input id="cfg_smbpass" type="password" autocomplete="off" bind:value={form.config.password}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="cfg_smbpath" class="block text-sm font-medium text-text-muted mb-1.5">Path</label>
        <input id="cfg_smbpath" type="text" bind:value={form.config.base_path}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="vault" />
      </div>
    {:else if form.type === 'nfs'}
      <div class="grid grid-cols-2 gap-3">
        <div class="col-span-2">
          <label for="nfs_host" class="block text-sm font-medium text-text-muted mb-1.5">NFS Host</label>
          <input id="nfs_host" type="text" bind:value={form.config.host} placeholder="192.168.1.100"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div class="col-span-2">
          <label for="nfs_export" class="block text-sm font-medium text-text-muted mb-1.5">Export Path <Tooltip text="The path the NFS server exports — matches the entry in /etc/exports on the server (e.g. /mnt/user/backups). This is what gets mounted, not a sub-path within it." /></label>
          <input id="nfs_export" type="text" bind:value={form.config.export} placeholder="/mnt/user/backups"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="nfs_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional sub-directory within the mounted export where Vault will write its data. Leave blank to use the export root directly." /></label>
          <input id="nfs_base" type="text" bind:value={form.config.base_path} placeholder="vault"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="nfs_version" class="block text-sm font-medium text-text-muted mb-1.5">NFS Version <Tooltip text="NFSv3: wider compatibility, simpler setup. NFSv4: better security and performance, but may require DNS and auth configuration." /></label>
          <select id="nfs_version" bind:value={form.config.version}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
            <option value="3">NFSv3</option>
            <option value="4">NFSv4</option>
          </select>
        </div>
        <div class="col-span-2">
          <label for="nfs_options" class="block text-sm font-medium text-text-muted mb-1.5">Mount Options <Tooltip text="Optional NFS mount flags such as rw, sync, hard, soft, or nolock. Leave blank for sensible defaults." /></label>
          <input id="nfs_options" type="text" bind:value={form.config.options} placeholder="Optional: rw,sync"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
    {:else if form.type === 'webdav'}
      <div>
        <label for="dav_url" class="block text-sm font-medium text-text-muted mb-1.5">Server URL <Tooltip text="Full URL to the WebDAV endpoint, e.g. https://nextcloud.example.com/remote.php/dav/files/username/" /></label>
        <input id="dav_url" type="url" bind:value={form.config.url} placeholder="https://webdav.example.com/"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="dav_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
          <input id="dav_user" type="text" bind:value={form.config.username}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="dav_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password / App Token</label>
          <input id="dav_pass" type="password" autocomplete="off" bind:value={form.config.password}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="dav_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional sub-folder under the server URL where Vault will write its data." /></label>
        <input id="dav_base" type="text" bind:value={form.config.base_path} placeholder="vault"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <label class="flex items-center gap-2 text-sm text-text-muted">
        <input type="checkbox" bind:checked={form.config.insecure_skip_verify} class="accent-vault" />
        Allow self-signed TLS certificates
        <Tooltip text="Skip TLS certificate validation. Only enable for trusted private servers using self-signed certificates." />
      </label>
      <details class="group">
        <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
          Advanced &middot; Transfer
        </summary>
        <div class="flex flex-col gap-3 mt-3">
          <div>
            <label for="dav_chunk" class="block text-sm font-medium text-text-muted mb-1.5">
              Chunk size (MiB)
              <Tooltip text="Files larger than this are split into independent WebDAV PUT requests. Default 0 = 50 MiB. Set to -1 to disable chunking." />
            </label>
            <input id="dav_chunk" type="number" bind:value={form.config.chunk_size_mb} placeholder="0 (50 MiB)" min="-1"
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          </div>
          <div>
            <label for="dav_stall" class="block text-sm font-medium text-text-muted mb-1.5">
              Stall timeout (seconds)
              <Tooltip text="Abort an upload if no bytes flow for this many seconds. Default 300 (5 min). Set to -1 to disable. Multi-GB uploads of any size complete as long as data keeps moving." />
            </label>
            <input id="dav_stall" type="number" bind:value={form.config.stall_timeout_seconds} placeholder="300" min="-1"
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          </div>
          <div>
            <label for="dav_overall" class="block text-sm font-medium text-text-muted mb-1.5">
              Overall request timeout (seconds)
              <Tooltip text="Hard ceiling on every WebDAV request including upload body. Default 0 = unlimited (recommended). Only set a value if you understand it must accommodate the largest single file upload over your slowest link." />
            </label>
            <input id="dav_overall" type="number" bind:value={form.config.timeout_seconds} placeholder="0 (unlimited)" min="0"
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          </div>
        </div>
      </details>
    {:else if form.type === 's3'}
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="s3_bucket" class="block text-sm font-medium text-text-muted mb-1.5">Bucket</label>
          <input id="s3_bucket" type="text" bind:value={form.config.bucket} placeholder="my-vault-backups"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="s3_region" class="block text-sm font-medium text-text-muted mb-1.5">Region <Tooltip text="AWS region code, e.g. us-east-1. For S3-compatible providers, use the region required by the provider." /></label>
          <input id="s3_region" type="text" bind:value={form.config.region} placeholder="us-east-1"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="s3_ak" class="block text-sm font-medium text-text-muted mb-1.5">Access Key ID</label>
          <input id="s3_ak" type="text" bind:value={form.config.access_key} autocomplete="off"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
        </div>
        <div>
          <label for="s3_sk" class="block text-sm font-medium text-text-muted mb-1.5">Secret Access Key</label>
          <input id="s3_sk" type="password" bind:value={form.config.secret_key} autocomplete="off"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="s3_endpoint" class="block text-sm font-medium text-text-muted mb-1.5">Endpoint <Tooltip text="Optional. Required for S3-compatible providers like Backblaze B2, MinIO, Cloudflare R2 or Wasabi. Leave blank for AWS S3." /></label>
        <input id="s3_endpoint" type="text" bind:value={form.config.endpoint} placeholder="https://s3.us-west-002.backblazeb2.com"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
      </div>
      <div>
        <label for="s3_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional key prefix prepended to every object Vault writes." /></label>
        <input id="s3_base" type="text" bind:value={form.config.base_path} placeholder="vault"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <label class="flex items-center gap-2 text-sm text-text-muted">
        <input type="checkbox" bind:checked={form.config.force_path_style} class="accent-vault" />
        Force path-style addressing
        <Tooltip text="Enable for older S3-compatible servers (e.g. older MinIO) that don't support virtual hosted-style buckets." />
      </label>
      <details class="group">
        <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
          Advanced &middot; Transfer
        </summary>
        <div class="flex flex-col gap-3 mt-3">
          <div>
            <label for="s3_upload_timeout" class="block text-sm font-medium text-text-muted mb-1.5">
              Upload timeout (minutes)
              <Tooltip text="Maximum time a single object upload (including multipart transfers) may take. Defaults to 240 (4 hours) when 0 or unset. Increase for very large files over slow links." />
            </label>
            <input id="s3_upload_timeout" type="number" bind:value={form.config.upload_timeout_minutes} placeholder="240 (default)" min="0"
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          </div>
          <div>
            <label for="s3_part_size" class="block text-sm font-medium text-text-muted mb-1.5">
              Part size (MiB)
              <Tooltip text="Multipart upload part size. S3 allows max 10,000 parts per object, so this directly sets the per-object ceiling (PartSize × 10,000). Default 0 = 64 MiB (640 GB ceiling). Raise for multi-TB datasets (256 → 2.5 TB, 1024 → 10 TB). Valid range: 5-5120 MiB." />
            </label>
            <input id="s3_part_size" type="number" bind:value={form.config.part_size_mb} placeholder="0 (64 MiB)" min="0" max="5120"
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          </div>
        </div>
      </details>
    {/if}
    {/key}

    <!-- Universal (remote only): bandwidth throttling. Local destinations
         talk directly to the host's filesystem; there is no upstream link
         to protect, so the field is hidden + the backend factory skips the
         throttle wrapper for `type === 'local'`. -->
    {#if form.type !== 'local'}
      <div>
        <label for="bandwidth_limit_mbps" class="block text-sm font-medium text-text-muted mb-1.5">
          Bandwidth limit (Mbps)
          <Tooltip text="Cap upload/download throughput for this destination in megabits per second. 0 = unlimited. Useful for shared internet uplinks so backups don't saturate the line. Metadata operations (list/stat/test) are never throttled." />
        </label>
        <input id="bandwidth_limit_mbps" type="number" bind:value={form.config.bandwidth_limit_mbps} min="0" placeholder="0 (unlimited)"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    {/if}

    <!-- Universal: deduplication. Top-level column on storage_destinations.
         Immutable after creation — backend ignores any update attempt and
         the UI disables the toggle when editing. -->
    <div class="border-t border-border pt-4">
      <label class="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          bind:checked={form.dedup_enabled}
          disabled={editing !== null}
          class="accent-vault mt-1"
        />
        <span class="flex-1">
          <span class="block font-medium text-text">Enable deduplication</span>
          <span class="block text-xs text-text-muted mt-0.5">
            Stores only changed data blocks across snapshots and jobs targeting this destination. Recommended for backups containing similar data (Immich, Nextcloud, container volumes). <strong>Cannot be changed after creating the destination.</strong>
          </span>
          {#if editing !== null}
            <span class="block text-xs text-warning mt-1 italic">
              Dedup mode is locked at creation time. Create a new destination to switch.
            </span>
          {/if}
        </span>
      </label>
    </div>

    <div class="flex justify-end gap-3 pt-4 border-t border-border">
      <button type="button" onclick={() => showModal = false} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
        Cancel
      </button>
      <button type="submit" disabled={saving} class="px-4 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
        {#if saving}Saving...{:else}{editing ? 'Save Changes' : 'Add Storage'}{/if}
      </button>
    </div>
  </form>
</Modal>

<!-- Enhanced Delete Dialog -->
{#if confirmDelete.show}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm animate-backdrop"
    onclick={(e) => { if (e.target === e.currentTarget) confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 } }}
    onkeydown={(e) => { if (e.key === 'Escape') confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 } }}
    role="dialog" aria-modal="true" aria-labelledby="del-storage-title" tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 p-6 animate-panel-up">
      <h2 id="del-storage-title" class="text-lg font-semibold text-text">Delete Storage</h2>
      <p class="text-sm text-text-muted mt-2">Are you sure you want to delete <strong class="text-text">{confirmDelete.name}</strong>?</p>

      {#if confirmDelete.jobCount > 0}
        <div class="mt-3 p-3 bg-warning/10 border border-warning/30 rounded-lg">
          <p class="text-sm text-warning font-medium flex items-center gap-1.5"><svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/></svg> {confirmDelete.jobCount} job{confirmDelete.jobCount !== 1 ? 's' : ''} use{confirmDelete.jobCount === 1 ? 's' : ''} this storage</p>
          <p class="text-xs text-text-dim mt-1">Those jobs will no longer have a storage destination and will fail to run.</p>
        </div>
      {/if}

      <div class="mt-4 space-y-2">
        <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors {!confirmDelete.deleteFiles ? 'border-vault bg-vault/5' : 'border-border hover:border-vault/30'}">
          <input type="radio" name="delStorageMode" checked={!confirmDelete.deleteFiles}
            onchange={() => confirmDelete.deleteFiles = false}
            class="mt-0.5 accent-vault" />
          <div>
            <p class="text-sm font-medium text-text">Keep backup files</p>
            <p class="text-xs text-text-dim mt-0.5">Only remove the storage destination from Vault. Files remain on storage.</p>
          </div>
        </label>
        <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors {confirmDelete.deleteFiles ? 'border-danger bg-danger/5' : 'border-border hover:border-danger/30'}">
          <input type="radio" name="delStorageMode" checked={confirmDelete.deleteFiles}
            onchange={() => confirmDelete.deleteFiles = true}
            class="mt-0.5 accent-[var(--color-danger)]" />
          <div>
            <p class="text-sm font-medium text-text">Delete all backup files</p>
            <p class="text-xs text-text-dim mt-0.5">Remove the storage destination <strong class="text-danger">and permanently delete all Vault backup files</strong> on it.</p>
          </div>
        </label>
      </div>

      <div class="flex justify-end gap-3 mt-6">
        <button type="button" onclick={() => { confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 } }}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
          Cancel
        </button>
        <button type="button" onclick={doDeleteStorage}
          class="px-4 py-2 text-sm font-medium rounded-lg transition-colors bg-danger text-white hover:bg-danger/90">
          {confirmDelete.deleteFiles ? 'Delete Storage & Files' : 'Delete Storage'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Import Backups Modal -->
<Modal show={showImport} title={`Import Backups — ${importStorageName}`} onclose={() => showImport = false}>
  <!-- Subfolder field — always visible so users can rescan with a different path -->
  <div class="mb-4">
    <label for="import-base-path" class="block text-xs font-medium text-text-muted mb-1">
      Subfolder <span class="font-normal text-text-dim">(optional — leave blank to scan the storage root)</span>
    </label>
    <div class="flex gap-2">
      <input
        id="import-base-path"
        type="text"
        bind:value={importBasePath}
        placeholder="e.g. appdata-backups or appdata/ab_archives"
        class="flex-1 px-3 py-2 text-sm bg-surface-3 border border-border rounded-lg text-text placeholder-text-dim focus:outline-none focus:border-vault"
        onkeydown={(e) => e.key === 'Enter' && !scanning && scanStorage()}
      />
      <button
        type="button"
        onclick={scanStorage}
        disabled={scanning}
        class="px-3 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed shrink-0"
      >
        {scanning ? 'Scanning…' : 'Scan'}
      </button>
    </div>
    <p class="text-xs text-text-dim mt-1">
      If your AppData Backup plugin stores backups in a subfolder (the <em>Destination</em> field in its settings), enter that path here.
    </p>
  </div>

  {#if scanning}
    <Spinner text="Scanning storage for backups..." />
  {:else if scannedBackups.length === 0}
    <EmptyState title="No backups found" description="No backup manifests were found. Try entering the subfolder where your AppData Backup archives are stored above.">
      {#snippet iconSlot()}
        <svg aria-hidden="true" class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
      {/snippet}
    </EmptyState>
    <div class="flex justify-end mt-4">
      <button type="button" onclick={() => showImport = false}
        class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
        Close
      </button>
    </div>
  {:else}
    <div class="space-y-4">
      <p class="text-sm text-text-muted">Found <strong class="text-text">{scannedBackups.length}</strong> backup{scannedBackups.length !== 1 ? 's' : ''} on storage. Select which to import.</p>

      <!-- Select All -->
      <label class="flex items-center gap-2 text-sm text-text-muted cursor-pointer">
        <input type="checkbox" checked={selectedBackups.size === scannedBackups.length}
          onchange={toggleAllBackups} class="accent-vault" />
        Select All
      </label>

      <!-- Backup list -->
      <div class="max-h-72 overflow-y-auto space-y-2 border border-border rounded-lg p-2">
        {#each scannedBackups as backup, idx (idx)}
          <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors {selectedBackups.has(idx) ? 'border-vault/50 bg-vault/5' : 'border-border hover:border-vault/30'}">
            <input type="checkbox" checked={selectedBackups.has(idx)}
              onchange={() => toggleBackup(idx)} class="mt-0.5 accent-vault" />
            <div class="flex-1 min-w-0">
              <div class="flex items-center justify-between">
                <p class="text-sm font-medium text-text truncate">{backup.job_name || 'Unknown Job'}</p>
                <span class="text-xs text-text-dim shrink-0 ml-2">{formatSize(backup.size_bytes)}</span>
              </div>
              <div class="flex flex-wrap gap-x-3 mt-1 text-xs text-text-dim">
                <span>{backup.backup_type || 'full'}</span>
                <span>{backup.compression || 'none'}</span>
                {#if backup.encryption && backup.encryption !== 'none'}<span class="inline-flex items-center gap-1"><svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/></svg> {backup.encryption}</span>{/if}
                {#if backup.created_at}<span>{formatDate(backup.created_at)}</span>{/if}
              </div>
              <p class="text-xs text-text-dim mt-0.5 truncate font-mono">{backup.storage_path}</p>
            </div>
          </label>
        {/each}
      </div>

      <!-- Restore DB option -->
      {#if vaultDBInfo}
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Restore Full Database
          </summary>
          <div class="mt-3 pl-6 space-y-2">
            <p class="text-xs text-text-dim">Restoring the database will replace <strong>all</strong> current data (jobs, history, settings).</p>
            <button
              type="button"
              onclick={() => doRestoreDB(vaultDBInfo.path)}
              class="w-full text-left px-3 py-2 text-xs rounded-lg border border-border hover:border-warning/50 hover:bg-warning/5 transition-colors"
            >
              <span class="font-medium text-text">Vault Database</span>
              <span class="text-text-dim ml-2">{vaultDBInfo.modified_at ? formatDate(vaultDBInfo.modified_at) : ''}</span>
            </button>
          </div>
        </details>
      {/if}

      <div class="flex justify-end gap-3 pt-4 border-t border-border">
        <button type="button" onclick={() => showImport = false}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
          Cancel
        </button>
        <button
          type="button"
          onclick={doImport}
          disabled={selectedBackups.size === 0 || importing}
          class="px-4 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {#if importing}Importing...{:else}Import {selectedBackups.size} Backup{selectedBackups.size !== 1 ? 's' : ''}{/if}
        </button>
      </div>
    </div>
  {/if}
</Modal>
