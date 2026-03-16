<script>
  import { onMount } from 'svelte'
  import { SvelteSet, SvelteMap } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { relTime, formatBytes, formatSpeed, formatDurationFromDates, statusBadge, getFailureReason } from '../lib/utils.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import Skeleton from '../components/Skeleton.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import SizeChart from '../components/SizeChart.svelte'
  import PullToRefresh from '../components/PullToRefresh.svelte'

  let loading = $state(true)
  let error = $state('')
  let jobs = $state([])
  let allRuns = $state([])
  let selectedJob = $state(0)
  let selectedRunType = $state('all')
  let selectedStatus = $state('all')
  let searchQuery = $state('')
  let pageSize = 20
  let visibleCount = $state(pageSize)
  let expandedRunIds = $state(new SvelteSet())

  onMount(() => {
    loadData()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_started' || msg.type === 'job_run_completed') {
        loadData(true)
      }
    })
    return unsub
  })

  async function loadData(silent = false) {
    if (!silent) loading = true
    try {
      jobs = (await api.listJobs()) || []
      const promises = jobs.map(async (job) => {
        try {
          const runs = await api.getJobHistory(job.id, 200)
          return (runs || []).map(r => ({ ...r, jobName: job.name }))
        } catch { return [] }
      })
      const results = await Promise.all(promises)
      allRuns = results.flat().sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime())
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
      .filter(r => {
        if (selectedStatus === 'all') return true
        if (selectedStatus === 'completed') return r.status === 'completed' || r.status === 'success'
        if (selectedStatus === 'failed') return r.status === 'failed' || r.status === 'error'
        return r.status === selectedStatus
      })
      .filter(r => {
        if (!searchQuery.trim()) return true
        const q = searchQuery.toLowerCase()
        return r.jobName?.toLowerCase().includes(q)
          || r.status?.toLowerCase().includes(q)
          || r.backup_type?.toLowerCase().includes(q)
      })
  )

  let paginatedRuns = $derived(filteredRuns.slice(0, visibleCount))
  let hasMore = $derived(filteredRuns.length > visibleCount)

  // Group by date
  let dateGroups = $derived.by(() => {
    const groups = new SvelteMap()
    for (const run of paginatedRuns) {
      const d = new Date(run.started_at)
      const key = d.toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })
      if (!groups.has(key)) groups.set(key, [])
      groups.get(key).push(run)
    }
    return Array.from(groups.entries())
  })

  // Stats
  let stats = $derived.by(() => {
    const total = filteredRuns.length
    const success = filteredRuns.filter(r => r.status === 'completed' || r.status === 'success').length
    const failed = filteredRuns.filter(r => r.status === 'failed' || r.status === 'error').length
    const running = filteredRuns.filter(r => r.status === 'running').length
    const totalSize = filteredRuns.reduce((sum, r) => sum + (r.size_bytes || 0), 0)
    return { total, success, failed, running, totalSize }
  })

  function duration(run) {
    return formatDurationFromDates(run.started_at, run.completed_at)
  }

  function tryParseJSON(str) {
    if (!str) return null
    try { return JSON.parse(str) } catch { return null }
  }

  function toggleRunExpand(runId) {
    if (expandedRunIds.has(runId)) expandedRunIds.delete(runId)
    else expandedRunIds.add(runId)
  }

  function hasLogDetails(run) {
    return run.log && run.log.trim().length > 0
  }

  function statusIcon(status) {
    switch (status?.toLowerCase()) {
      case 'completed': case 'success':
        return { d: 'M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z', cls: 'text-success' }
      case 'failed': case 'error':
        return { d: 'M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z', cls: 'text-danger' }
      case 'running':
        return { d: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15', cls: 'text-info' }
      default:
        return { d: 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z', cls: 'text-text-muted' }
    }
  }

  function loadMore() {
    visibleCount += pageSize
  }

  // Reset pagination when filters change
  $effect(() => {
    selectedJob; selectedRunType; selectedStatus; searchQuery;
    visibleCount = pageSize
  })
</script>

<PullToRefresh onrefresh={loadData}>
<div>
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Backup & Restore History</h1>
      <p class="text-sm text-text-muted mt-1">View past backup and restore runs and their results</p>
    </div>
  </div>

  {#if loading}
    <Skeleton variant="table" count={5} />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{error}</span>
    </div>
  {:else}
    <!-- Stats bar -->
    <div class="grid grid-cols-2 sm:grid-cols-5 gap-3 mb-6 stagger">
      <div class="bg-surface-2 border border-border rounded-xl p-3 text-center">
        <p class="text-lg font-bold text-text">{stats.total}</p>
        <p class="text-xs text-text-muted">Total Runs</p>
      </div>
      <div class="bg-surface-2 border border-border rounded-xl p-3 text-center">
        <p class="text-lg font-bold text-success">{stats.success}</p>
        <p class="text-xs text-text-muted">Successful</p>
      </div>
      <div class="bg-surface-2 border border-border rounded-xl p-3 text-center">
        <p class="text-lg font-bold text-danger">{stats.failed}</p>
        <p class="text-xs text-text-muted">Failed</p>
      </div>
      <div class="bg-surface-2 border border-border rounded-xl p-3 text-center">
        <p class="text-lg font-bold text-info">{stats.running}</p>
        <p class="text-xs text-text-muted">Running</p>
      </div>
      <div class="bg-surface-2 border border-border rounded-xl p-3 text-center col-span-2 sm:col-span-1">
        <p class="text-lg font-bold text-text">{formatBytes(stats.totalSize)}</p>
        <p class="text-xs text-text-muted">Total Size</p>
      </div>
    </div>

    <!-- Filters row -->
    <div class="flex flex-wrap items-center gap-3 mb-6">
      <!-- Search -->
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <svg class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
        </svg>
        <input type="text" bind:value={searchQuery} placeholder="Search runs..."
          class="w-full pl-9 pr-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
      </div>

      <!-- Status filter pills -->
      {#each [['all','All'], ['completed','Completed'], ['failed','Failed'], ['running','Running']] as [val, label] (val)}
        <button type="button" onclick={() => selectedStatus = val}
          class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors {selectedStatus === val ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}">
          {label}
        </button>
      {/each}

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
        class="px-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text">
        <option value={0}>All Jobs</option>
        {#each jobs as job (job.id)}
          <option value={job.id}>{job.name}</option>
        {/each}
      </select>
    </div>

    {#if filteredRuns.length === 0}
      <EmptyState icon="📜" title="No matching runs" description="Try adjusting your filters or search query." />
    {:else}
      <!-- Size trend chart -->
      <SizeChart runs={filteredRuns} />

      <!-- Date-grouped timeline -->
      <div class="space-y-8">
        {#each dateGroups as [dateLabel, runs] (dateLabel)}
          <div>
            <!-- Date header -->
            <div class="flex items-center gap-3 mb-3">
              <div class="w-2 h-2 rounded-full bg-vault"></div>
              <h3 class="text-sm font-semibold text-text">{dateLabel}</h3>
              <div class="flex-1 h-px bg-border"></div>
              <span class="text-xs text-text-dim">{runs.length} run{runs.length !== 1 ? 's' : ''}</span>
            </div>

            <!-- Timeline entries -->
            <div class="ml-1 border-l-2 border-border pl-5 space-y-3">
              {#each runs as run (run.id)}
                {@const icon = statusIcon(run.status)}
                <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
                <div
                  class="bg-surface-2 border border-border rounded-xl p-4 hover:border-vault/30 transition-all {hasLogDetails(run) ? 'cursor-pointer' : ''}"
                  role={hasLogDetails(run) ? 'button' : undefined}
                  tabindex={hasLogDetails(run) ? 0 : undefined}
                  onclick={() => hasLogDetails(run) && toggleRunExpand(run.id)}
                  onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && hasLogDetails(run) && toggleRunExpand(run.id)}
                >
                  <div class="flex items-start justify-between gap-3">
                    <div class="flex items-center gap-3 min-w-0 flex-1">
                      <!-- Status icon -->
                      <svg class="w-5 h-5 shrink-0 {icon.cls}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={icon.d}/>
                      </svg>
                      <div class="min-w-0 flex-1">
                        <div class="flex items-center gap-2 flex-wrap">
                          <span class="text-sm font-medium text-text">{run.jobName}</span>
                          <span class="{statusBadge(run.status)} text-xs">{run.status}</span>
                          {#if (run.run_type || 'backup') === 'restore'}
                            <span class="text-xs px-2 py-0.5 rounded-full font-medium bg-purple-500/15 text-purple-400">restore</span>
                          {:else}
                            <span class="text-xs px-2 py-0.5 rounded-full font-medium bg-accent/15 text-accent">backup</span>
                          {/if}
                          {#if run.backup_type}
                            <span class="text-xs text-text-dim capitalize">{run.backup_type}</span>
                          {/if}
                        </div>
                        <div class="flex items-center gap-4 mt-1.5 text-xs text-text-dim">
                          <span>{relTime(run.started_at)}</span>
                          <span>{duration(run)}</span>
                          {#if run.items_total}
                            <span>
                              <span class="text-success">{run.items_done}</span>/{run.items_total} items
                              {#if run.items_failed > 0}
                                <span class="text-danger">({run.items_failed} failed)</span>
                              {/if}
                            </span>
                          {/if}
                          {#if run.size_bytes}
                            <span>{formatBytes(run.size_bytes)}</span>
                          {/if}
                          {#if run.duration_seconds && run.size_bytes}
                            <span>{formatSpeed(run.size_bytes, run.duration_seconds)}</span>
                          {/if}
                        </div>
                        {#if run.status === 'failed' && getFailureReason(run)}
                          <p class="text-xs text-danger mt-1.5 truncate max-w-md" title={getFailureReason(run)}>{getFailureReason(run)}</p>
                        {/if}
                      </div>
                    </div>
                    {#if hasLogDetails(run)}
                      <svg class="w-4 h-4 text-text-dim shrink-0 transition-transform {expandedRunIds.has(run.id) ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/>
                      </svg>
                    {/if}
                  </div>

                  <!-- Expandable log details -->
                  {#if expandedRunIds.has(run.id) && hasLogDetails(run)}
                    {@const items = tryParseJSON(run.log)}
                    <div class="mt-3 pt-3 border-t border-border">
                      {#if Array.isArray(items)}
                        <p class="text-xs font-medium text-text-muted uppercase tracking-wider mb-2">Per-Item Results</p>
                        <div class="space-y-1.5">
                          {#each items as item (item.name)}
                            <div class="flex items-center gap-2 text-sm">
                              {#if item.status === 'ok'}
                                <svg class="w-4 h-4 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
                              {:else}
                                <svg class="w-4 h-4 text-danger shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
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
                        <pre class="text-xs text-text-dim font-mono whitespace-pre-wrap">{run.log}</pre>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          </div>
        {/each}
      </div>

      <!-- Load more -->
      {#if hasMore}
        <div class="text-center mt-6">
          <button type="button" onclick={loadMore}
            class="px-6 py-2 text-sm font-medium text-vault bg-vault/10 hover:bg-vault/20 rounded-lg transition-colors">
            Load More ({filteredRuns.length - visibleCount} remaining)
          </button>
        </div>
      {/if}

      <p class="text-xs text-text-dim mt-3 text-center">{filteredRuns.length} run{filteredRuns.length !== 1 ? 's' : ''} total</p>
    {/if}
  {/if}
</div>
</PullToRefresh>
