<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatDate, formatBytes } from '../lib/utils.js'
  import Toast from '../components/Toast.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import PathBrowser from '../components/PathBrowser.svelte'

  let loading = $state(true)
  let jobs = $state([])
  let selectedJobId = $state(0)
  let restorePoints = $state([])
  let jobItems = $state([])
  let loadingPoints = $state(false)
  let restoringId = $state(0)
  let selectedItemName = $state('')
  let selectedItemType = $state('')
  let restoreDestination = $state('')
  let showDestOverride = $state(false)
  let restorePassphrase = $state('')
  let toast = $state({ message: '', type: 'info', key: 0 })
  let confirmRestore = $state({ show: false, rp: null })

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(async () => {
    try {
      jobs = (await api.listJobs()) || []
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
    }
  })

  async function loadRestorePoints() {
    if (!selectedJobId) {
      restorePoints = []
      jobItems = []
      return
    }
    loadingPoints = true
    try {
      const [rps, jobData] = await Promise.all([
        api.getRestorePoints(selectedJobId),
        api.getJob(selectedJobId),
      ])
      restorePoints = rps || []
      jobItems = jobData.items || []
      restorePassphrase = ''
      // Auto-select first item
      if (jobItems.length > 0) {
        selectedItemName = jobItems[0].item_name
        selectedItemType = jobItems[0].item_type
      }
    } catch (e) {
      showToast(e.message, 'error')
      restorePoints = []
      jobItems = []
    } finally {
      loadingPoints = false
    }
  }

  $effect(() => {
    if (selectedJobId) loadRestorePoints()
    else {
      restorePoints = []
      jobItems = []
    }
  })

  function parseMetadata(meta) {
    if (!meta) return {}
    try { return JSON.parse(meta) } catch { return {} }
  }

  let selectedJob = $derived(jobs.find(j => j.id === selectedJobId))
  let jobNeedsPassphrase = $derived(selectedJob?.encryption === 'age')
  let showDestOverrideOption = $derived(selectedItemType === 'folder' || selectedItemType === 'container' || selectedItemType === 'vm')

  function destHelpText(type) {
    switch (type) {
      case 'container': return 'Volumes will be extracted under this directory (e.g. /mnt/user/restore/volume_name).'
      case 'vm': return 'Disk images and NVRAM will be copied under this directory. The VM definition will be updated to reference the new paths.'
      default: return 'Leave empty to restore to the original location.'
    }
  }

  async function doRestore(rp) {
    if (restoringId) return
    if (!selectedItemName || !selectedItemType) {
      showToast('Please select an item to restore', 'error')
      return
    }
    restoringId = rp.id
    try {
      const payload = {
        restore_point_id: rp.id,
        item_name: selectedItemName,
        item_type: selectedItemType,
      }
      if (showDestOverride && restoreDestination.trim()) {
        payload.destination = restoreDestination.trim()
      }
      if (restorePassphrase) {
        payload.passphrase = restorePassphrase
      }
      await api.restoreJob(selectedJobId, payload)
      showToast(`Restore started for ${selectedItemName}`, 'success')
    } catch (e) {
      showToast(`Restore failed: ${e.message}`, 'error')
    } finally {
      restoringId = 0
    }
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="mb-6">
    <h1 class="text-2xl font-bold text-text">Restore</h1>
    <p class="text-sm text-text-muted mt-1">Browse and restore from backup snapshots</p>
  </div>

  {#if loading}
    <Spinner text="Loading..." />
  {:else}
    <!-- Job Selector -->
    <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6">
      <label for="restore_job" class="block text-sm font-medium text-text-muted mb-2">Select Backup Job</label>
      <select id="restore_job" bind:value={selectedJobId}
        class="w-full sm:w-80 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
        <option value={0}>— Choose a job —</option>
        {#each jobs as job}
          <option value={job.id}>{job.name}</option>
        {/each}
      </select>
    </div>

    {#if !selectedJobId}
      <EmptyState icon="🔄" title="Select a job" description="Choose a backup job above to see its restore points." />
    {:else if loadingPoints}
      <Spinner text="Loading restore points..." />
    {:else if restorePoints.length === 0}
      <EmptyState icon="📦" title="No restore points" description="This job has no restore points yet. Run a backup first." />
    {:else}
      <!-- Item Selector + Destination Override -->
      {#if jobItems.length > 1 || showDestOverrideOption || jobNeedsPassphrase}
        <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6 space-y-4">
          {#if jobItems.length > 1}
            <div>
              <label for="restore_item" class="block text-sm font-medium text-text-muted mb-2">Select Item to Restore</label>
              <select id="restore_item"
                onchange={(e) => {
                  const item = jobItems.find(i => i.item_name === e.target.value)
                  if (item) { selectedItemName = item.item_name; selectedItemType = item.item_type }
                }}
                class="w-full sm:w-80 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
              >
                {#each jobItems as item}
                  <option value={item.item_name} selected={item.item_name === selectedItemName}>
                    {item.item_name} ({item.item_type})
                  </option>
                {/each}
              </select>
            </div>
          {/if}

          {#if showDestOverrideOption}
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
                <p class="text-xs text-text-dim mt-1">{destHelpText(selectedItemType)}</p>
              {/if}
            </div>
          {/if}

          {#if jobNeedsPassphrase}
            <div>
              <label for="restore_passphrase" class="block text-sm font-medium text-text-muted mb-2">Encryption Passphrase</label>
              <input id="restore_passphrase" type="password" bind:value={restorePassphrase} placeholder="Enter the passphrase used to encrypt these backups" class="w-full sm:w-80 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
              <p class="text-xs text-text-dim mt-1">This job uses age encryption. A passphrase is required to decrypt.</p>
            </div>
          {/if}
        </div>
      {/if}
      <div class="space-y-3">
        {#each restorePoints as rp}
          {@const meta = parseMetadata(rp.metadata)}
          <div class="bg-surface-2 border border-border rounded-xl p-5 hover:border-vault/30 transition-colors">
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
              <div class="flex-1">
                <div class="flex items-center gap-3 mb-2">
                  <span class="text-xs px-2 py-1 rounded bg-vault/10 text-vault font-medium uppercase">{rp.backup_type}</span>
                  <span class="text-xs text-text-dim">Run #{rp.job_run_id}</span>
                </div>
                <p class="text-sm text-text-muted font-mono">{rp.storage_path || '—'}</p>
                <div class="flex items-center gap-4 mt-2 text-xs text-text-dim">
                  <span>{formatBytes(rp.size_bytes)}</span>
                  <span>{formatDate(rp.created_at)}</span>
                  {#if meta.items}
                    <span>{meta.items} items</span>
                  {/if}
                </div>
              </div>
              <button
                class="px-4 py-2 text-sm font-medium text-vault bg-vault/10 hover:bg-vault/20 rounded-lg transition-colors flex items-center gap-2 whitespace-nowrap disabled:opacity-50"
                disabled={restoringId === rp.id}
                onclick={() => { confirmRestore = { show: true, rp } }}
              >
                {#if restoringId === rp.id}
                  <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>
                  Restoring...
                {:else}
                  <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                  Restore
                {/if}
              </button>
            </div>
          </div>
        {/each}
      </div>
      <p class="text-xs text-text-dim mt-3 text-center">{restorePoints.length} restore point{restorePoints.length !== 1 ? 's' : ''}</p>
    {/if}
  {/if}
</div>

<ConfirmDialog
  show={confirmRestore.show}
  title="Confirm Restore"
  message="This will overwrite existing data for {selectedItemName || 'the selected item'}. Are you sure you want to proceed?"
  confirmLabel="Restore"
  variant="warning"
  onconfirm={() => { const rp = confirmRestore.rp; confirmRestore = { show: false, rp: null }; if (rp) doRestore(rp) }}
  oncancel={() => { confirmRestore = { show: false, rp: null } }}
/>
