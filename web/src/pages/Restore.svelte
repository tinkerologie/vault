<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import RestoreWizard from '../components/RestoreWizard.svelte'

  let loading = $state(true)
  let jobs = $state([])
  let toast = $state({ message: '', type: 'info', key: 0 })

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

  async function handleRestore(jobId, payload) {
    try {
      await api.restoreJob(jobId, payload)
      showToast(`Restore started for ${payload.item_name}`, 'success')
    } catch (e) {
      showToast(`Restore failed: ${e.message}`, 'error')
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
  {:else if jobs.length === 0}
    <EmptyState icon="🔄" title="No backup jobs" description="Create a backup job first, then come back to restore." />
  {:else}
    <RestoreWizard {jobs} onrestore={handleRestore} />
  {/if}
</div>
