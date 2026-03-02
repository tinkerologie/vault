<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatDate, parseConfig } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import PathBrowser from '../components/PathBrowser.svelte'

  let loading = $state(true)
  let destinations = $state([])
  let showModal = $state(false)
  let editing = $state(null)
  let testing = $state(null)
  let testResults = $state(new Map())
  let toast = $state({ message: '', type: 'info', key: 0 })
  let confirmDelete = $state({ show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 })
  let depCounts = $state(new Map())

  // Import state
  let showImport = $state(false)
  let importStorageId = $state(0)
  let importStorageName = $state('')
  let scanning = $state(false)
  let scannedBackups = $state([])
  let selectedBackups = $state(new Set())
  let importing = $state(false)

  let form = $state(defaultForm())

  function defaultForm() {
    return {
      name: '',
      type: 'local',
      config: { path: '' },
    }
  }

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(async () => {
    await loadData()
  })

  async function loadData() {
    loading = true
    try {
      destinations = (await api.listStorage()) || []
      // Load dependent job counts for each storage
      const counts = new Map()
      await Promise.all(destinations.map(async (d) => {
        try {
          const result = await api.getDependentJobs(d.id)
          counts.set(d.id, result?.job_count || 0)
        } catch { counts.set(d.id, 0) }
      }))
      depCounts = counts
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
    form = {
      name: dest.name,
      type: dest.type,
      config: parseConfig(dest.config),
    }
    showModal = true
  }

  async function saveStorage() {
    try {
      const payload = {
        name: form.name,
        type: form.type,
        config: JSON.stringify(form.config),
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
    }
  }

  async function deleteStorage(id, name) {
    // Check for dependent jobs before showing the dialog.
    let jobCount = 0
    try {
      const result = await api.getDependentJobs(id)
      jobCount = result?.job_count || 0
    } catch (_) { /* ignore */ }
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
    scannedBackups = []
    selectedBackups = new Set()
    showImport = true
    await scanStorage()
  }

  async function scanStorage() {
    scanning = true
    try {
      const results = await api.scanStorage(importStorageId)
      scannedBackups = results || []
      selectedBackups = new Set(scannedBackups.map((_, i) => i))
    } catch (e) {
      showToast(`Scan failed: ${e.message}`, 'error')
    } finally {
      scanning = false
    }
  }

  function toggleBackup(idx) {
    const s = new Set(selectedBackups)
    if (s.has(idx)) s.delete(idx)
    else s.add(idx)
    selectedBackups = s
  }

  function toggleAllBackups() {
    if (selectedBackups.size === scannedBackups.length) {
      selectedBackups = new Set()
    } else {
      selectedBackups = new Set(scannedBackups.map((_, i) => i))
    }
  }

  async function doImport() {
    importing = true
    try {
      const backups = scannedBackups.filter((_, i) => selectedBackups.has(i))
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
      const next = new Map(testResults)
      next.set(id, result)
      testResults = next
      if (result.success) {
        showToast('Connection successful!', 'success')
      } else {
        showToast(`Connection failed: ${result.error}`, 'error')
      }
    } catch (e) {
      showToast(e.message, 'error')
      const next = new Map(testResults)
      next.set(id, { success: false, error: e.message })
      testResults = next
    } finally {
      testing = null
    }
  }

  function onTypeChange() {
    const defaults = {
      local: { path: '' },
      sftp: { host: '', port: 22, user: '', password: '', path: '' },
      smb: { host: '', share: '', user: '', password: '', path: '' },
    }
    form.config = defaults[form.type] || {}
  }

  const storageIcons = {
    local: 'M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z',
    sftp: 'M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z',
    smb: 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z',
    nfs: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
  }

  const storageColors = {
    local: 'text-blue-400',
    sftp: 'text-emerald-400',
    smb: 'text-purple-400',
    nfs: 'text-amber-400',
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Storage Destinations</h1>
      <p class="text-sm text-text-muted mt-1">Configure where your backups are stored</p>
    </div>
    <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
      <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
      Add Storage
    </button>
  </div>

  {#if loading}
    <Spinner text="Loading storage..." />
  {:else if destinations.length === 0}
    <EmptyState icon="💾" title="No storage destinations" subtitle="Required before creating jobs" description="Add a storage destination to start backing up your data." actionLabel="Add Storage" onaction={() => openCreate()} />
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each destinations as dest}
        {@const cfg = parseConfig(dest.config)}
        {@const tr = testResults.get(dest.id)}
        {@const jobCount = depCounts.get(dest.id) || 0}
        <div class="bg-surface-2 border border-border rounded-xl p-5 hover:border-vault/30 transition-colors">
          <div class="flex items-start justify-between mb-3">
            <div class="flex items-center gap-3">
              <div class="w-10 h-10 rounded-lg bg-surface-3 flex items-center justify-center">
                <svg class="w-5 h-5 {storageColors[dest.type] || 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={storageIcons[dest.type] || storageIcons.local}/>
                </svg>
              </div>
              <div>
                <h3 class="text-sm font-semibold text-text">{dest.name}</h3>
                <span class="text-xs text-text-dim uppercase">{dest.type}</span>
              </div>
            </div>
            <div class="flex items-center gap-1">
              <button onclick={() => openImport(dest.id, dest.name)} class="p-1.5 text-text-muted hover:text-vault hover:bg-vault/10 rounded-lg transition-colors" title="Import Backups">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12"/></svg>
              </button>
              <button onclick={() => openEdit(dest)} class="p-1.5 text-text-muted hover:text-text hover:bg-surface-3 rounded-lg transition-colors" title="Edit">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/></svg>
              </button>
              <button onclick={() => deleteStorage(dest.id, dest.name)} class="p-1.5 text-text-muted hover:text-danger hover:bg-danger/10 rounded-lg transition-colors" title="Delete">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
              </button>
            </div>
          </div>

          <!-- Config summary -->
          <div class="text-xs text-text-dim space-y-1 mb-4">
            {#if dest.type === 'local'}
              <p>Path: {cfg.path || '—'}</p>
            {:else if dest.type === 'sftp'}
              <p>Host: {cfg.host || '—'}:{cfg.port || 22}</p>
              <p>Path: {cfg.path || '/'}</p>
            {:else if dest.type === 'smb'}
              <p>Share: \\{cfg.host || '—'}\{cfg.share || '—'}</p>
            {/if}
          </div>

          <div class="flex items-center justify-between pt-3 border-t border-border">
            <div class="flex items-center gap-3">
              <span class="text-xs text-text-dim">{formatDate(dest.created_at)}</span>
              {#if jobCount > 0}
                <span class="text-xs px-2 py-0.5 rounded-full bg-vault/10 text-vault font-medium">{jobCount} job{jobCount !== 1 ? 's' : ''}</span>
              {:else}
                <span class="text-xs text-text-dim">No jobs</span>
              {/if}
            </div>
            <button
              onclick={() => testConnection(dest.id)}
              disabled={testing === dest.id}
              class="text-xs px-3 py-1.5 rounded-lg font-medium transition-colors
                {tr
                  ? (tr.success ? 'bg-success/20 text-success' : 'bg-danger/20 text-danger')
                  : 'bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text'}"
            >
              {#if testing === dest.id}
                Testing...
              {:else if tr}
                {tr.success ? '✓ Connected' : '✗ Failed'}
              {:else}
                Test Connection
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
      <select id="stype" bind:value={form.type} onchange={onTypeChange}
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
        <option value="local">Local Path</option>
        <option value="sftp">SFTP</option>
        <option value="smb">SMB / CIFS</option>
      </select>
    </div>

    <!-- Dynamic config fields per type -->
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
          <input id="cfg_port" type="number" bind:value={form.config.port}
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
          <input id="cfg_pass" type="password" bind:value={form.config.password}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="cfg_spath" class="block text-sm font-medium text-text-muted mb-1.5">Remote Path</label>
        <input id="cfg_spath" type="text" bind:value={form.config.path}
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
          <label for="cfg_share" class="block text-sm font-medium text-text-muted mb-1.5">Share</label>
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
          <input id="cfg_smbpass" type="password" bind:value={form.config.password}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
      <div>
        <label for="cfg_smbpath" class="block text-sm font-medium text-text-muted mb-1.5">Path</label>
        <input id="cfg_smbpath" type="text" bind:value={form.config.path}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="vault" />
      </div>
    {/if}

    <div class="flex justify-end gap-3 pt-4 border-t border-border">
      <button type="button" onclick={() => showModal = false} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
        Cancel
      </button>
      <button type="submit" class="px-4 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors">
        {editing ? 'Save Changes' : 'Add Storage'}
      </button>
    </div>
  </form>
</Modal>

<!-- Enhanced Delete Dialog -->
{#if confirmDelete.show}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
    onclick={(e) => { if (e.target === e.currentTarget) confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 } }}
    onkeydown={(e) => { if (e.key === 'Escape') confirmDelete = { show: false, id: 0, name: '', deleteFiles: false, jobCount: 0 } }}
    role="dialog" aria-modal="true" aria-labelledby="del-storage-title" tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
      <h2 id="del-storage-title" class="text-lg font-semibold text-text">Delete Storage</h2>
      <p class="text-sm text-text-muted mt-2">Are you sure you want to delete <strong class="text-text">{confirmDelete.name}</strong>?</p>

      {#if confirmDelete.jobCount > 0}
        <div class="mt-3 p-3 bg-warning/10 border border-warning/30 rounded-lg">
          <p class="text-sm text-warning font-medium">⚠️ {confirmDelete.jobCount} job{confirmDelete.jobCount !== 1 ? 's' : ''} use{confirmDelete.jobCount === 1 ? 's' : ''} this storage</p>
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
  {#if scanning}
    <Spinner text="Scanning storage for backups..." />
  {:else if scannedBackups.length === 0}
    <EmptyState icon="📂" title="No backups found" description="No backup manifests were found on this storage destination." />
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
        {#each scannedBackups as backup, idx}
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
                {#if backup.encryption && backup.encryption !== 'none'}<span>🔒 {backup.encryption}</span>{/if}
                {#if backup.created_at}<span>{new Date(backup.created_at).toLocaleString()}</span>{/if}
              </div>
              <p class="text-xs text-text-dim mt-0.5 truncate font-mono">{backup.storage_path}</p>
            </div>
          </label>
        {/each}
      </div>

      <!-- Restore DB option -->
      {#if scannedBackups.length > 0}
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Restore Full Database
          </summary>
          <div class="mt-3 pl-6 space-y-2">
            <p class="text-xs text-text-dim">Each backup contains a snapshot of the Vault database. Restoring it will replace <strong>all</strong> current data (jobs, history, settings).</p>
            <div class="max-h-40 overflow-y-auto space-y-1">
              {#each scannedBackups as backup}
                <button
                  type="button"
                  onclick={() => doRestoreDB(backup.storage_path)}
                  class="w-full text-left px-3 py-2 text-xs rounded-lg border border-border hover:border-warning/50 hover:bg-warning/5 transition-colors"
                >
                  <span class="font-medium text-text">{backup.job_name}</span>
                  <span class="text-text-dim ml-2">{backup.created_at ? new Date(backup.created_at).toLocaleString() : backup.storage_path}</span>
                </button>
              {/each}
            </div>
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
