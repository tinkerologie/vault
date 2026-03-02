<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatDate, relTime, formatBytes, statusBadge, getFailureReason } from '../lib/utils.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import Skeleton from '../components/Skeleton.svelte'
  import EmptyState from '../components/EmptyState.svelte'

  let loading = $state(true)
  let error = $state('')
  let jobs = $state([])
  let allRuns = $state([])
  let selectedJob = $state(0)
  let selectedRunType = $state('all')
  let expandedRunIds = $state(new Set())

  onMount(() => {
    loadData()

    // Auto-refresh when backup or restore events arrive via WebSocket
    const unsub = onWsMessage((msg) => {
      if (['job_run_started', 'job_run_completed', 'item_backup_done', 'item_backup_failed', 'item_restore_done', 'item_restore_failed'].includes(msg.type)) {
        loadData()
      }
    })
    return unsub
  })

  async function loadData() {
    loading = true
    try {
      jobs = (await api.listJobs()) || []
      // Fetch history for all jobs
      const promises = jobs.map(async (job) => {
        try {
          const runs = await api.getJobHistory(job.id, 100)
          return (runs || []).map(r => ({ ...r, jobName: job.name }))
        } catch { return [] }
      })
      const results = await Promise.all(promises)
      allRuns = results.flat().sort((a, b) => new Date(b.started_at) - new Date(a.started_at))
    } catch (e) {
      error = e.message || 'Failed to load history'
    } finally {
      loading = false
    }
  }

  const filteredRuns = $derived(
    allRuns
      .filter(r => selectedJob === 0 || r.job_id === selectedJob)
      .filter(r => selectedRunType === 'all' || (r.run_type || 'backup') === selectedRunType)
  )

  function duration(run) {
    if (!run.started_at || !run.completed_at) return '—'
    const start = new Date(run.started_at)
    const end = new Date(run.completed_at)
    const secs = Math.floor((end - start) / 1000)
    if (secs < 60) return `${secs}s`
    const mins = Math.floor(secs / 60)
    const remSecs = secs % 60
    if (mins < 60) return `${mins}m ${remSecs}s`
    const hrs = Math.floor(mins / 60)
    return `${hrs}h ${mins % 60}m`
  }

  function tryParseJSON(str) {
    if (!str) return null
    try { return JSON.parse(str) } catch { return null }
  }

  function toggleRunExpand(runId) {
    const next = new Set(expandedRunIds)
    if (next.has(runId)) next.delete(runId)
    else next.add(runId)
    expandedRunIds = next
  }

  function hasLogDetails(run) {
    return run.log && run.log.trim().length > 0
  }
</script>

