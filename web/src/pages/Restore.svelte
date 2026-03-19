<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { getRawHash } from '../lib/router.svelte.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import RestoreWizard from '../components/RestoreWizard.svelte'

  let loading = $state(true)
  let jobs = $state([])
  let toast = $state({ message: '', type: 'info', key: 0 })

  // Parse query params from hash route (e.g. #/restore?job=1)
  function getInitialJobId() {
    const raw = getRawHash()
    const qIdx = raw.indexOf('?')
    if (qIdx === -1) return null
    const params = new URLSearchParams(raw.slice(qIdx))
    return params.get('job')
  }

  const initialJobId = getInitialJobId()

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(() => {
    loadJobs()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_completed') {
        loadJobs()
      }
    })
    return unsub
  })

  async function loadJobs() {
    try {
      jobs = (await api.listJobs()) || []
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
    }
  }

  async function handleRestore(jobId, payload) {
    try {
      await api.restoreJob(jobId, payload)
      const count = payload.items?.length || 1
      const label = count === 1 ? (payload.items?.[0] || payload.item_name) : `${count} items`
      showToast(`Restore started for ${label}`, 'success')
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
    <EmptyState title="No backup jobs" description="Create a backup job first, then come back to restore.">
      {#snippet iconSlot()}
        <svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
      {/snippet}
    </EmptyState>
  {:else}
    <RestoreWizard {jobs} onrestore={handleRestore} {initialJobId} />
  {/if}
</div>
