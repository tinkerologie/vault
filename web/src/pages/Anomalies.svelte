<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { relTime, formatBytes, formatDuration } from '../lib/utils.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import AnomalyBadge from '../components/AnomalyBadge.svelte'
  import Toast from '../components/Toast.svelte'
  import Skeleton from '../components/Skeleton.svelte'

  // --- Filter state ---
  let filterState    = $state('open')
  let filterSeverity = $state('')
  let filterScope    = $state('')
  let filterSince    = $state('-30d')

  // --- Data state ---
  let anomalies  = $state([])
  let nextCursor = $state('')
  let loading    = $state(true)

  // Monotonic counter to discard out-of-order async responses.
  let listRequestId = 0
  let loadingMore = $state(false)
  let error      = $state('')

  // --- Bulk selection (SvelteSet is intrinsically reactive; mutate in-place) ---
  const selected = new SvelteSet()
  let bulkReason = $state('')
  let bulkWorking = $state(false)

  // --- Detail inline expansion ---
  /** @type {number|null} */
  let expandedId = $state(null)

  // --- Toast ---
  let toast = $state({ message: '', type: 'info', key: 0 })

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  // --- Per-row ack in-flight ---
  let ackingId = $state(null)

  const TERMINAL_STATES = new Set(['resolved', 'expected'])

  onMount(() => {
    loadAnomalies(true)
    const unsub = onWsMessage((msg) => {
      switch (msg.type) {
        case 'anomaly.raised':
        case 'anomaly.updated':
          // In-place upsert/remove — no refetch, no flicker.
          applyLiveAnomaly(msg.data)
          break
        case 'anomaly.bulk_resolved':
        case 'anomaly.bulk_acked':
          // Server-side bulk change: refetch but DON'T clear the list first
          // (replace the array only once new data arrives).
          refetchInPlace()
          break
      }
    })
    return unsub
  })

  /** Does an anomaly match the currently-active state filter? */
  function matchesStateFilter(anomaly) {
    return !filterState || anomaly.state === filterState
  }

  /** Apply a single live anomaly event to the current list in place. */
  function applyLiveAnomaly(anomaly) {
    if (!anomaly) return
    const idx = anomalies.findIndex(a => a.id === anomaly.id)
    // If the active filter is open and the anomaly became terminal, drop it.
    if (filterState === 'open' && TERMINAL_STATES.has(anomaly.state)) {
      if (idx >= 0) anomalies = anomalies.filter(a => a.id !== anomaly.id)
      return
    }
    if (!matchesStateFilter(anomaly)) {
      // No longer matches the active filter — remove if present.
      if (idx >= 0) anomalies = anomalies.filter(a => a.id !== anomaly.id)
      return
    }
    // Matches the filter: upsert by id.
    if (idx >= 0) {
      anomalies = anomalies.map(a => (a.id === anomaly.id ? anomaly : a))
    } else {
      anomalies = [anomaly, ...anomalies]
    }
  }

  /** Refetch the first page from the current filters and swap the list once
   *  fresh data arrives, avoiding any flash-to-empty. */
  async function refetchInPlace() {
    const reqId = ++listRequestId
    try {
      const res = await api.listAnomalies(buildFilter(undefined))
      if (reqId !== listRequestId) return // stale response — discard
      anomalies = res?.anomalies ?? []
      nextCursor = res?.next_cursor ?? ''
      // Drop selections that no longer exist.
      const ids = new Set(anomalies.map(a => a.id))
      for (const id of [...selected]) if (!ids.has(id)) selected.delete(id)
    } catch {
      // Keep the current list on transient failure.
    }
  }

  function buildFilter(cursor) {
    return {
      state: filterState || undefined,
      severity: filterSeverity || undefined,
      scope_kind: filterScope || undefined,
      since: filterSince || undefined,
      limit: 50,
      cursor: cursor || undefined,
    }
  }

  async function loadAnomalies(reset = false) {
    const reqId = ++listRequestId
    if (reset) {
      loading = true
      anomalies = []
      nextCursor = ''
      clearSelected()
    } else {
      loadingMore = true
    }
    error = ''
    try {
      const res = await api.listAnomalies(buildFilter(reset ? undefined : nextCursor))
      if (reqId !== listRequestId) return // stale response — discard
      const rows = res?.anomalies ?? []
      anomalies = reset ? rows : [...anomalies, ...rows]
      nextCursor = res?.next_cursor ?? ''
    } catch (e) {
      if (reqId !== listRequestId) return
      error = e.message || 'Failed to load anomalies'
    } finally {
      if (reqId === listRequestId) {
        loading = false
        loadingMore = false
      }
    }
  }

  // Auto-apply: reload the first page immediately when any filter changes.
  // Wired to each <select>'s onchange so filters apply without a manual click.
  function onFilterChange() {
    loadAnomalies(true)
  }

  // --- Selection helpers (mutate in-place) ---
  function clearSelected() {
    selected.clear()
  }

  function toggleSelect(id) {
    if (selected.has(id)) selected.delete(id)
    else selected.add(id)
  }

  let allSelected = $derived(anomalies.length > 0 && selected.size === anomalies.length)

  function toggleSelectAll() {
    if (allSelected) {
      selected.clear()
    } else {
      selected.clear()
      for (const a of anomalies) selected.add(a.id)
    }
  }

  // --- Single-row ack ---
  async function ackRow(anomaly, action) {
    ackingId = anomaly.id
    try {
      await api.ackAnomaly(anomaly.id, action, '', 'user')
      anomalies = anomalies.filter(a => a.id !== anomaly.id)
      selected.delete(anomaly.id)
      showToast(`Anomaly ${action === 'dismiss' ? 'dismissed' : 'marked as expected'}`, 'success')
    } catch (e) {
      showToast(e.message || 'Failed to acknowledge', 'error')
    } finally {
      ackingId = null
    }
  }

  // --- Bulk ack ---
  async function bulkAck(action) {
    if (selected.size === 0) return
    bulkWorking = true
    try {
      const ids = [...selected]
      const res = await api.bulkAckAnomalies(ids, action, bulkReason, 'user')
      showToast(`${res?.acknowledged ?? ids.length} acknowledged, ${res?.skipped ?? 0} skipped`, 'success')
      selected.clear()
      bulkReason = ''
      await loadAnomalies(true)
    } catch (e) {
      showToast(e.message || 'Bulk ack failed', 'error')
    } finally {
      bulkWorking = false
    }
  }

  // --- Detail helpers ---
  function toggleDetail(id) {
    expandedId = expandedId === id ? null : id
  }

  function parsedDetails(anomaly) {
    if (!anomaly?.details) return null
    try { return JSON.parse(anomaly.details) } catch { return null }
  }

  // Metric → friendly value formatting for the anomaly detail block.
  const BYTE_METRICS = new Set(['total_bytes', 'total_bytes_low', 'free_bytes', 'free_bytes_low'])
  const DURATION_METRICS = new Set(['duration_seconds'])
  const DAY_METRICS = new Set(['free_bytes_eta_days'])

  function trimNum(n, dp) {
    return String(parseFloat(Number(n).toFixed(dp)))
  }

  // Render observed/expected values in units appropriate to the metric:
  // bytes → KB/MB/GB, seconds → m/s/h, ETA → days, everything else a trimmed number.
  function formatMetricValue(metric, value) {
    if (value == null) return '—'
    if (BYTE_METRICS.has(metric)) return formatBytes(value)
    if (DURATION_METRICS.has(metric)) return formatDuration(value)
    if (DAY_METRICS.has(metric)) return `${trimNum(value, 1)} days`
    return trimNum(value, 2)
  }

  // Deviation is a modified z-score (or growth factor); show ~2 dp instead of a
  // long float like -27.28833643436231.
  function formatDeviation(value) {
    if (value == null) return '—'
    if (!isFinite(value)) return value > 0 ? '∞' : '−∞'
    return trimNum(value, 2)
  }

  // --- Display helpers ---
  const stateLabel = {
    open:         'Open',
    resolved:     'Resolved',
    acknowledged: 'Acknowledged',
    expected:     'Expected',
  }

  function stateBadgeClass(state) {
    if (state === 'open') return 'bg-danger/10 text-danger'
    if (state === 'resolved') return 'bg-success/10 text-success'
    if (state === 'acknowledged') return 'bg-warning/10 text-warning'
    return 'bg-surface-4 text-text-muted'
  }

  function scopeLabel(a) {
    if (a.scope_kind === 'job') return `Job #${a.scope_id}`
    if (a.scope_kind === 'destination') return `Dest #${a.scope_id}`
    return a.scope_kind || '—'
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="mb-6">
    <h1 class="text-2xl font-bold text-text">Anomalies</h1>
    <p class="text-sm text-text-muted mt-1">Detected deviations from expected backup behaviour</p>
  </div>

  <!-- Filters -->
  <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6">
    <div class="flex flex-wrap gap-3 items-end">
      <div>
        <label for="filter-state" class="block text-xs font-medium text-text-muted mb-1">State</label>
        <select id="filter-state" bind:value={filterState} onchange={onFilterChange} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="">All</option>
          <option value="open">Open</option>
          <option value="acknowledged">Acknowledged</option>
          <option value="resolved">Resolved</option>
          <option value="expected">Expected</option>
        </select>
      </div>
      <div>
        <label for="filter-severity" class="block text-xs font-medium text-text-muted mb-1">Severity</label>
        <select id="filter-severity" bind:value={filterSeverity} onchange={onFilterChange} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="">All</option>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </select>
      </div>
      <div>
        <label for="filter-scope" class="block text-xs font-medium text-text-muted mb-1">Scope</label>
        <select id="filter-scope" bind:value={filterScope} onchange={onFilterChange} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="">All</option>
          <option value="job">Job</option>
          <option value="destination">Destination</option>
        </select>
      </div>
      <div>
        <label for="filter-since" class="block text-xs font-medium text-text-muted mb-1">Since</label>
        <select id="filter-since" bind:value={filterSince} onchange={onFilterChange} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="-7d">Last 7 days</option>
          <option value="-30d">Last 30 days</option>
          <option value="-90d">Last 90 days</option>
          <option value="">All time</option>
        </select>
      </div>
    </div>
  </div>

  <!-- Bulk action bar -->
  {#if selected.size > 0}
    <div class="bg-surface-2 border border-border rounded-xl px-4 py-3 mb-4 flex flex-wrap items-center gap-3">
      <span class="text-sm font-medium text-text">{selected.size} selected</span>
      <div class="w-px h-5 bg-border"></div>
      <input
        type="text"
        bind:value={bulkReason}
        placeholder="Reason (optional)"
        class="px-3 py-1.5 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim flex-1 min-w-[160px]"
      />
      <button
        onclick={() => bulkAck('dismiss')}
        disabled={bulkWorking}
        class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-warning hover:bg-warning/10 rounded-lg transition-colors disabled:opacity-40"
      >
        Dismiss all
      </button>
      <button
        onclick={() => bulkAck('mark_expected')}
        disabled={bulkWorking}
        class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-info hover:bg-info/10 rounded-lg transition-colors disabled:opacity-40"
      >
        Mark expected
      </button>
      <button onclick={clearSelected} class="text-xs text-text-muted hover:text-text transition-colors">
        Clear
      </button>
    </div>
  {/if}

  <!-- Table -->
  {#if loading}
    <Skeleton variant="card" count={4} />

  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg aria-hidden="true" class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
      </svg>
      <span class="text-sm">{error}</span>
    </div>

  {:else if anomalies.length === 0}
    <div class="bg-surface-2 border border-border rounded-xl px-5 py-12 text-center">
      <div class="w-12 h-12 rounded-full bg-success/10 flex items-center justify-center mx-auto mb-4">
        <svg aria-hidden="true" class="w-6 h-6 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
        </svg>
      </div>
      <p class="text-base font-semibold text-text">No anomalies found</p>
      <p class="text-sm text-text-muted mt-1">No anomalies match the current filters.</p>
    </div>

  {:else}
    <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
      <!-- Table header -->
      <div class="px-4 py-3 border-b border-border flex items-center gap-3 text-xs font-medium text-text-muted">
        <input
          type="checkbox"
          checked={allSelected}
          onchange={toggleSelectAll}
          class="accent-vault w-3.5 h-3.5 shrink-0"
          aria-label="Select all anomalies"
        />
        <span class="w-20 shrink-0">Severity</span>
        <span class="flex-1">Summary</span>
        <span class="w-24 shrink-0 hidden sm:block">Scope</span>
        <span class="w-20 shrink-0 hidden md:block">State</span>
        <span class="w-24 shrink-0 hidden lg:block">First seen</span>
        <span class="w-32 shrink-0">Actions</span>
      </div>

      <div class="divide-y divide-border">
        {#each anomalies as anomaly (anomaly.id)}
          <div class="hover:bg-surface-3 transition-colors {selected.has(anomaly.id) ? 'bg-vault/5' : ''}">
            <div class="px-4 py-3 flex items-start gap-3">
              <!-- Checkbox -->
              <input
                type="checkbox"
                checked={selected.has(anomaly.id)}
                onchange={() => toggleSelect(anomaly.id)}
                onclick={(e) => e.stopPropagation()}
                class="accent-vault w-3.5 h-3.5 shrink-0 mt-0.5 cursor-pointer"
                aria-label="Select anomaly {anomaly.id}"
              />

              <!-- Severity badge -->
              <div class="w-20 shrink-0">
                <AnomalyBadge count={1} severity={anomaly.severity} />
              </div>

              <!-- Summary + detector (click to expand inline) -->
              <button
                class="flex-1 min-w-0 text-left"
                onclick={() => toggleDetail(anomaly.id)}
                aria-expanded={expandedId === anomaly.id}
              >
                <p class="text-sm text-text leading-snug">{anomaly.summary}</p>
                <p class="text-xs text-text-dim mt-0.5">{anomaly.detector}</p>
              </button>

              <!-- Scope -->
              <span class="w-24 shrink-0 text-xs text-text-dim hidden sm:block">{scopeLabel(anomaly)}</span>

              <!-- State -->
              <span class="w-20 shrink-0 hidden md:block">
                <span class="text-[11px] px-2 py-0.5 rounded-full font-medium {stateBadgeClass(anomaly.state)}">
                  {stateLabel[anomaly.state] ?? anomaly.state}
                </span>
              </span>

              <!-- First seen -->
              <span class="w-24 shrink-0 text-xs text-text-dim hidden lg:block">{relTime(anomaly.first_seen_at)}</span>

              <!-- Per-row ack actions (only for open) -->
              <div class="w-32 shrink-0 flex items-center gap-1">
                {#if anomaly.state === 'open'}
                  <button
                    onclick={() => ackRow(anomaly, 'dismiss')}
                    disabled={ackingId === anomaly.id}
                    class="text-xs px-2 py-1 rounded-lg bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text transition-colors disabled:opacity-40"
                    title="Dismiss"
                  >
                    Dismiss
                  </button>
                  <button
                    onclick={() => ackRow(anomaly, 'mark_expected')}
                    disabled={ackingId === anomaly.id}
                    class="text-xs px-2 py-1 rounded-lg bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text transition-colors disabled:opacity-40"
                    title="Mark as expected"
                  >
                    Expected
                  </button>
                {:else}
                  <span class="text-xs text-text-dim italic">—</span>
                {/if}
              </div>
            </div>

            {#if expandedId === anomaly.id}
              {@const details = parsedDetails(anomaly)}
              <div class="px-4 pb-3">
                <div class="bg-surface-3 rounded-lg p-3 text-xs text-text-muted space-y-1 text-left">
                  <p><span class="font-medium text-text-dim">Metric:</span> {anomaly.metric}</p>
                  <p><span class="font-medium text-text-dim">Observed:</span> {formatMetricValue(anomaly.metric, anomaly.observed)}</p>
                  <p><span class="font-medium text-text-dim">Expected:</span> {formatMetricValue(anomaly.metric, anomaly.expected)}</p>
                  <p><span class="font-medium text-text-dim">Deviation:</span> {formatDeviation(anomaly.deviation)}</p>
                  {#if anomaly.ack_reason}
                    <p><span class="font-medium text-text-dim">Ack reason:</span> {anomaly.ack_reason}</p>
                  {/if}
                  {#if details}
                    <details class="mt-1">
                      <summary class="cursor-pointer text-text-dim hover:text-text">Raw details</summary>
                      <pre class="mt-1 whitespace-pre-wrap break-all text-[10px] font-mono">{JSON.stringify(details, null, 2)}</pre>
                    </details>
                  {/if}
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>

      <!-- Load more / keyset pagination -->
      {#if nextCursor}
        <div class="px-4 py-4 border-t border-border text-center">
          <button
            onclick={() => loadAnomalies(false)}
            disabled={loadingMore}
            class="text-sm font-medium text-vault hover:text-vault-dark transition-colors disabled:opacity-40"
          >
            {loadingMore ? 'Loading...' : 'Load more'}
          </button>
        </div>
      {/if}
    </div>
  {/if}
</div>
