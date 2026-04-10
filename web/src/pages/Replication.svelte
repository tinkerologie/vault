<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { getLiveMode } from '../lib/runtime-config.js'
  import { formatDate, describeSchedule } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import ScheduleBuilder from '../components/ScheduleBuilder.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'
  import Tooltip from '../components/Tooltip.svelte'

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

  let modalTesting = $state(false)
  let modalTestResult = $state(null)
  const liveMode = getLiveMode()

  // Cloud OAuth state
  let gdriveConnecting = $state(false)
  let gdriveEmbeddedCreds = $state(false)
  let gdriveShowAdvanced = $state(false)
  let onedriveConnecting = $state(false)
  let onedriveEmbeddedCreds = $state(false)
  let onedriveShowAdvanced = $state(false)

  let form = $state(defaultForm())

  function defaultForm() {
    return {
      name: '',
      type: 'remote_vault',
      url: '',
      config: '{}',
      storage_dest_id: 0,
      schedule: '0 3 * * *',
      enabled: true,
    }
  }

  // Parsed config helper for cloud form bindings.
  let cloudConfig = $derived((() => {
    try { return JSON.parse(form.config || '{}') } catch { return {} }
  })())

  function updateCloudConfig(key, value) {
    const cfg = { ...cloudConfig }
    cfg[key] = value
    form.config = JSON.stringify(cfg)
  }

  function setCloudConfig(obj) {
    form.config = JSON.stringify(obj)
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
    const pollTimer = liveMode === 'poll' ? setInterval(() => { loadData() }, 10000) : null
    return () => {
      unsub()
      if (pollTimer) clearInterval(pollTimer)
    }
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

  async function testModalConnection() {
    if (!form.url) {
      modalTestResult = { success: false, error: 'Enter a URL first' }
      return
    }
    modalTesting = true
    modalTestResult = null
    try {
      const result = await api.testReplicationURL(form.url)
      modalTestResult = {
        success: true,
        version: result.version,
        warning: result.warning,
        message: result.message,
      }
    } catch (e) {
      modalTestResult = { success: false, error: e.message }
    } finally {
      modalTesting = false
    }
  }

  function openCreate() {
    editing = null
    form = defaultForm()
    modalTestResult = null
    gdriveShowAdvanced = false
    onedriveShowAdvanced = false
    showModal = true
  }

  function openEdit(src) {
    editing = src
    form = {
      name: src.name,
      type: src.type || 'remote_vault',
      url: src.url || '',
      config: src.config || '{}',
      storage_dest_id: src.storage_dest_id,
      schedule: src.schedule || '0 3 * * *',
      enabled: src.enabled,
    }
    modalTestResult = null
    gdriveShowAdvanced = false
    onedriveShowAdvanced = false
    if (src.type === 'gdrive') checkGDriveStatus()
    if (src.type === 'onedrive') checkOneDriveStatus()
    showModal = true
  }

  async function saveSource() {
    try {
      const payload = { ...form }
      if (editing) {
        await api.updateReplicationSource(editing.id, payload)
        showToast('Target updated', 'success')
      } else {
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

  function onTypeChange() {
    // Reset type-specific fields when type changes.
    form.url = ''
    form.config = '{}'
    form.storage_dest_id = 0
    modalTestResult = null
    if (form.type === 'gdrive') checkGDriveStatus()
    if (form.type === 'onedrive') checkOneDriveStatus()
  }

  async function checkGDriveStatus() {
    try {
      const status = await api.getGDriveStatus()
      gdriveEmbeddedCreds = status?.configured ?? false
    } catch {
      gdriveEmbeddedCreds = false
    }
  }

  async function connectGDrive() {
    if (!gdriveEmbeddedCreds && (!cloudConfig.client_id || !cloudConfig.client_secret)) {
      showToast('Google Drive is not configured. Ask your Vault administrator to set VAULT_GDRIVE_CLIENT_ID and VAULT_GDRIVE_CLIENT_SECRET, or provide your own credentials under Advanced Settings.', 'error')
      return
    }
    gdriveConnecting = true
    try {
      const redirectUri = window.location.origin + '/api/v1/replication/gdrive/callback'
      const clientId = cloudConfig.client_id || ''
      const clientSecret = cloudConfig.client_secret || ''
      const result = await api.getGDriveAuthUrl(redirectUri, clientId, clientSecret)
      const popup = window.open(result.url, 'gdrive-auth', 'width=600,height=700,scrollbars=yes')
      const code = await new Promise((resolve, reject) => {
        function onMessage(event) {
          // Validate origin and source before processing
          if (event.origin !== window.location.origin) {
            return
          }
          if (event.source !== popup) {
            return
          }
          if (event.data?.type === 'gdrive-auth-code') {
            window.removeEventListener('message', onMessage)
            clearInterval(pollTimer)
            resolve(event.data.code)
          } else if (event.data?.type === 'gdrive-auth-error') {
            window.removeEventListener('message', onMessage)
            clearInterval(pollTimer)
            reject(new Error(event.data.error || 'Authorization failed'))
          }
        }
        window.addEventListener('message', onMessage)
        const pollTimer = setInterval(() => {
          if (popup && popup.closed) {
            clearInterval(pollTimer)
            window.removeEventListener('message', onMessage)
            const manualCode = prompt('If the popup closed without connecting, paste the authorization code here:')
            if (manualCode) resolve(manualCode)
            else reject(new Error('Authorization cancelled'))
          }
        }, 1000)
      })
      const tokenResult = await api.exchangeGDriveToken(code, redirectUri, clientId, clientSecret)
      updateCloudConfig('refresh_token', tokenResult.refresh_token)
      showToast('Google Drive connected successfully!', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      gdriveConnecting = false
    }
  }

  async function checkOneDriveStatus() {
    try {
      const status = await api.getOneDriveStatus()
      onedriveEmbeddedCreds = status?.configured ?? false
    } catch {
      onedriveEmbeddedCreds = false
    }
  }

  async function connectOneDrive() {
    if (!onedriveEmbeddedCreds && (!cloudConfig.client_id || !cloudConfig.client_secret)) {
      showToast('OneDrive is not configured. Ask your Vault administrator to set VAULT_ONEDRIVE_CLIENT_ID and VAULT_ONEDRIVE_CLIENT_SECRET, or provide your own credentials under Advanced Settings.', 'error')
      return
    }
    onedriveConnecting = true
    try {
      const redirectUri = window.location.origin + '/api/v1/replication/onedrive/callback'
      const clientId = cloudConfig.client_id || ''
      const clientSecret = cloudConfig.client_secret || ''
      const result = await api.getOneDriveAuthUrl(redirectUri, clientId, clientSecret)
      const popup = window.open(result.url, 'onedrive-auth', 'width=600,height=700,scrollbars=yes')
      const code = await new Promise((resolve, reject) => {
        function onMessage(event) {
          // Validate origin and source before processing
          if (event.origin !== window.location.origin) {
            return
          }
          if (event.source !== popup) {
            return
          }
          if (event.data?.type === 'onedrive-auth-code') {
            window.removeEventListener('message', onMessage)
            clearInterval(pollTimer)
            resolve(event.data.code)
          } else if (event.data?.type === 'onedrive-auth-error') {
            window.removeEventListener('message', onMessage)
            clearInterval(pollTimer)
            reject(new Error(event.data.error || 'Authorization failed'))
          }
        }
        window.addEventListener('message', onMessage)
        const pollTimer = setInterval(() => {
          if (popup && popup.closed) {
            clearInterval(pollTimer)
            window.removeEventListener('message', onMessage)
            const manualCode = prompt('If the popup closed without connecting, paste the authorization code here:')
            if (manualCode) resolve(manualCode)
            else reject(new Error('Authorization cancelled'))
          }
        }, 1000)
      })
      const tokenResult = await api.exchangeOneDriveToken(code, redirectUri, clientId, clientSecret)
      updateCloudConfig('refresh_token', tokenResult.refresh_token)
      showToast('OneDrive connected successfully!', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      onedriveConnecting = false
    }
  }

  const typeLabels = {
    remote_vault: 'Remote Vault',
    gdrive: 'Google Drive',
    onedrive: 'OneDrive',
  }

  const typeIcons = {
    remote_vault: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15',
    gdrive: 'M12 2L4.5 20.29l.71.71L12 18l6.79 3 .71-.71L12 2z',
    onedrive: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z',
  }

  const typeColors = {
    remote_vault: 'text-vault',
    gdrive: 'text-sky-400',
    onedrive: 'text-cyan-400',
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
      <p class="text-sm text-text-muted mt-1">Replicate backups to remote Vault servers or cloud storage for disaster recovery</p>
    </div>
    {#if sources.length > 0}
      <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
        Add Target
      </button>
    {/if}
  </div>

  {#if loading}
    <Spinner text="Loading replication targets..." />
  {:else if sources.length === 0}
    <EmptyState title="No replication targets" description="Add a remote Vault server, Google Drive, or OneDrive target to replicate backups for disaster recovery." actionLabel="Add Target" onaction={() => openCreate()}>
      {#snippet iconSlot()}
        <svg aria-hidden="true" class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="space-y-4">
      {#each sources as src (src.id)}
        {@const badge = statusBadge(src)}
        <div class="bg-surface-2 border border-border rounded-xl overflow-hidden hover:border-vault/30 hover:shadow-sm transition-all">
          <div class="p-5">
            <div class="flex items-start justify-between">
              <div class="flex items-center gap-3">
                <div class="w-10 h-10 rounded-lg bg-surface-3 flex items-center justify-center">
                  <svg aria-hidden="true" class="w-5 h-5 {typeColors[src.type] || 'text-vault'}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="{typeIcons[src.type] || typeIcons.remote_vault}"/></svg>
                </div>
                <div>
                  <h2 class="font-semibold text-text">{src.name}</h2>
                  <p class="text-xs text-text-dim mt-0.5 font-mono">
                    {#if src.type === 'gdrive'}
                      Google Drive
                    {:else if src.type === 'onedrive'}
                      OneDrive
                    {:else}
                      {src.url}
                    {/if}
                  </p>
                </div>
              </div>
              <span class="text-xs font-medium px-2.5 py-1 rounded-full {badge.cls}">{badge.label}</span>
            </div>

            <div class="mt-4 grid grid-cols-2 sm:grid-cols-4 gap-3 text-sm">
              <div>
                <span class="text-text-dim text-xs">Type</span>
                <p class="text-text font-medium">{typeLabels[src.type] || 'Remote Vault'}</p>
              </div>
              <div>
                <span class="text-text-dim text-xs">Schedule</span>
                <p class="text-text font-medium">{describeSchedule(src.schedule)}</p>
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
                  <svg aria-hidden="true" class="w-3 h-3 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Syncing...
                {:else}
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                  Sync Now
                {/if}
              </button>
              <button onclick={() => testConnection(src.id)} disabled={testing === src.id}
                class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors disabled:opacity-50 flex items-center gap-1.5
                  {testResult?.id === src.id ? (testResult.success ? 'bg-success/20 text-success' : 'bg-danger/20 text-danger') : 'bg-surface-3 hover:bg-surface-4 text-text'}">
                {#if testing === src.id}
                  <svg aria-hidden="true" class="w-3 h-3 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Testing...
                {:else if testResult?.id === src.id}
                  {#if testResult.success}<svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg> Connected{:else}<svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg> Failed{/if}
                {:else}
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
                  Test
                {/if}
              </button>
              <div class="ml-auto flex items-center gap-2">
                <button onclick={() => toggleExpand(src.id)}
                  class="px-3 py-1.5 bg-surface-3 hover:bg-surface-4 text-text text-xs font-medium rounded-lg transition-colors flex items-center gap-1.5">
                  <svg aria-hidden="true" class="w-3 h-3 transition-transform {expandedSource === src.id ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
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
                  <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Loading replicated jobs...
                </div>
              {:else if replicatedJobs.length === 0}
                <p class="text-sm text-text-muted">No jobs replicated yet. Run a sync to pull jobs from the remote server.</p>
              {:else}
                <div class="space-y-2">
                  <h3 class="text-xs font-semibold text-text-dim uppercase tracking-wider">Replicated Jobs ({replicatedJobs.length})</h3>
                  {#each replicatedJobs as job (job.name)}
                    <div class="flex items-center justify-between py-2 px-3 bg-surface-2 rounded-lg">
                      <div class="flex items-center gap-2">
                        <svg aria-hidden="true" class="w-4 h-4 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                          {#if job.backup_type === 'container'}
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4"/>
                          {:else if job.backup_type === 'vm'}
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/>
                          {:else}
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
                          {/if}
                        </svg>
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

      <!-- Target Type Selector -->
      <div>
        <label for="repl-type" class="block text-sm font-medium text-text mb-1">Target Type</label>
        <select id="repl-type" bind:value={form.type} onchange={onTypeChange} disabled={!!editing}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault">
          <option value="remote_vault">Remote Vault Server</option>
          <option value="gdrive">Google Drive</option>
          <option value="onedrive">OneDrive</option>
        </select>
      </div>

      <!-- Remote Vault Fields -->
      {#if form.type === 'remote_vault'}
        <div>
          <label for="repl-url" class="block text-sm font-medium text-text mb-1">Remote Vault URL</label>
          <input id="repl-url" type="url" required bind:value={form.url} placeholder="http://192.168.1.100:24085"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-text text-sm placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
          <p class="text-xs text-text-dim mt-1">The base URL of the remote Vault server (include port)</p>
        </div>

        <!-- Test Connection -->
        <div class="flex items-center gap-3">
          <button type="button" onclick={testModalConnection} disabled={modalTesting || !form.url}
            class="px-3 py-1.5 bg-surface-3 hover:bg-surface-4 text-text text-xs font-medium rounded-lg transition-colors disabled:opacity-50 flex items-center gap-1.5">
            {#if modalTesting}
              <svg aria-hidden="true" class="w-3 h-3 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
              Testing...
            {:else}
              <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
              Test Connection
            {/if}
          </button>
          {#if modalTestResult}
            <span class="text-xs {modalTestResult.success ? (modalTestResult.warning ? 'text-warning' : 'text-success') : 'text-danger'}">
              {#if modalTestResult.success && modalTestResult.warning}
                <svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/></svg> {modalTestResult.message || (modalTestResult.version ? `Connected (v${modalTestResult.version})` : 'Connection validated')} — {modalTestResult.warning}
              {:else if modalTestResult.success}
                <svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg> {modalTestResult.message || (modalTestResult.version ? `Connected (v${modalTestResult.version})` : 'Connection validated')}
              {:else}
                <svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg> {modalTestResult.error}
              {/if}
            </span>
          {/if}
        </div>

      <!-- Google Drive Fields -->
      {:else if form.type === 'gdrive'}
        <div class="space-y-3">
          <div class="flex items-center gap-3">
            {#if cloudConfig.refresh_token}
              <div class="flex items-center gap-2 text-sm text-success">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
                Connected to Google Drive
              </div>
              <button type="button" onclick={() => { updateCloudConfig('refresh_token', ''); }}
                class="text-xs text-text-muted hover:text-danger transition-colors">Disconnect</button>
            {:else}
              <button type="button" onclick={connectGDrive} disabled={gdriveConnecting}
                class="px-4 py-2 text-sm font-medium text-white bg-sky-600 hover:bg-sky-700 rounded-lg transition-colors disabled:opacity-40 flex items-center gap-2">
                {#if gdriveConnecting}
                  <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                  Connecting...
                {:else}
                  <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 3h4a2 2 0 012 2v14a2 2 0 01-2 2h-4M10 17l5-5-5-5M13.8 12H3"/></svg>
                  Sign in with Google
                {/if}
              </button>
              {#if !gdriveEmbeddedCreds && !cloudConfig.client_id}
                <span class="text-xs text-warning">Requires credentials — see Advanced Settings below</span>
              {:else}
                <span class="text-xs text-text-dim">Sign in to authorize Vault</span>
              {/if}
            {/if}
          </div>
          <div>
            <label for="cfg_folderid" class="block text-sm font-medium text-text-muted mb-1.5">Folder ID <span class="text-text-dim font-normal">(optional, blank for root)</span></label>
            <input id="cfg_folderid" type="text" value={cloudConfig.folder_id || ''} oninput={(e) => updateCloudConfig('folder_id', e.target.value)}
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="Google Drive folder ID" />
          </div>
          <div class="pt-2 border-t border-border">
            <button type="button" onclick={() => { gdriveShowAdvanced = !gdriveShowAdvanced }}
              class="text-xs text-text-muted hover:text-text flex items-center gap-1 transition-colors">
              <svg class="w-3 h-3 transition-transform {gdriveShowAdvanced ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              Advanced Settings
            </button>
            {#if gdriveShowAdvanced}
              <div class="mt-3 space-y-3 pl-4 border-l-2 border-border">
                <p class="text-xs text-text-dim">Provide your own OAuth credentials from the <a href="https://console.cloud.google.com/apis/credentials" target="_blank" rel="noopener noreferrer" class="text-vault hover:underline">Google Cloud Console</a>. Leave blank to use the server's built-in credentials.</p>
                <div>
                  <label for="cfg_clientid" class="block text-sm font-medium text-text-muted mb-1.5">Client ID</label>
                  <input id="cfg_clientid" type="text" value={cloudConfig.client_id || ''} oninput={(e) => updateCloudConfig('client_id', e.target.value)}
                    class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="your-app.apps.googleusercontent.com" />
                </div>
                <div>
                  <label for="cfg_clientsecret" class="block text-sm font-medium text-text-muted mb-1.5">Client Secret</label>
                  <input id="cfg_clientsecret" type="password" value={cloudConfig.client_secret || ''} oninput={(e) => updateCloudConfig('client_secret', e.target.value)}
                    class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
                </div>
              </div>
            {/if}
          </div>
        </div>

      <!-- OneDrive Fields -->
      {:else if form.type === 'onedrive'}
        <div class="space-y-3">
          <div class="flex items-center gap-3">
            {#if cloudConfig.refresh_token}
              <div class="flex items-center gap-2 text-sm text-success">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
                Connected to OneDrive
              </div>
              <button type="button" onclick={() => { updateCloudConfig('refresh_token', ''); }}
                class="text-xs text-text-muted hover:text-danger transition-colors">Disconnect</button>
            {:else}
              <button type="button" onclick={connectOneDrive} disabled={onedriveConnecting}
                class="px-4 py-2 text-sm font-medium text-white bg-cyan-600 hover:bg-cyan-700 rounded-lg transition-colors disabled:opacity-40 flex items-center gap-2">
                {#if onedriveConnecting}
                  <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                  Connecting...
                {:else}
                  <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 3h4a2 2 0 012 2v14a2 2 0 01-2 2h-4M10 17l5-5-5-5M13.8 12H3"/></svg>
                  Sign in with Microsoft
                {/if}
              </button>
              {#if !onedriveEmbeddedCreds && !cloudConfig.client_id}
                <span class="text-xs text-warning">Requires credentials — see Advanced Settings below</span>
              {:else}
                <span class="text-xs text-text-dim">Sign in to authorize Vault</span>
              {/if}
            {/if}
          </div>
          <div>
            <label for="cfg_od_folderpath" class="block text-sm font-medium text-text-muted mb-1.5">Folder Path <span class="text-text-dim font-normal">(optional, blank for root)</span></label>
            <input id="cfg_od_folderpath" type="text" value={cloudConfig.folder_path || ''} oninput={(e) => updateCloudConfig('folder_path', e.target.value)}
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="Vault/Backups" />
          </div>
          <div class="pt-2 border-t border-border">
            <button type="button" onclick={() => { onedriveShowAdvanced = !onedriveShowAdvanced }}
              class="text-xs text-text-muted hover:text-text flex items-center gap-1 transition-colors">
              <svg class="w-3 h-3 transition-transform {onedriveShowAdvanced ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              Advanced Settings
            </button>
            {#if onedriveShowAdvanced}
              <div class="mt-3 space-y-3 pl-4 border-l-2 border-border">
                <p class="text-xs text-text-dim">Provide your own OAuth credentials from the <a href="https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade" target="_blank" rel="noopener noreferrer" class="text-vault hover:underline">Azure App Registrations</a>. Leave blank to use the server's built-in credentials.</p>
                <div>
                  <label for="cfg_od_clientid" class="block text-sm font-medium text-text-muted mb-1.5">Application (client) ID</label>
                  <input id="cfg_od_clientid" type="text" value={cloudConfig.client_id || ''} oninput={(e) => updateCloudConfig('client_id', e.target.value)}
                    class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" />
                </div>
                <div>
                  <label for="cfg_od_clientsecret" class="block text-sm font-medium text-text-muted mb-1.5">Client Secret</label>
                  <input id="cfg_od_clientsecret" type="password" value={cloudConfig.client_secret || ''} oninput={(e) => updateCloudConfig('client_secret', e.target.value)}
                    class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
                </div>
              </div>
            {/if}
          </div>
        </div>
      {/if}

      <div>
        <span class="block text-sm font-medium text-text mb-1">Sync Schedule <Tooltip text="Controls how frequently Vault syncs restore points to the replication target." /></span>
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
  confirmLabel="Delete"
  variant="danger"
  onconfirm={deleteSource}
  oncancel={() => confirmDelete = { show: false, id: 0, name: '' }}
/>