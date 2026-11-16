<script>
  import { onMount } from 'svelte'
  import { SvelteMap } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { getRawHash } from '../lib/router.svelte.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { handleProgressMessage, restoreFromStatus } from '../lib/progress.svelte.js'
  import { outcomeFromCompletion, waitForRestoreOutcome } from '../lib/restore-sync.js'
  import Toast from '../components/Toast.svelte'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import RestoreRequestList from '../components/RestoreRequestList.svelte'
  import RestoreRequestReview from '../components/RestoreRequestReview.svelte'
  import RestoreWizard from '../components/RestoreWizard.svelte'
  import ActiveMounts from '../components/ActiveMounts.svelte'
  import { refreshMounts } from '../lib/mounts.svelte.js'

  let loading = $state(true)
  let jobs = $state([])
  let toast = $state({ message: '', type: 'info', key: 0 })
  let destroyed = false
  const restoreRuns = new SvelteMap()

  // Parse query params from hash route (e.g. #/restore?job=1 or
  // #/restore?type=container&name=plex from the Dashboard / command palette).
  function getInitialParams() {
    const raw = getRawHash()
    const qIdx = raw.indexOf('?')
    if (qIdx === -1) return {}
    const params = new URLSearchParams(raw.slice(qIdx))
    return { job: params.get('job'), type: params.get('type'), name: params.get('name'), request: params.get('request') }
  }

  let requestId = $derived(getInitialParams().request)
  const initialParams = getInitialParams()
  const initialJobId = initialParams.job ?? null
  const initialType = initialParams.type ?? null
  const initialName = initialParams.name ?? null

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  function rememberRestoreRun({ job_id: jobId, run_id: runId, run_type: runType }) {
    if (runType !== 'restore' || jobId == null || runId == null) return
    const jobKey = String(jobId)
    let tracked = restoreRuns.get(jobKey)
    // A delayed event for an older run must not displace the latest run for this job.
    if (tracked && Number(runId) < Number(tracked.runId)) return
    if (tracked?.runId !== String(runId)) {
      tracked = { jobId, runId: String(runId), resolving: false, reported: false }
      restoreRuns.set(jobKey, tracked)
    }
    return tracked
  }

  function showRestoreOutcome({ status, done, total, failed }, jobId) {
    const job = jobs.find(j => j.id === jobId)
    const jobSuffix = job?.name ? ` for ${job.name}` : jobId ? ` (Job #${jobId})` : ''
    if (status === 'completed') {
      showToast(`Restore completed successfully${jobSuffix} (${done}/${total} items restored)`, 'success')
    } else if (status === 'partial') {
      showToast(`Restore partially completed${jobSuffix} (${done}/${total} restored, ${failed} failed)`, 'warning')
    } else if (status === 'cancelled') {
      showToast(`Restore cancelled${jobSuffix}. The restore location may be partly restored.`, 'warning')
    } else {
      showToast(`Restore failed${jobSuffix} (${failed} items failed)`, 'error')
    }
  }

  function reportRestoreOutcome(tracked, outcome) {
    tracked.reported = true
    showRestoreOutcome(outcome, tracked.jobId)
  }

  async function resolveRestoreOutcome(tracked) {
    if (tracked.resolving) return
    tracked.resolving = true
    const jobKey = String(tracked.jobId)
    const isCurrent = () => !destroyed && !tracked.reported && restoreRuns.get(jobKey) === tracked
    try {
      const outcome = await waitForRestoreOutcome(api.getRun, tracked, isCurrent)
      if (outcome && isCurrent()) reportRestoreOutcome(tracked, outcome)
    } finally {
      tracked.resolving = false
    }
  }

  function handleRestoreCompletion(msg) {
    const { job_id: jobId, run_id: runId } = msg
    const outcome = outcomeFromCompletion(msg)
    if (jobId == null || runId == null) {
      if (outcome) showRestoreOutcome(outcome, jobId)
      return
    }
    const tracked = rememberRestoreRun(msg)
    if (!tracked || tracked.reported) return
    if (outcome) {
      reportRestoreOutcome(tracked, outcome)
    } else {
      void resolveRestoreOutcome(tracked)
    }
  }

  onMount(() => {
    loadJobs()
    refreshMounts()
    api.getRunnerStatus().then(status => {
      restoreFromStatus(status)
    }).catch(() => {})
    const unsub = onWsMessage((msg) => {
      handleProgressMessage(msg)
      if (msg.type === 'job_run_completed') {
        loadJobs()
        if (msg.run_type === 'restore') handleRestoreCompletion(msg)
      } else if (msg.type === 'job_run_started') {
        rememberRestoreRun(msg)
      } else if (msg.type === 'runner_status_snapshot') {
        if (msg.status?.active) rememberRestoreRun(msg.status)
      } else if (msg.type === 'import_completed') {
        loadJobs()
      }
    })
    return () => {
      destroyed = true
      unsub()
    }
  })

  async function loadJobs() {
    try {
      jobs = (await api.listJobs(true)) || []
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
    }
  }

  async function handleRestore(jobId, payload) {
    const job = jobs.find(j => j.id === jobId)
    const jobLabel = job?.name ? ` in ${job.name}` : ` (Job #${jobId})`
    try {
      await api.restoreJob(jobId, payload)
      const count = payload.items?.length || 1
      const label = count === 1 ? (payload.items?.[0] || payload.item_name) : `${count} items`
      showToast(`Restore started for ${label}${jobLabel}`, 'success')
      return { ok: true }
    } catch (e) {
      showToast(`Restore failed${jobLabel}: ${e.message}`, 'error')
      return { ok: false, error: e.message }
    }
  }

  function handleMounted(session) {
    showToast(`Mounted backup at ${session.mount_path}`, 'success')
  }

  function handleUnmounted(mount) {
    showToast(`Unmounted ${mount.mount_path}`, 'info')
  }

  function handleUnmountError(err) {
    showToast(`Failed to unmount: ${err.message}`, 'error')
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="mb-6">
    <h1 class="text-2xl font-bold text-text">Restore</h1>
    <p class="text-sm text-text-muted mt-1">Browse and restore from backup snapshots</p>
  </div>

  {#if requestId}
    {#key requestId}<RestoreRequestReview {requestId} />{/key}
  {/if}
  <RestoreRequestList />

  {#if !requestId}
    <ActiveMounts onunmount={handleUnmounted} onunmounterror={handleUnmountError} />

    {#if loading}
      <Spinner text="Loading..." />
    {:else if jobs.length === 0}
      <EmptyState title="No backup jobs" description="Create a backup job first, then come back to restore.">
        {#snippet iconSlot()}
          <svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
        {/snippet}
      </EmptyState>
    {:else}
      <RestoreWizard {jobs} onrestore={handleRestore} onmount={handleMounted} {initialJobId} {initialType} {initialName} />
    {/if}
  {/if}
</div>