<div>
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Backup & Restore History</h1>
      <p class="text-sm text-text-muted mt-1">View past backup and restore runs and their results</p>
    </div>
    <div class="flex items-center gap-3 flex-wrap">
      <!-- Run type filter -->
      <div class="flex rounded-lg border border-border overflow-hidden text-xs">
        <button onclick={() => selectedRunType = 'all'}
          class="px-3 py-1.5 transition-colors {selectedRunType === 'all' ? 'bg-accent text-white' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">All</button>
        <button onclick={() => selectedRunType = 'backup'}
          class="px-3 py-1.5 transition-colors border-l border-border {selectedRunType === 'backup' ? 'bg-accent text-white' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">Backups</button>
        <button onclick={() => selectedRunType = 'restore'}
          class="px-3 py-1.5 transition-colors border-l border-border {selectedRunType === 'restore' ? 'bg-accent text-white' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">Restores</button>
      </div>
      <select bind:value={selectedJob}
        class="px-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text w-full sm:w-auto">
        <option value={0}>All Jobs</option>
        {#each jobs as job}
          <option value={job.id}>{job.name}</option>
        {/each}
      </select>
    </div>
  </div>

  {#if loading}
    <Skeleton variant="table" count={5} />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{error}</span>
    </div>
  {:else if filteredRuns.length === 0}
    <EmptyState icon="📜" title="No history yet" subtitle="Run a backup job to see results" description="Backup and restore runs will appear here once jobs have been executed." />
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border">
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider">Job</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider">Status</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider hidden sm:table-cell">Operation</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider hidden sm:table-cell">Type</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider hidden md:table-cell">Items</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider hidden lg:table-cell">Size</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider hidden md:table-cell">Duration</th>
              <th class="px-5 py-3 text-left text-xs font-medium text-text-muted uppercase tracking-wider">Started</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each filteredRuns as run}
              <tr
                class="hover:bg-surface-3/50 transition-colors {hasLogDetails(run) ? 'cursor-pointer' : ''}"
                onclick={() => hasLogDetails(run) && toggleRunExpand(run.id)}
              >
                <td class="px-5 py-3">
                  <div class="flex items-center gap-2">
                    {#if hasLogDetails(run)}
                      <svg class="w-3.5 h-3.5 text-text-dim shrink-0 transition-transform {expandedRunIds.has(run.id) ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
                      </svg>
                    {:else}
                      <span class="w-3.5"></span>
                    {/if}
                    <p class="text-sm font-medium text-text">{run.jobName}</p>
                  </div>
                </td>
                <td class="px-5 py-3">
                  <span class="text-xs px-2 py-1 rounded-full font-medium {statusBadge(run.status)}">
                    {run.status}
                  </span>
                  {#if run.status === 'failed' && getFailureReason(run)}
                    <p class="text-xs text-danger mt-1 max-w-[200px] truncate" title={getFailureReason(run)}>{getFailureReason(run)}</p>
                  {/if}
                </td>
                <td class="px-5 py-3 hidden sm:table-cell">
                  {#if (run.run_type || 'backup') === 'restore'}
                    <span class="text-xs px-2 py-1 rounded-full font-medium bg-purple-500/15 text-purple-400">restore</span>
                  {:else}
                    <span class="text-xs px-2 py-1 rounded-full font-medium bg-accent/15 text-accent">backup</span>
                  {/if}
                </td>
                <td class="px-5 py-3 text-sm text-text-muted hidden sm:table-cell capitalize">{run.backup_type || '—'}</td>
                <td class="px-5 py-3 text-sm text-text-muted hidden md:table-cell">
                  {#if run.items_total}
                    <span class="text-success">{run.items_done}</span>/{run.items_total}
                    {#if run.items_failed > 0}
                      <span class="text-danger ml-1">({run.items_failed} failed)</span>
                    {/if}
                  {:else}
                    —
                  {/if}
                </td>
                <td class="px-5 py-3 text-sm text-text-muted hidden lg:table-cell">{formatBytes(run.size_bytes)}</td>
                <td class="px-5 py-3 text-sm text-text-muted hidden md:table-cell">{duration(run)}</td>
                <td class="px-5 py-3 text-sm text-text-muted">{relTime(run.started_at)}</td>
              </tr>
              <!-- Expandable per-item log detail -->
              {#if expandedRunIds.has(run.id) && hasLogDetails(run)}
                {@const items = tryParseJSON(run.log)}
                <tr class="bg-surface-3/20">
                  <td colspan="8" class="px-5 py-3">
                    {#if Array.isArray(items)}
                      <div class="space-y-1.5">
                        <p class="text-xs font-medium text-text-muted uppercase tracking-wider mb-2">Per-Item Results</p>
                        {#each items as item}
                          <div class="flex items-center gap-2 text-sm">
                            {#if item.status === 'ok'}
                              <svg class="w-4 h-4 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
                              </svg>
                            {:else}
                              <svg class="w-4 h-4 text-danger shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
                              </svg>
                            {/if}
                            <span class="font-medium text-text">{item.name}</span>
                            {#if item.size_bytes}
                              <span class="text-xs text-text-dim">{formatBytes(item.size_bytes)}</span>
                            {/if}
                            {#if item.verified}
                              <span class="text-xs px-1.5 py-0.5 rounded bg-success/15 text-success">verified</span>
                            {/if}
                            {#if item.error}
                              <span class="text-xs text-danger ml-auto">{item.error}</span>
                            {/if}
                          </div>
                        {/each}
                      </div>
                    {:else}
                      <!-- Legacy plain-text log -->
                      <pre class="text-xs text-text-dim font-mono whitespace-pre-wrap">{run.log}</pre>
                    {/if}
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </div>
    <p class="text-xs text-text-dim mt-3 text-center">{filteredRuns.length} run{filteredRuns.length !== 1 ? 's' : ''} shown</p>
  {/if}
</div>
