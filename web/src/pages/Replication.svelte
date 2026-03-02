<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { formatDate } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import ScheduleBuilder from '../components/ScheduleBuilder.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'

  let loading = $state(true)
  let sources = $state([])
  let destinations = $state([])
  let showModal = $state(false)
  let editing = $state(null)
  let testing = $state(null)
  let testResult = $state(null)
  let syncing = $state(null)
  let toast = $state({ message: '', type: 'info', key: 0 })
  let confirmDelete = $state({ show: false, id: 0, name: '' })
  let expandedSource = $state(null)
  let replicatedJobs = $state([])
  let loadingJobs = $state(false)

  let form = $state(defaultForm())

  function defaultForm() {
    return {
      name: '',
      url: '',
      api_key: '',
      storage_dest_id: 0,
      schedule: '0 3 * * *',
      enabled: true,
    }
  }

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(() => {
    loadData()
    // Subscribe to WS events — refresh when syncs complete
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'replication_sync_completed' || msg.type === 'replication_sync_failed') {
        loadData()
      }
    })
    return unsub
  })

  async function loadData() {
    loading = true
    try {
      const [srcs, dests] = await Promise.all([
        api.listReplicationSources(),
        api.listStorage(),
      ])
      sources = srcs || []
      destinations = dests || []
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

  function openEdit(src) {
    editing = src
    form = {
      name: src.name,
      url: src.url,
      api_key: '', // don't prefill sealed key
      storage_dest_id: src.storage_dest_id,
      schedule: src.schedule || '0 3 * * *',
      enabled: src.enabled,
    }
    showModal = true
  }

  async function saveSource() {
    try {
      const payload = { ...form }
      if (editing) {
        // If api_key is empty, the backend keeps the existing one
        await api.updateReplicationSource(editing.id, payload)
        showToast('Target updated', 'success')
      } else {
        if (!payload.api_key) {
          showToast('API key is required', 'error')
          return
        }
        await api.createReplicationSource(payload)
        showToast('Target created', 'success')
      }
      showModal = false
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function deleteSource() {
    try {
      await api.deleteReplicationSource(confirmDelete.id)
      confirmDelete = { show: false, id: 0, name: '' }
      showToast('Target deleted', 'success')
      if (expandedSource === confirmDelete.id) expandedSource = null
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function testConnection(id) {
    testing = id
    testResult = null
    try {
      await api.testReplicationSource(id)
      testResult = { id, success: true }
      showToast('Connection successful', 'success')
    } catch (e) {
      testResult = { id, success: false, error: e.message }
      showToast(e.message, 'error')
    } finally {
      testing = null
    }
  }

  async function syncNow(id) {
    syncing = id
    try {
      await api.syncReplicationSource(id)
      showToast('Sync started', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      syncing = null
    }
  }

  async function toggleExpand(id) {
    if (expandedSource === id) {
      expandedSource = null
      return
    }
    expandedSource = id
    loadingJobs = true
    try {
      replicatedJobs = (await api.listReplicatedJobs(id)) || []
    } catch (e) {
      showToast(e.message, 'error')
      replicatedJobs = []
    } finally {
      loadingJobs = false
    }
  }

  function destName(id) {
    const d = destinations.find(d => d.id === id)
    return d?.name || `Storage #${id}`
  }

  function statusBadge(src) {
    if (!src.enabled) return { label: 'Disabled', cls: 'bg-surface-3 text-text-muted' }
    if (src.last_sync_status === 'success') return { label: 'Synced', cls: 'bg-success/10 text-success' }
    if (src.last_sync_status === 'error') return { label: 'Error', cls: 'bg-danger/10 text-danger' }
    if (src.last_sync_status === 'running') return { label: 'Syncing', cls: 'bg-warning/10 text-warning' }
    return { label: 'Pending', cls: 'bg-surface-3 text-text-muted' }
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Replication</h1>
      <p class="text-sm text-text-muted mt-1">Replicate backups from remote Vault servers for disaster recovery</p>
    </div>
    <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
      <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
      Add Target
    </button>
  </div>

  {#if loading}
    <Spinner text="Loading replication targets..." />
  {:else if sources.length === 0}
    <EmptyState icon="🔄" title="No replication targets" description="Add a remote Vault server to replicate backups for disaster recovery." actionLabel="Add Target" onaction={() => openCreate()} />
  {:else}
    <div class="space-y-4">
      {#each sources as src}
        {@const badge = statusBadge(src)}
        <div class="bg-surface-2 border border-border rounded-xl overflow-hidden hover:border-vault/30 transition-colors">
          <div class="p-5">
            <div class="flex items-start justify-between">
              <div class="flex items-center gap-3">
                <span class="text-2xl">🔄</span>
                <div>
                  <h3 class="font-semibold text-text">{src.name}</h3>
                  <p class="text-xs text-text-dim mt-0.5 font-mono">{src.url}</p>
                </div>
              </div>
              <span class="text-xs font-medium px-2.5 py-1 rounded-full {badge.cls}">{badge.label}</span>
            </div>

            <div class="mt-4 grid grid-cols-2 sm:grid-cols-4 gap-3 text-sm">
              <div>
                <span class="text-text-dim text-xs">Storage</span>
                <p class="text-text font-medium">{destName(src.storage_dest_id)}</p>
              </div>
              <div>
                <span class="text-text-dim text-xs">Schedule</span>
                <p class="text-text font-medium">{src.schedule || 'Manual'}</p>
              </div>
              <div>
                <span class="text-text-dim text-xs">Last Sync</span>
                <p class="text-text font-medium">{src.last_sync_at ? formatDate(src.last_sync_at) : 'Never'}</p>
              </div>
              <div>
                <span class="text-text-dim text-xs">Created</span>
                <p class="text-text font-medium">{formatDate(src.created_at)}</p>
              </div>
            </div>

            {#if src.last_sync_status === 'error' && src.last_sync_error}
              <div class="mt-3 p-2 bg-danger/5 border border-danger/20 rounded-lg text-xs text-danger">
                {src.last_sync_error}
              </div>
            {/if}

            <div class="flex items-center gap-2 mt-4 pt-4 border-t border-border">
              <button onclick={() => syncNow(src.id)} disabled={syncing === src.id}
                class="px-3 py-1.5 bg-vault/10 hover:bg-vault/20 text-vault text-xs font-medium rounded-lg transition-colors disabled:opacity-50 flex items-center gap-1.5">
                {#if syncing === src.id}
                  <svg class="w-3 h-3 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Syncing...
                {:else}
                  <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                  Sync Now
                {/if}
              </button>
              <button onclick={() => testConnection(src.id)} disabled={testing === src.id}
                class="px-3 py-1.5 bg-surface-3 hover:bg-surface-4 text-text text-xs font-medium rounded-lg transition-colors disabled:opacity-50 flex items-center gap-1.5">
                {#if testing === src.id}
                  <svg class="w-3 h-3 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                {:else}
                  <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
                {/if}
                Test
              </button>
              {#if testResult?.id === src.id}
                <span class="text-xs {testResult.success ? 'text-success' : 'text-danger'}">
                  {testResult.success ? '✓ Connected' : `✗ ${testResult.error}`}
                </span>
              {/if}
              <div class="ml-auto flex items-center gap-2">
                <button onclick={() => toggleExpand(src.id)}
                  class="px-3 py-1.5 bg-surface-3 hover:bg-surface-4 text-text text-xs font-medium rounded-lg transition-colors flex items-center gap-1.5">
                  <svg class="w-3 h-3 transition-transform {expandedSource === src.id ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
                  Jobs
                </button>
                <button onclick={() => openEdit(src)}
                  class="px-3 py-1.5 bg-surface-3 hover:bg-surface-4 text-text text-xs font-medium rounded-lg transition-colors"
                  aria-label="Edit replication target">
                  Edit
                </button>
                <button onclick={() => confirmDelete = { show: true, id: src.id, name: src.name }}
                  class="px-3 py-1.5 bg-danger/10 hover:bg-danger/20 text-danger text-xs font-medium rounded-lg transition-colors"
                  aria-label="Delete replication target">
                  Delete
                </button>
              </div>
            </div>
          </div>

          {#if expandedSource === src.id}
            <div class="border-t border-border bg-surface px-5 py-4">
              {#if loadingJobs}
                <div class="flex items-center gap-2 text-sm text-text-muted">
                  <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Loading replicated jobs...
                </div>
              {:else if replicatedJobs.length === 0}
                <p class="text-sm text-text-muted">No jobs replicated yet. Run a sync to pull jobs from the remote server.</p>
              {:else}
                <div class="space-y-2">
                  <h4 class="text-xs font-semibold text-text-dim uppercase tracking-wider">Replicated Jobs ({replicatedJobs.length})</h4>
                  {#each replicatedJobs as job}
                    <div class="flex items-center justify-between py-2 px-3 bg-surface-2 rounded-lg">
                      <div class="flex items-center gap-2">
                        <span class="text-sm">{job.backup_type === 'container' ? '🐳' : job.backup_type === 'vm' ? '🖥️' : '📁'}</span>
                        <span class="text-sm font-medium text-text">{job.name}</span>
                      </div>
                      <span class="text-xs text-text-muted">{job.backup_type}</span>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Create/Edit Modal -->
<Modal show={showModal} title={editing ? 'Edit Replication Target' : 'Add Replication Target'} onclose={() => showModal = false}>
    <form onsubmit={(e) => { e.preventDefault(); saveSource() }} class="space-y-4">
      <div>
        <label for="repl-name" class="block text-sm font-medium text-text mb-1">Name</label>
        <input id="repl-name" type="text" required bind:value={form.name} placeholder="e.g. Production Server"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
      </div>

      <div>
        <label for="repl-url" class="block text-sm font-medium text-text mb-1">Remote Vault URL</label>
        <input id="repl-url" type="url" required bind:value={form.url} placeholder="http://192.168.1.100:24085"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
        <p class="text-xs text-text-dim mt-1">The base URL of the remote Vault server (include port)</p>
      </div>

      <div>
        <label for="repl-key" class="block text-sm font-medium text-text mb-1">
          API Key {editing ? '(leave blank to keep current)' : ''}
        </label>
        <input id="repl-key" type="password" bind:value={form.api_key} placeholder={editing ? '••••••••' : 'Enter API key'}
          required={!editing}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
      </div>

      <div>
        <label for="repl-storage" class="block text-sm font-medium text-text mb-1">Local Storage Destination</label>
        <select id="repl-storage" required bind:value={form.storage_dest_id}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault">
          <option value={0} disabled>Select storage...</option>
          {#each destinations as dest}
            <option value={dest.id}>{dest.name} ({dest.type})</option>
          {/each}
        </select>
        <p class="text-xs text-text-dim mt-1">Where replicated backups will be stored locally</p>
      </div>

      <div>
        <label class="block text-sm font-medium text-text mb-1">Sync Schedule</label>
        <ScheduleBuilder bind:value={form.schedule} />
      </div>

      <div class="flex items-center gap-2">
        <input id="repl-enabled" type="checkbox" bind:checked={form.enabled}
          class="w-4 h-4 rounded border-border text-vault focus:ring-vault/50" />
        <label for="repl-enabled" class="text-sm text-text">Enable scheduled syncing</label>
      </div>

      <div class="flex justify-end gap-3 pt-2">
        <button type="button" onclick={() => showModal = false}
          class="px-4 py-2 bg-surface-3 hover:bg-surface-4 text-text text-sm font-medium rounded-lg transition-colors">
          Cancel
        </button>
        <button type="submit"
          class="btn btn-primary">
          {editing ? 'Update' : 'Add Target'}
        </button>
      </div>
    </form>
  </Modal>

<!-- Confirm Delete -->
<ConfirmDialog
  show={confirmDelete.show}
  title="Delete Replication Target"
  message="Are you sure you want to delete '{confirmDelete.name}'? All replicated jobs from this target will also be removed."
  confirmText="Delete"
  danger={true}
  onconfirm={deleteSource}
  oncancel={() => confirmDelete = { show: false, id: 0, name: '' }}
/>
