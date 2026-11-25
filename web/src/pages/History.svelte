<script>
  import { onMount, tick } from 'svelte'
  import { SvelteSet, SvelteMap } from 'svelte/reactivity'
  import { api, isReplicaMode } from '../lib/api.js'
  import { relTime, formatBytes, formatInt, runSpeed, formatDurationFromDates, statusBadge, getFailureReason, formatDate, itemDisplayLabel, itemTypeNoun, commonItemType, unchangedItemCount } from '../lib/utils.js'
  import { runStatusLabel } from '../lib/statusLabels.js'
  import { readHashFilters, writeHashFilters } from '../lib/urlFilters.js'
  import { editLinkFor } from '../lib/jobForm.js'
  import { errorHint } from '../lib/errorHint.js'
  import LoadError from '../components/LoadError.svelte'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { getProgress, handleProgressMessage, restoreFromStatus } from '../lib/progress.svelte.js'
  import Skeleton from '../components/Skeleton.svelte'
  import RestoreRequestList from '../components/RestoreRequestList.svelte'
  import VmReadinessResult from '../components/VmReadinessResult.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import SizeChart from '../components/SizeChart.svelte'
  import DurationChart from '../components/DurationChart.svelte'
  import PullToRefresh from '../components/PullToRefresh.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'
  import { PURGE_HISTORY_SUMMARY } from '../lib/risky-actions.js'
  import Tooltip from '../components/Tooltip.svelte'
  import Toast from '../components/Toast.svelte'
  import AnomalyBadge from '../components/AnomalyBadge.svelte'
  import { setOpenList, getAnomalyCounts, formatAnomalyTooltip } from '../lib/anomalies.svelte.js'
  import InlineSpinner from '../components/InlineSpinner.svelte'
  import { ICONS } from '../lib/icons.js'
  import { doneCountClass } from '../lib/outcome-colour.js'

  /** Count open anomalies associated with a specific job run. */
  function runAnomalyCounts(runId) {
    return getAnomalyCounts({ job_run_id: runId })
  }

  const progress = getProgress()
  let toast = $state({ message: '', type: 'info', key: 0 })
  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }
  let entryToCancel = $state(null)
  let cancelInProgress = $state(false)

  function promptCancelQueue(entry) {
    entryToCancel = entry
  }

  async function confirmCancelQueue() {
    if (!entryToCancel) return
    cancelInProgress = true
    try {
      await api.cancelQueueEntry(entryToCancel.id)
      showToast(`Cancelled queued ${entryToCancel.kind || 'operation'} for "${entryToCancel.job_name}"`, 'success')
      entryToCancel = null
    } catch (e) {
      showToast(e.message || 'Failed to cancel queued item', 'error')
    } finally {
      cancelInProgress = false
    }
  }

  let loading = $state(true)
  let error = $state('')
  let jobs = $state([])
  let allRuns = $state([])
  const statusFilters = [
    ['all', 'All'], ['completed', 'Completed'], ['failed', 'Failed'], ['running', 'Running'],
    ['skipped', 'Skipped'], ['cancelled', 'Cancelled'], ['partial', 'Partial'],
  ]
  const RUN_TYPES = ['all', 'backup', 'restore']
  // Every filter lives in the hash query (#693), in this order, and is left out
  // of the URL while at its default.
  const FILTER_DEFAULTS = { status: 'all', review: '', job: 0, kind: 'all', q: '' }

  /** The filters a hash query asks for, with anything unknown at its default. */
  function filtersFrom(params) {
    const status = params?.get('status')
    const kind = params?.get('kind')
    const job = Number(params?.get('job'))
    return {
      status: statusFilters.some(([value]) => value === status) ? status : 'all',
      review: params?.get('review') === 'outstanding',
      job: Number.isInteger(job) && job > 0 ? job : 0,
      kind: RUN_TYPES.includes(kind) ? kind : 'all',
      q: params?.get('q') ?? '',
    }
  }

  // Read before the first render, so a reload or Back shows the filtered list
  // straight away rather than flashing the unfiltered one.
  const initialFilters = filtersFrom(readHashFilters('/history'))
  let selectedJob = $state(initialFilters.job)
  let selectedRunType = $state(initialFilters.kind)
  let selectedStatus = $state(initialFilters.status)
  let needsReview = $state(initialFilters.review)
  let searchQuery = $state(initialFilters.q)
  const filtersActive = $derived(selectedJob !== 0 || selectedRunType !== 'all' || selectedStatus !== 'all' || needsReview || searchQuery.trim() !== '')

  function clearFilters() {
    selectedJob = 0
    selectedRunType = 'all'
    selectedStatus = 'all'
    needsReview = false
    searchQuery = ''
  }

  $effect(() => {
    writeHashFilters('/history', {
      status: selectedStatus,
      review: needsReview ? 'outstanding' : '',
      job: selectedJob,
      kind: selectedRunType,
      q: searchQuery,
    }, FILTER_DEFAULTS)
  })
  let reviewFilter = $state()
  let selectedRunIds = new SvelteSet()
  let acknowledgeReason = $state('')
  let acknowledging = $state(false)
  const acknowledgementBatchLimit = 1000

  function runNeedsReview(run) {
    return !run.acknowledged_at && ['failed', 'error', 'skipped', 'cancelled', 'partial'].includes(run.status)
  }

  function selectAllNeedingReview() {
    for (const run of reviewableRuns) selectedRunIds.add(run.id)
  }

  function acknowledgementMessage(acknowledged, skipped) {
    return `Acknowledged ${acknowledged} ${acknowledged === 1 ? 'run' : 'runs'}. Skipped ${skipped}.`
  }

  async function acknowledgeRuns(ids) {
    if (acknowledging || ids.length === 0) return
    const reason = acknowledgeReason
    if ([...reason].length > 500) {
      showToast('Reason must be at most 500 characters', 'error')
      return
    }
    const trigger = document.activeElement
    acknowledging = true
    let acknowledged = 0
    let skipped = 0
    let processed = 0
    try {
      for (let offset = 0; offset < ids.length; offset += acknowledgementBatchLimit) {
        const batch = ids.slice(offset, offset + acknowledgementBatchLimit)
        const result = await api.acknowledgeRuns(batch, reason)
        acknowledged += result.acknowledged
        skipped += result.skipped
        processed += batch.length
      }
      selectedRunIds.clear()
      acknowledgeReason = ''
      showToast(acknowledgementMessage(acknowledged, skipped), 'success')
      await loadData(true)
    } catch (e) {
      const result = processed > 0 ? ` ${acknowledgementMessage(acknowledged, skipped)}` : ''
      showToast((e.message || 'Failed to acknowledge runs') + result, 'error')
      if (processed > 0) await loadData(true)
    } finally {
      acknowledging = false
      await tick()
      if (trigger instanceof HTMLButtonElement && document.activeElement === document.body) {
        if (trigger.isConnected && !trigger.disabled) trigger.focus()
        else reviewFilter?.focus()
      }
    }
  }
  let pageSize = 20
  let visibleCount = $state(pageSize)
  let expandedRunIds = $state(new SvelteSet())
  // Runs we've already auto-expanded once, so a user collapsing a failed run
  // isn't undone on the next refresh.
  // eslint-disable-next-line svelte/prefer-svelte-reactivity -- one-shot guard; it is intentionally not rendered.
  const autoExpandedRunIds = new Set()
  let confirmPurge = $state(false)
  let purging = $state(false)

  // Purge is destructive, so it isn't the page's primary action: it lives in an
  // overflow menu as a ghost item, then asks for typed confirmation (#699).
  let moreOpen = $state(false)
  /** @type {HTMLButtonElement | undefined} */
  let moreButton = $state()
  /** @type {HTMLElement | undefined} */
  let moreMenu = $state()

  async function openMore() {
    moreOpen = true
    await tick()
    // With nothing to purge the only item is disabled, so the menu itself takes
    // focus and still closes on Escape.
    const items = menuItems()
    const target = items.length ? items[0] : moreMenu
    target?.focus()
  }

  /** @returns {HTMLElement[]} */
  function menuItems() {
    return [...(moreMenu?.querySelectorAll('[role="menuitem"]:not([disabled])') ?? [])]
  }

  function closeMore(returnFocus = true) {
    moreOpen = false
    if (returnFocus) moreButton?.focus()
  }

  function moreKeydown(event) {
    const items = menuItems()
    const at = items.indexOf(/** @type {HTMLElement} */ (document.activeElement))
    if (event.key === 'Escape') {
      event.preventDefault()
      closeMore()
    } else if (event.key === 'Tab') {
      closeMore(false)
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const step = event.key === 'ArrowDown' ? 1 : -1
      if (items.length) items[(at + step + items.length) % items.length].focus()
    }
  }

  function moreFocusOut(event) {
    if (!moreOpen || confirmPurge) return
    const next = event.relatedTarget
    if (event.currentTarget.contains(next)) return
    // Focus moving into the confirmation dialog isn't leaving the menu.
    if (next instanceof Element && next.closest('dialog[open]')) return
    moreOpen = false
  }

  function startPurge() {
    // The menu stays open behind the modal confirmation, so Cancel or Escape
    // returns focus to Purge itself, as every other dialog returns to its opener.
    confirmPurge = true
  }

  let trendPeriod = $state('30d')
  let trendData = $state({ period: '30d', bucket: 'day', points: [] })
  let trendMetric = $state('size')
  const TREND_PERIODS = [['7d', '7d'], ['30d', '30d'], ['90d', '90d'], ['6m', '6m'], ['1y', '1y']]

  let trendRequestGeneration = $state(0)
  // A failed trend fetch is shown in the chart area. Keeping the previous
  // period's chart under the newly selected period button would mislabel it (#681).
  let trendError = $state('')
  let trendLoading = $state(false)

  async function loadTrend() {
    const generation = ++trendRequestGeneration
    const period = trendPeriod
    const metric = trendMetric
    trendLoading = true
    try {
      const result = await api.getHistoryTrend(period, metric)
      if (generation === trendRequestGeneration) {
        trendData = result
        trendError = ''
      }
    } catch (e) {
      if (generation === trendRequestGeneration) {
        trendData = { period, bucket: 'day', points: [] }
        trendError = e?.message || 'Request failed'
      }
    } finally {
      if (generation === trendRequestGeneration) trendLoading = false
    }
  }

  function selectTrendPeriod(val) {
    trendPeriod = val
    loadTrend()
  }

  function selectTrendMetric(val) {
    trendMetric = val
    loadTrend()
  }

  async function loadAnomalies() {
    try {
      const res = await api.listAnomalies({ state: 'open', limit: 200 })
      setOpenList(res?.anomalies ?? [])
    } catch { /* keep whatever is already loaded */ }
  }

  // #/history?run=<id> opens that run, e.g. "View run" from the restore wizard (#701).
  function runFrom(params) {
    const run = Number(params?.get('run'))
    return Number.isInteger(run) && run > 0 ? run : null
  }
  let focusRunId = $state(runFrom(readHashFilters('/history')))
  function revealFocusedRun() {
    if (focusRunId == null || !allRuns.some(r => r.id === focusRunId)) return
    const id = focusRunId
    focusRunId = null
    expandedRunIds.add(id)
    tick().then(() => document.getElementById(`run-${id}`)?.scrollIntoView({ block: 'center' }))
  }

  // A link or Back/Forward to another History URL while the page is open.
  function applyHashFilters() {
    const params = readHashFilters('/history')
    if (params === null) return
    const next = filtersFrom(params)
    selectedStatus = next.status
    needsReview = next.review
    selectedJob = next.job
    selectedRunType = next.kind
    searchQuery = next.q
    focusRunId = runFrom(params)
    revealFocusedRun()
  }

  onMount(() => {
    window.addEventListener('hashchange', applyHashFilters)
    api.getRunnerStatus().then(s => restoreFromStatus(s)).catch(() => {})
    loadData()
    loadTrend()
    loadAnomalies()
    const unsub = onWsMessage((msg) => {
      handleProgressMessage(msg)
      if (msg.type === 'job_run_started' || msg.type === 'job_run_completed' || msg.type === 'import_completed' || msg.type === 'job_runs_acknowledged') {
        loadData(true)
      }
    })
    return () => {
      window.removeEventListener('hashchange', applyHashFilters)
      unsub()
    }
  })

  let dataRequestGeneration = 0
  async function loadData(silent = false) {
    const generation = ++dataRequestGeneration
    if (!silent) loading = true
    try {
      const [loadedJobs, runs] = await Promise.all([api.listJobs(), api.getHistory(200)])
      if (generation !== dataRequestGeneration) return
      jobs = loadedJobs || []
      const names = new Map(jobs.map(job => [job.id, job.name]))
      allRuns = (runs || []).map(run => ({ ...run, jobName: names.get(run.job_id) || `Job ${run.job_id}` }))
      if (selectedRunIds.size > 0) {
        const available = new Set(reviewableRuns.map(run => run.id))
        for (const id of selectedRunIds) {
          if (!available.has(id)) selectedRunIds.delete(id)
        }
      }
      error = ''
      autoExpandRecentFailures()
      revealFocusedRun()
    } catch (e) {
      if (generation === dataRequestGeneration) error = e.message || 'Failed to load history'
    } finally {
      if (generation === dataRequestGeneration) loading = false
    }
  }

  const filteredRuns = $derived(
    allRuns
      .filter(r => !needsReview || runNeedsReview(r))
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

  const reviewableRuns = $derived(filteredRuns.filter(runNeedsReview))
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

  // The noun for a run's item counter. run.log already carries a per-item
  // `type`, so a run touching one item type reads "3/3 containers" instead of
  // the ambiguous "3/3 items" (issue #334). Mixed-type runs keep "items".
  function runItemNoun(run, count) {
    return itemTypeNoun(commonItemType(tryParseJSON(run.log)), count)
  }

  // How many of a run's items the engine captured no changed content for
  // (issue #326).
  function runUnchangedCount(run) {
    return unchangedItemCount(tryParseJSON(run.log))
  }

  // Auto-expand the most recent failed/partial runs (once each) so operators
  // immediately see the per-item breakdown and error messages without hunting.
  // Capped at the five newest to avoid expanding a wall of historical failures.
  function autoExpandRecentFailures() {
    const recent = allRuns
      .filter(r => (r.status === 'failed' || r.status === 'error' || r.status === 'partial') && hasLogDetails(r))
      .slice(0, 5)
    for (const r of recent) {
      if (autoExpandedRunIds.has(r.id)) continue
      autoExpandedRunIds.add(r.id)
      expandedRunIds.add(r.id)
    }
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
      case 'partial': case 'pending':
        // Partial/pending are neither clean success nor outright failure —
        // amber warning triangle to match the amber status badge.
        return { d: ICONS.warning, cls: 'text-warning' }
      case 'running':
        return { d: ICONS.refresh, cls: 'text-info' }
      case 'skipped':
        // Skipped runs are neither failures nor successes – render them
        // neutrally with a forward-skip icon to make the distinction clear.
        return { d: 'M13 5l7 7-7 7M5 5l7 7-7 7', cls: 'text-text-muted' }
      default:
        return { d: ICONS.clock, cls: 'text-text-muted' }
    }
  }

  function loadMore() {
    visibleCount += pageSize
  }

  async function handlePurge() {
    if (purging) return
    purging = true
    try {
      await api.purgeHistory()
      // Reload first: the Purge button is disabled once the list is empty, so
      // closing after the reload sends focus to the page instead of a dead control.
      await loadData()
      confirmPurge = false
      moreOpen = false
    } catch (e) {
      // A failed purge leaves the history as it was, so say so without
      // replacing the list with a load error.
      confirmPurge = false
      showToast(e.message || 'Failed to purge history', 'error')
    } finally {
      purging = false
    }
  }

  // Reset pagination when filters change
  $effect(() => {
    selectedJob; selectedRunType; selectedStatus; searchQuery; needsReview;
    selectedRunIds.clear()
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
    {#if !isReplicaMode()}
      <div class="relative self-start sm:self-auto" onfocusout={moreFocusOut}>
        <button type="button" bind:this={moreButton} onclick={() => moreOpen ? closeMore() : openMore()}
          aria-haspopup="menu" aria-expanded={moreOpen} aria-controls={moreOpen ? 'history-more-menu' : undefined}
          class="min-h-[36px] px-3 py-2 rounded-lg text-sm font-medium text-text-muted hover:text-text hover:bg-surface-3 transition-colors flex items-center gap-1.5">
          <svg aria-hidden="true" class="w-4 h-4" fill="currentColor" viewBox="0 0 24 24"><circle cx="5" cy="12" r="1.75"/><circle cx="12" cy="12" r="1.75"/><circle cx="19" cy="12" r="1.75"/></svg>
          More actions
        </button>
        {#if moreOpen}
          <div id="history-more-menu" role="menu" tabindex="-1" aria-label="More actions" bind:this={moreMenu} onkeydown={moreKeydown}
            class="absolute right-0 sm:right-0 left-0 sm:left-auto z-20 mt-1 min-w-48 bg-surface-2 border border-border rounded-lg shadow-lg p-1">
            <button type="button" role="menuitem" onclick={startPurge} disabled={allRuns.length === 0}
              class="w-full min-h-[36px] px-3 py-2 rounded-md text-left text-sm text-text-muted hover:text-text hover:bg-surface-3 focus-visible:bg-surface-3 transition-colors flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed">
              <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.trash}/></svg>
              Purge history…
            </button>
          </div>
        {/if}
      </div>
    {/if}
  </div>

  {#if isReplicaMode()}
    <div class="flex items-center gap-2.5 bg-surface-3 border border-border rounded-xl px-4 py-2.5 mb-4 text-sm text-text-muted">
      <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.lock}/></svg>
      <span>Read-only replica — write actions are disabled on this instance.</span>
    </div>
  {/if}

  {#if loading}
    <Skeleton variant="table" count={5} />
  {:else if error}
    <LoadError title="Couldn't load history" message={error} onretry={() => loadData()} />
  {:else}
    <!-- Active & Queued Operations -->
    {#if (progress.running && progress.activeRun) || (progress.queue && progress.queue.length > 0)}
      <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6 shadow-sm">
        <div class="flex items-center justify-between gap-2 mb-3">
          <div class="flex items-center gap-2">
            <span class="relative flex h-2 w-2">
              <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-warning opacity-75"></span>
              <span class="relative inline-flex rounded-full h-2 w-2 bg-warning"></span>
            </span>
            <h2 class="text-xs font-semibold text-text uppercase tracking-wider">Active & Queued Operations ({(progress.running && progress.activeRun ? 1 : 0) + (progress.queue?.length || 0)})</h2>
          </div>
        </div>
        <div class="divide-y divide-border/60">
          {#if progress.running && progress.activeRun}
            <div class="py-2.5 flex items-center justify-between gap-3 text-sm">
              <div class="flex items-center gap-2.5 min-w-0">
                {#if progress.activeRun.run_type === 'restore'}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-hue-purple-bg/10 text-hue-purple border border-hue-purple-bg/20 shrink-0">Restore</span>
                {:else}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-info/10 text-info border border-info/20 shrink-0">Backup</span>
                {/if}

                <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-info/10 text-info border border-info/20 flex items-center gap-1 shrink-0">
                  <InlineSpinner size="xs" />
                  Running
                </span>

                <span class="font-medium text-text truncate">{progress.activeRun.job_name || `Job #${progress.activeRun.job_id}`}</span>
                {#if progress.activeRun.started_at}
                  <span class="text-xs text-text-muted shrink-0">· {relTime(progress.activeRun.started_at)}</span>
                {/if}
              </div>
            </div>
          {/if}
          {#each progress.queue as entry (entry.id || entry.job_id + (entry.queued_at || ''))}
            <div class="py-2.5 flex items-center justify-between gap-3 text-sm">
              <div class="flex items-center gap-2.5 min-w-0">
                {#if entry.kind === 'restore'}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-hue-purple-bg/10 text-hue-purple border border-hue-purple-bg/20 shrink-0">Restore</span>
                {:else if entry.kind === 'delete'}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-hue-rose-bg/10 text-hue-rose border border-hue-rose-bg/20 shrink-0">Cleanup</span>
                {:else}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-info/10 text-info border border-info/20 shrink-0">Backup</span>
                {/if}

                {#if entry.status === 'running'}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-info/10 text-info border border-info/20 flex items-center gap-1 shrink-0">
                    <InlineSpinner size="xs" />
                    Running
                  </span>
                {:else}
                  <span class="px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider rounded-md bg-warning/10 text-warning border border-warning/20 shrink-0">Queued</span>
                {/if}

                <span class="font-medium text-text truncate">{entry.job_name || `Job #${entry.job_id}`}</span>
                {#if entry.queued_at}
                  <span class="text-xs text-text-muted shrink-0">· {relTime(entry.queued_at)}</span>
                {/if}
              </div>
              <div class="flex items-center gap-2 shrink-0">
                {#if !isReplicaMode() && entry.status === 'queued' && entry.kind !== 'delete'}
                  <button
                    type="button"
                    onclick={() => promptCancelQueue(entry)}
                    disabled={cancelInProgress && entryToCancel?.id === entry.id}
                    class="px-2.5 py-1 text-xs text-danger hover:bg-danger/10 border border-danger/20 rounded-lg transition-colors flex items-center gap-1 disabled:opacity-50"
                    title="Cancel queued operation"
                  >
                    <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.close}/>
                    </svg>
                    Cancel
                  </button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <!-- One summary row: run counts for the current filters beside the Restore
         Request history, so the run list starts above the fold (#699). -->
    <div class="flex flex-wrap items-stretch gap-3 mb-4">
      <dl data-run-summary class="flex flex-wrap items-center gap-x-5 gap-y-1 rounded-xl border border-border bg-surface-2 px-4 py-2 text-sm">
        <div class="flex items-baseline gap-1.5"><dt class="text-text-muted">Total runs</dt><dd class="font-semibold text-text tabular-nums">{formatInt(stats.total)}</dd></div>
        <div class="flex items-baseline gap-1.5"><dt class="text-text-muted">Completed</dt><dd class="font-semibold text-success tabular-nums">{formatInt(stats.success)}</dd></div>
        <div class="flex items-baseline gap-1.5"><dt class="text-text-muted">Failed</dt><dd class="font-semibold {stats.failed > 0 ? 'text-danger' : 'text-text'} tabular-nums">{formatInt(stats.failed)}</dd></div>
        <div class="flex items-baseline gap-1.5"><dt class="text-text-muted">Running</dt><dd class="font-semibold {stats.running > 0 ? 'text-info' : 'text-text'} tabular-nums">{formatInt(stats.running)}</dd></div>
        <div class="flex items-baseline gap-1.5"><dt class="text-text-muted">Total size</dt><dd class="font-semibold text-text tabular-nums">{formatBytes(stats.totalSize)}</dd></div>
      </dl>
      {#if !isReplicaMode()}
        <div class="flex-1 min-w-[16rem] [&>section]:mb-0 [&>section]:h-full"><RestoreRequestList history /></div>
      {/if}
    </div>

    <!-- One filter toolbar (#699). -->
    <div role="group" aria-label="Filter runs" class="flex flex-wrap items-center gap-x-3 gap-y-2 mb-4">
      <!-- Search -->
      <div class="relative flex-1 min-w-[12rem] max-w-xs">
        <svg aria-hidden="true" class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.search}/>
        </svg>
        <input type="text" bind:value={searchQuery} placeholder="Search runs..."
          aria-label="Search runs"
          class="w-full pl-9 pr-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text placeholder:text-text-muted focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault max-lg:min-h-11" />
      </div>

      <!-- Status filter pills -->
      <div role="group" aria-label="Filter by status" class="flex items-center gap-1 flex-wrap rounded-lg bg-surface-3 p-0.5">
        {#each statusFilters as [val, label] (val)}
          <button type="button" onclick={() => selectedStatus = val}
            aria-pressed={selectedStatus === val}
            class="min-h-[28px] px-2.5 py-1 text-xs font-medium rounded-md transition-colors {selectedStatus === val ? 'bg-vault text-on-vault' : 'text-text-muted hover:text-text hover:bg-surface-4'}">
            {label}
          </button>
        {/each}
      </div>

      <!-- Run type filter -->
      <div class="flex items-center">
        <div role="group" aria-label="Filter by type" class="flex rounded-lg border border-border overflow-hidden text-xs">
        <button onclick={() => selectedRunType = 'all'}
          aria-pressed={selectedRunType === 'all'}
          class="px-3 py-1.5 transition-colors {selectedRunType === 'all' ? 'bg-vault text-on-vault' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">All</button>
        <button onclick={() => selectedRunType = 'backup'}
          aria-pressed={selectedRunType === 'backup'}
          class="px-3 py-1.5 transition-colors border-l border-border {selectedRunType === 'backup' ? 'bg-vault text-on-vault' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">Backups</button>
        <button onclick={() => selectedRunType = 'restore'}
          aria-pressed={selectedRunType === 'restore'}
          class="px-3 py-1.5 transition-colors border-l border-border {selectedRunType === 'restore' ? 'bg-vault text-on-vault' : 'bg-surface-2 text-text-muted hover:bg-surface-3'}">Restores</button>
        </div>
      </div>

      <select bind:value={selectedJob}
        aria-label="Filter by job"
        class="px-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text max-lg:min-h-11">
        <option value={0}>All Jobs</option>
        {#each jobs as job (job.id)}
          <option value={job.id}>{job.name}</option>
        {/each}
      </select>

      <label class="flex items-center gap-2 min-h-8 text-sm text-text">
        <input type="checkbox" bind:checked={needsReview} bind:this={reviewFilter} class="size-4 accent-vault" />
        Needs review
      </label>

      {#if !isReplicaMode() && reviewableRuns.length > 0 && selectedRunIds.size === 0}
        <button type="button" class="text-sm font-medium text-vault-text hover:underline min-h-8 disabled:opacity-50" onclick={selectAllNeedingReview} disabled={acknowledging}>
          Select all needing review ({formatInt(reviewableRuns.length)})
        </button>
      {/if}
    </div>

    <!-- The bulk toolbar appears only once runs are selected (#699). -->
    {#if !isReplicaMode() && selectedRunIds.size > 0}
      <section aria-label="Run acknowledgement" class="flex flex-wrap items-center gap-3 mb-4 rounded-xl border border-border bg-surface-2 px-4 py-2">
        <span class="text-sm text-text tabular-nums">{formatInt(selectedRunIds.size)} selected</span>
        {#if selectedRunIds.size < reviewableRuns.length}
          <button type="button" class="text-sm font-medium text-vault-text hover:underline disabled:opacity-50" onclick={selectAllNeedingReview} disabled={acknowledging}>
            Select all needing review ({formatInt(reviewableRuns.length)})
          </button>
        {/if}
        <input type="text" bind:value={acknowledgeReason} disabled={acknowledging}
          aria-label="Acknowledgement reason (optional)" placeholder="Reason (optional)"
          class="flex-1 min-w-[10rem] max-w-sm px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text max-lg:min-h-11" />
        <button type="button" class="btn btn-primary" onclick={() => acknowledgeRuns([...selectedRunIds])}
          disabled={acknowledging}>
          Acknowledge selected
        </button>
        <button type="button" class="btn btn-ghost" onclick={() => selectedRunIds.clear()} disabled={acknowledging}>
          Clear selection
        </button>
      </section>
    {/if}

    <!-- Trend chart: covers every run, so it stays while filters narrow the list. -->
    {#if allRuns.length > 0}
      <div class="flex items-center justify-between mb-2 gap-3 flex-wrap">
        <div class="relative">
          <select value={trendMetric} onchange={(e) => selectTrendMetric(e.currentTarget.value)}
            aria-label="Trend metric"
            class="appearance-none pl-3 pr-9 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text cursor-pointer max-lg:min-h-11">
            <option value="size">Backup size</option>
            <option value="duration">Job duration</option>
          </select>
          <svg aria-hidden="true" class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-dim"
            fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronDown}/>
          </svg>
        </div>
        <div role="group" aria-label="Trend period" class="flex items-center rounded-lg border border-border bg-surface-3 p-0.5 text-xs">
          {#each TREND_PERIODS as [val, label] (val)}
            <button type="button" onclick={() => selectTrendPeriod(val)}
              aria-pressed={trendPeriod === val}
              class="px-2.5 py-1 rounded-md font-medium transition-colors cursor-pointer {trendPeriod === val ? 'bg-vault text-on-vault' : 'text-text-muted hover:text-text'}">{label}</button>
          {/each}
        </div>
      </div>
      {#if trendError}
        <div class="mb-6">
          <LoadError title="Couldn't load the {trendMetric === 'duration' ? 'duration' : 'size'} trend for {trendPeriod}" message={trendError} onretry={loadTrend} retrying={trendLoading} />
        </div>
      {:else if trendLoading && !trendData.points?.length}
        <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6 text-sm text-text-muted text-center py-8" role="status">Loading trend…</div>
      {:else if trendMetric === 'duration'}
        <DurationChart buckets={trendData.points} bucket={trendData.bucket} />
      {:else}
        <SizeChart buckets={trendData.points} bucket={trendData.bucket} />
      {/if}
    {/if}

    {#if allRuns.length === 0}
      <!-- Nothing has run yet: a first-run next step, not "no matches" (#695). -->
      <EmptyState title="No runs yet"
        description={jobs.length > 0 ? 'Backup and restore runs appear here once a job runs. Run a job now, or wait for its schedule.' : 'Create a backup job, and its runs will appear here.'}>
        {#snippet iconSlot()}
          <svg aria-hidden="true" class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={ICONS.clock}/></svg>
        {/snippet}
        {#if !isReplicaMode()}
          <a href="#/jobs" class="mt-4 px-4 py-2 text-sm font-medium text-on-vault bg-vault hover:bg-vault-dark rounded-lg transition-colors">
            {jobs.length > 0 ? 'Run a backup' : 'Create a job'}
          </a>
        {/if}
      </EmptyState>
    {:else if filteredRuns.length === 0}
      <EmptyState title="No matching runs" description="No runs match these filters.">
        {#snippet iconSlot()}
          <svg aria-hidden="true" class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={ICONS.search}/></svg>
        {/snippet}
        {#if filtersActive}
          <button type="button" onclick={clearFilters} class="mt-4 px-4 py-2 text-sm font-medium text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">Clear filters</button>
        {/if}
      </EmptyState>
    {:else}
      <!-- Date-grouped timeline -->
      <div class="space-y-8">
        {#each dateGroups as [dateLabel, runs] (dateLabel)}
          <div>
            <!-- Date header -->
            <div class="flex items-center gap-3 mb-3">
              <div class="w-2 h-2 rounded-full bg-vault"></div>
              <h2 class="text-sm font-semibold text-text">{dateLabel}</h2>
              <div class="flex-1 h-px bg-border"></div>
              <span class="text-xs text-text-dim tabular-nums">{formatInt(runs.length)} run{runs.length !== 1 ? 's' : ''}</span>
            </div>

            <!-- Timeline entries -->
            <div class="ml-1 border-l-2 border-border pl-5 space-y-3">
              {#each runs as run (run.id)}
                {@const icon = statusIcon(run.status)}
                {@const anomalyCounts = runAnomalyCounts(run.id)}
                {@const speed = runSpeed(run)}
                {@const expandable = hasLogDetails(run) || Boolean(run.acknowledged_at)}
                <div id="run-{run.id}" class="bg-surface-2 border border-border rounded-xl p-4 hover:border-vault/30 transition-all">
                  <!-- With details to show, the run's name is the disclosure button and its
                       ::after stretches over this row, so the whole summary stays clickable
                       while the anomaly badge remains a sibling control, not a nested one. -->
                  <div class="relative flex items-start justify-between gap-3">
                    <div class="flex items-center gap-3 min-w-0 flex-1">
                      <!-- Status icon -->
                      <svg aria-hidden="true" class="w-5 h-5 shrink-0 {icon.cls}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={icon.d}/>
                      </svg>
                      <div class="min-w-0 flex-1">
                        <div class="flex items-center gap-2 flex-wrap">
                          {#if expandable}
                            <button
                              type="button"
                              onclick={() => toggleRunExpand(run.id)}
                              aria-expanded={expandedRunIds.has(run.id)}
                              aria-controls={expandedRunIds.has(run.id) ? `run-details-${run.id}` : undefined}
                              class="flex items-center gap-2 text-left cursor-pointer after:absolute after:inset-0 after:content-['']"
                            >
                              <span class="text-sm font-medium text-text">{run.jobName}</span>
                              <span class="{statusBadge(run.status)} text-xs">{runStatusLabel(run.status)}</span>
                            </button>
                          {:else}
                            <span class="text-sm font-medium text-text">{run.jobName}</span>
                            <span class="{statusBadge(run.status)} text-xs">{runStatusLabel(run.status)}</span>
                          {/if}
                          {#if run.acknowledged_at}
                            <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text-muted">Acknowledged</span>
                          {/if}
                          {#if !isReplicaMode() && runNeedsReview(run)}
                            <label class="relative z-10 flex items-center justify-center min-w-8 min-h-8">
                              <input type="checkbox" class="size-4 accent-vault"
                                aria-label={`Select run ${run.id} for acknowledgement`}
                                checked={selectedRunIds.has(run.id)} disabled={acknowledging}
                                onchange={(event) => event.currentTarget.checked
                                  ? selectedRunIds.add(run.id) : selectedRunIds.delete(run.id)} />
                            </label>
                            <button type="button" class="relative z-10 btn btn-secondary"
                              disabled={acknowledging} onclick={() => acknowledgeRuns([run.id])}>
                              Acknowledge
                            </button>
                          {/if}
                          {#if run.retry_attempt > 0}
                            <span
                              class="text-xs px-2 py-0.5 rounded-full font-medium bg-info/15 text-info"
                              title={run.retry_next_at ? `Next retry at ${formatDate(run.retry_next_at)}` : `Retry attempt ${run.retry_attempt}`}
                            >
                              Retry {run.retry_attempt}
                            </span>
                          {/if}
                          {#if (run.run_type || 'backup') === 'restore'}
                            <span class="text-xs px-2 py-0.5 rounded-full font-medium bg-hue-purple-bg/15 text-hue-purple">restore</span>
                          {:else}
                            <span class="text-xs px-2 py-0.5 rounded-full font-medium bg-vault/15 text-vault-text">backup</span>
                          {/if}
                          {#if run.backup_type}
                            <span class="text-xs text-text-dim capitalize">{run.backup_type}</span>
                          {/if}
                          <!-- Anomaly badge for this run -->
                          {#if anomalyCounts.critical > 0 || anomalyCounts.warning > 0 || anomalyCounts.info > 0}
                            <span class="relative">
                              <Tooltip text={formatAnomalyTooltip(anomalyCounts)}>
                                <span class="inline-flex items-center gap-1">
                                  {#each ['critical', 'warning', 'info'] as sev (sev)}
                                    {#if anomalyCounts[sev] > 0}
                                      <AnomalyBadge count={anomalyCounts[sev]} severity={sev} />
                                    {/if}
                                  {/each}
                                </span>
                              </Tooltip>
                            </span>
                          {/if}
                        </div>
                        <div class="flex items-center flex-wrap gap-x-4 gap-y-1 mt-1.5 text-xs text-text-dim tabular-nums">
                          <span title={relTime(run.started_at)}>{formatDate(run.started_at)}</span>
                          <span>{duration(run)}</span>
                          {#if run.items_total}
                            <span>
                              <span class={doneCountClass(run)}>{formatInt(run.items_done)}</span>/{formatInt(run.items_total)} {runItemNoun(run, run.items_total)}
                              {#if run.items_failed > 0}
                                <span class="text-danger">({formatInt(run.items_failed)} failed)</span>
                              {/if}
                              {#if runUnchangedCount(run) > 0}
                                <span class="text-text-dim">({formatInt(runUnchangedCount(run))} unchanged)</span>
                              {/if}
                            </span>
                          {/if}
                          {#if run.size_bytes}
                            <span>{formatBytes(run.size_bytes)}</span>
                          {/if}
                          {#if speed}
                            <span>{speed}</span>
                          {/if}
                        </div>
                        {#if getFailureReason(run)}
                          <p class="text-xs text-danger mt-1.5 max-w-xl whitespace-pre-wrap break-words line-clamp-3" title={getFailureReason(run)}>{getFailureReason(run)}</p>
                        {/if}
                      </div>
                    </div>
                    {#if expandable}
                      <svg aria-hidden="true" class="w-4 h-4 text-text-dim shrink-0 transition-transform {expandedRunIds.has(run.id) ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronRight}/>
                      </svg>
                    {/if}
                  </div>

                  <!-- Expandable log details -->
                  {#if expandedRunIds.has(run.id) && expandable}
                    {@const items = tryParseJSON(run.log)}
                    <div id="run-details-{run.id}" class="mt-3 pt-3 border-t border-border">
                      {#if run.acknowledged_at}
                        <dl class="mb-2 text-xs text-text-muted">
                          <div><dt class="inline font-semibold">Acknowledged</dt> <dd class="inline">{formatDate(run.acknowledged_at)}</dd></div>
                          {#if run.acknowledged_by}
                            <div><dt class="inline font-semibold">By</dt> <dd class="inline">{run.acknowledged_by}</dd></div>
                          {/if}
                          {#if run.acknowledge_reason}
                            <div><dt class="inline font-semibold">Reason</dt> <dd class="inline whitespace-pre-wrap break-words">{run.acknowledge_reason}</dd></div>
                          {/if}
                        </dl>
                      {/if}
                      {#if Array.isArray(items)}
                        <p class="text-xs font-medium text-text-muted uppercase tracking-wider mb-2">Per-Item Results</p>
                        <div class="space-y-1.5">
                          {#each items as item, i (i)}
                            <div class="flex items-center gap-2 text-sm">
                              {#if item.status === 'ok' && item.unchanged}
                                <svg aria-hidden="true" class="w-4 h-4 text-text-dim shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.minus}/></svg>
                              {:else if item.status === 'ok'}
                                <svg aria-hidden="true" class="w-4 h-4 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.check}/></svg>
                              {:else}
                                <svg aria-hidden="true" class="w-4 h-4 text-danger shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.close}/></svg>
                              {/if}
                              <span class="font-medium text-text truncate min-w-0" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
                              {#if item.size_bytes}
                                <span class="text-xs text-text-dim shrink-0">{formatBytes(item.size_bytes)}</span>
                              {/if}
                              {#if item.unchanged}
                                <span class="text-xs px-1.5 py-0.5 rounded bg-text-dim/15 text-text-muted shrink-0" title="Nothing had changed since the reference backup — no new data was captured for this item">unchanged</span>
                              {/if}
                              {#if item.verified}
                                <span class="text-xs px-1.5 py-0.5 rounded bg-success/15 text-success shrink-0">verified</span>
                              {/if}
                              {#if item.error && !errorHint(item.error)}
                                <span class="text-xs text-danger ml-auto min-w-0 break-words">{item.error}</span>
                              {/if}
                            </div>
                            {#if item.error && errorHint(item.error)}
                              {@const hint = errorHint(item.error)}
                              <!-- A known cause in plain words with a next step; the raw
                                   error chain stays one click away (#711). -->
                              <div class="ml-6 text-xs space-y-0.5" data-error-hint={hint.kind}>
                                <p class="font-medium text-danger">{hint.cause}</p>
                                <p class="text-text-muted break-words">
                                  {hint.action}
                                  {#if hint.excludePath && !isReplicaMode() && (run.run_type || 'backup') === 'backup'}
                                    <a href={'#' + editLinkFor(run.job_id, item.item_name || item.name || '')} class="font-medium text-vault-text hover:underline">Edit {run.jobName}</a>
                                  {/if}
                                </p>
                                <details>
                                  <summary class="cursor-pointer text-text-muted hover:text-text w-fit">Details</summary>
                                  <p class="mt-1 font-mono text-text-dim whitespace-pre-wrap break-words">{item.error}</p>
                                </details>
                              </div>
                            {/if}
                            {#if run.run_type === 'restore' && item.type === 'vm' && item.vm_readiness}
                              <div class="ml-6 text-xs"><VmReadinessResult readiness={item.vm_readiness} /></div>
                            {/if}
                          {/each}
                        </div>
                      {:else if hasLogDetails(run)}
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
            class="px-6 py-2 text-sm font-medium text-vault-text bg-vault/10 hover:bg-vault/20 rounded-lg transition-colors">
            Load More ({formatInt(filteredRuns.length - visibleCount)} remaining)
          </button>
        </div>
      {/if}

      <p class="text-xs text-text-dim mt-3 text-center tabular-nums">{formatInt(filteredRuns.length)} run{filteredRuns.length !== 1 ? 's' : ''} total</p>
    {/if}
  {/if}
</div>
</PullToRefresh>

<ConfirmDialog
  show={confirmPurge}
  title="Purge All History"
  message="Permanently delete all job-run history and their restore points, the recoverable backups Vault tracks?"
  confirmLabel="Purge Everything"
  busyLabel="Purging…"
  variant="danger"
  confirmWord="PURGE"
  source={PURGE_HISTORY_SUMMARY.source}
  consequences={PURGE_HISTORY_SUMMARY.consequences}
  busy={purging}
  onconfirm={handlePurge}
  oncancel={() => confirmPurge = false}
/>

<ConfirmDialog
  show={!!entryToCancel}
  title="Cancel Queued Operation"
  message={`Are you sure you want to cancel the queued ${entryToCancel?.kind || 'operation'} for "${entryToCancel?.job_name || ''}"?`}
  confirmLabel="Cancel Operation"
  busy={cancelInProgress}
  busyLabel="Cancelling…"
  variant="danger"
  onconfirm={confirmCancelQueue}
  oncancel={() => entryToCancel = null}
/>

<Toast message={toast.message} type={toast.type} key={toast.key} />
