<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { formatDate, relTime, formatBytes } from '../lib/utils.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { getLiveMode } from '../lib/runtime-config.js'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'

  let loading = $state(true)
  let error = $state('')
  let entries = $state([])
  let category = $state('')
  let levelFilter = $state('')
  let limit = $state(100)
  let expandedIds = $state(new SvelteSet())
  let autoScroll = $state(true)
  let logContainer = $state(null)
  let copiedId = $state(null)
  let confirmPurge = $state(false)
  let purging = $state(false)
  const liveMode = getLiveMode()

  // Real-time: prepend new activity entries via WebSocket instead of full reload
  onMount(() => {
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'activity' && msg.entry) {
        if (category && msg.entry.category !== category) return
        if (!entries.some(e => e.id === msg.entry.id)) {
          entries = [msg.entry, ...entries].slice(0, limit)
          // Auto-expand errors
          if (msg.entry.level === 'error' && msg.entry.details) {
            expandedIds.add(msg.entry.id)
          }
          if (autoScroll && logContainer) {
            requestAnimationFrame(() => logContainer.scrollTop = 0)
          }
        }
      } else if (msg.type === 'activity') {
        loadLogs()
      }
    })
    const pollTimer = liveMode === 'poll' ? setInterval(() => { loadLogs() }, 5000) : null
    return () => {
      unsub()
      if (pollTimer) clearInterval(pollTimer)
    }
  })

  const categories = [
    { value: '', label: 'All' },
    { value: 'backup', label: 'Backup' },
    { value: 'restore', label: 'Restore' },
    { value: 'health', label: 'Health' },
    { value: 'system', label: 'System' },
  ]

  const levels = [
    { value: '', label: 'All Levels' },
    { value: 'error', label: 'Error' },
    { value: 'warning', label: 'Warning' },
    { value: 'info', label: 'Info' },
  ]

  async function loadLogs() {
    loading = true
    try {
      entries = (await api.getActivity(limit, category)) || []
      expandedIds.clear()
      // Auto-expand errors
      for (const e of entries) {
        if (e.level === 'error' && e.details) expandedIds.add(e.id)
      }
    } catch (e) {
      error = e.message || 'Failed to load activity log'
      entries = []
    } finally {
      loading = false
    }
  }

  $effect(() => {
    category
    loadLogs()
  })

  let filteredEntries = $derived(
    levelFilter ? entries.filter(e => e.level === levelFilter) : entries
  )

  function levelColor(level) {
    switch (level) {
      case 'error': return 'bg-danger/15 text-danger'
      case 'warning': return 'bg-warning/15 text-warning'
      case 'info': return 'bg-info/15 text-info'
      default: return 'bg-surface-4 text-text-dim'
    }
  }

  function levelIcon(level) {
    switch (level) {
      case 'error': return 'M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z'
      case 'warning': return 'M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z'
      case 'info': return 'M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z'
      default: return 'M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z'
    }
  }

  function categoryBadge(cat) {
    switch (cat) {
      case 'backup': return 'bg-vault/15 text-vault'
      case 'restore': return 'bg-blue-500/15 text-blue-400'
      case 'health': return 'bg-green-500/15 text-green-400'
      case 'system': return 'bg-purple-500/15 text-purple-400'
      default: return 'bg-surface-4 text-text-dim'
    }
  }

  function tryParseJSON(str) {
    if (!str) return null
    try { return JSON.parse(str) } catch { return null }
  }

  function toggleExpand(id) {
    if (expandedIds.has(id)) expandedIds.delete(id)
    else expandedIds.add(id)
  }

  function formatDetailValue(key, value) {
    if (key === 'size_bytes') return formatBytes(value)
    if (key === 'duration_seconds') return `${value}s`
    if (key === 'duration_ms') return `${value}ms`
    if (key === 'backup_type') return String(value).charAt(0).toUpperCase() + String(value).slice(1)
    if (key === 'containers_checked') return `${value} checked`
    if (key === 'containers_healthy') return `${value} healthy`
    if (key === 'containers_unhealthy') return `${value} unhealthy`
    if (Array.isArray(value)) return value.length ? value.join(', ') : '—'
    return String(value)
  }

  function detailLabel(key) {
    return key.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
  }

  async function copyEntry(entry) {
    const text = `[${entry.level?.toUpperCase()}] [${entry.category}] ${entry.message}${entry.details ? '\n' + entry.details : ''}`
    try {
      await navigator.clipboard.writeText(text)
      copiedId = entry.id
      setTimeout(() => { copiedId = null }, 2000)
    } catch { /* ignore */ }
  }

  function exportLogs() {
    const lines = filteredEntries.map(e => {
      const ts = formatDate(e.created_at)
      return `[${ts}] [${e.level?.toUpperCase()}] [${e.category}] ${e.message}${e.details ? ' — ' + e.details : ''}`
    })
    const blob = new Blob([lines.join('\n')], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `vault-logs-${new Date().toISOString().slice(0, 10)}.txt`
    a.click()
    URL.revokeObjectURL(url)
  }

  async function handlePurge() {
    purging = true
    try {
      await api.purgeActivity()
      confirmPurge = false
      await loadLogs()
    } catch (e) {
      confirmPurge = false
      error = e.message || 'Failed to purge logs'
    } finally {
      purging = false
    }
  }
</script>

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Activity Log</h1>
      <p class="text-sm text-text-muted mt-1">System activity and backup operation history</p>
    </div>
    <div class="flex items-center gap-2">
      <!-- Auto-scroll toggle -->
      <button
        role="switch"
        aria-checked={autoScroll}
        onclick={() => autoScroll = !autoScroll}
        class="flex items-center gap-2 text-xs text-text-muted hover:text-text transition-colors"
        title="Auto-scroll to new entries"
      >
        <span class="relative inline-flex h-4 w-7 shrink-0 items-center rounded-full transition-colors {autoScroll ? 'bg-vault' : 'bg-surface-4'}">
          <span class="inline-block h-3 w-3 rounded-full bg-white shadow transition-transform {autoScroll ? 'translate-x-3.5' : 'translate-x-0.5'}"></span>
        </span>
        Auto-scroll
      </button>
      <!-- Export button -->
      <button onclick={exportLogs} disabled={filteredEntries.length === 0}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text transition-colors flex items-center gap-1.5 disabled:opacity-40" title="Export logs">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
        Export
      </button>
      <!-- Purge button -->
      <button onclick={() => confirmPurge = true} disabled={filteredEntries.length === 0}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-danger hover:bg-danger/10 transition-colors flex items-center gap-1.5 disabled:opacity-40" title="Purge all logs">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
        Purge
      </button>
      <button onclick={loadLogs} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text transition-colors flex items-center gap-1.5" aria-label="Refresh">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
        Refresh
      </button>
    </div>
  </div>

  <!-- Filters -->
  <div class="flex flex-wrap items-center gap-2 mb-4">
    <div role="group" aria-label="Filter by category" class="flex items-center gap-2 flex-wrap">
      {#each categories as cat (cat.value)}
        <button
          type="button"
          onclick={() => (category = cat.value)}
          aria-pressed={category === cat.value}
          class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors {category === cat.value
            ? 'bg-vault text-white'
            : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}"
        >
          {cat.label}
        </button>
      {/each}
    </div>
    <div class="w-px h-5 bg-border" aria-hidden="true"></div>
    <div role="group" aria-label="Filter by level" class="flex items-center gap-2 flex-wrap">
      {#each levels as lev (lev.value)}
        <button
          type="button"
          onclick={() => (levelFilter = lev.value)}
          aria-pressed={levelFilter === lev.value}
          class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors {levelFilter === lev.value
            ? (lev.value === 'error' ? 'bg-danger text-white' : lev.value === 'warning' ? 'bg-warning text-white' : 'bg-vault text-white')
            : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}"
        >
          {lev.label}
        </button>
      {/each}
    </div>
  </div>

  {#if loading}
    <Spinner text="Loading activity log..." />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg aria-hidden="true" class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{error}</span>
    </div>
  {:else if filteredEntries.length === 0}
    <EmptyState title="No activity yet" subtitle="Events are logged automatically" description="Activity from backup and restore operations will appear here.">
      {#snippet iconSlot()}
        <svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl overflow-hidden" bind:this={logContainer}>
      <div class="divide-y divide-border">
        {#each filteredEntries as entry (entry.id)}
          <div class="px-5 py-3.5 hover:bg-surface-3/30 transition-colors {entry.level === 'error' ? 'border-l-2 border-l-danger' : ''} group">
            <div class="flex items-start gap-3">
              <div class="mt-0.5 shrink-0">
                <svg aria-hidden="true" class="w-4 h-4 {entry.level === 'error' ? 'text-danger' : entry.level === 'warning' ? 'text-warning' : 'text-info'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={levelIcon(entry.level)} />
                </svg>
              </div>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 mb-0.5">
                  <span class="text-xs px-1.5 py-0.5 rounded font-medium {levelColor(entry.level)}">{entry.level}</span>
                  <span class="text-xs px-1.5 py-0.5 rounded font-medium {categoryBadge(entry.category)}">{entry.category}</span>
                  <span class="text-xs text-text-dim ml-auto shrink-0" title={formatDate(entry.created_at)}>{relTime(entry.created_at)}</span>
                  <!-- Copy button -->
                  <button type="button" onclick={(e) => { e.stopPropagation(); copyEntry(entry) }}
                    class="opacity-0 group-hover:opacity-100 p-1 text-text-dim hover:text-text rounded transition-all" title="Copy entry">
                    {#if copiedId === entry.id}
                      <svg aria-hidden="true" class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
                    {:else}
                      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
                    {/if}
                  </button>
                </div>
                <p class="text-sm text-text {entry.level === 'error' ? 'font-medium' : ''}">{entry.message}</p>
                {#if entry.details}
                  {@const parsed = tryParseJSON(entry.details)}
                  {#if parsed}
                    <button
                      type="button"
                      class="text-xs text-text-dim mt-1 flex items-center gap-1 hover:text-text transition-colors"
                      onclick={() => toggleExpand(entry.id)}
                    >
                      <svg aria-hidden="true" class="w-3 h-3 transition-transform {expandedIds.has(entry.id) ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
                      </svg>
                      Details
                    </button>
                    {#if expandedIds.has(entry.id)}
                      <div class="mt-2 flex flex-wrap gap-1.5">
                        {#each Object.entries(parsed) as [key, value] (key)}
                          {#if key === 'summary' && value && typeof value === 'object' && !Array.isArray(value)}
                            {#each Object.entries(value) as [sk, sv] (sk)}
                              <span class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-surface-4 text-text-dim">
                                <span class="text-text-dim/70">{detailLabel(sk)}:</span> {formatDetailValue(sk, sv)}
                              </span>
                            {/each}
                          {:else if key === 'results' && Array.isArray(value)}
                            <!-- Skip results array in badge view — summary covers it -->
                          {:else if value === null || value === undefined}
                            <!-- Skip null values -->
                          {:else if Array.isArray(value) && value.length > 0}
                            <span class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-danger/10 text-danger font-medium">
                              {detailLabel(key)}: {value.join(', ')}
                            </span>
                          {:else if !Array.isArray(value) && (typeof value !== 'object' || value === null)}
                            <span class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-surface-4 text-text-dim">
                              <span class="text-text-dim/70">{detailLabel(key)}:</span> {formatDetailValue(key, value)}
                            </span>
                          {/if}
                        {/each}
                      </div>
                    {/if}
                  {:else}
                    <p class="text-xs text-text-dim mt-1 font-mono truncate">{entry.details}</p>
                  {/if}
                {/if}
              </div>
            </div>
          </div>
        {/each}
      </div>
    </div>
    <p class="text-xs text-text-dim mt-3 text-center">{filteredEntries.length} log entr{filteredEntries.length !== 1 ? 'ies' : 'y'}</p>
  {/if}
</div>

<ConfirmDialog
  show={confirmPurge}
  title="Purge All Logs"
  message="This will permanently delete all activity log entries. This action cannot be undone."
  confirmLabel={purging ? 'Purging...' : 'Purge All'}
  variant="danger"
  onconfirm={handlePurge}
  oncancel={() => confirmPurge = false}
/>
