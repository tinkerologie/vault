<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatDate, relTime, formatBytes } from '../lib/utils.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'

  let loading = $state(true)
  let error = $state('')
  let entries = $state([])
  let category = $state('')
  let limit = $state(100)
  let expandedIds = $state(new Set())

  // Real-time: prepend new activity entries via WebSocket instead of full reload
  onMount(() => {
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'activity' && msg.entry) {
        // If user is filtering by category, only show matching entries
        if (category && msg.entry.category !== category) return
        // Prepend the new entry — avoid duplicates by checking id
        if (!entries.some(e => e.id === msg.entry.id)) {
          entries = [msg.entry, ...entries].slice(0, limit)
        }
      } else if (msg.type === 'activity') {
        // Legacy: no entry payload, do a full reload
        loadLogs()
      }
    })
    return unsub
  })

  const categories = [
    { value: '', label: 'All' },
    { value: 'backup', label: 'Backup' },
    { value: 'restore', label: 'Restore' },
    { value: 'system', label: 'System' },
  ]

  async function loadLogs() {
    loading = true
    try {
      entries = (await api.getActivity(limit, category)) || []
      expandedIds = new Set()
    } catch (e) {
      error = e.message || 'Failed to load activity log'
      entries = []
    } finally {
      loading = false
    }
  }

  $effect(() => {
    // Re-fetch when category changes
    category
    loadLogs()
  })

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
      case 'system': return 'bg-purple-500/15 text-purple-400'
      default: return 'bg-surface-4 text-text-dim'
    }
  }

  function tryParseJSON(str) {
    if (!str) return null
    try { return JSON.parse(str) } catch { return null }
  }

  function toggleExpand(id) {
    const next = new Set(expandedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expandedIds = next
  }

  function formatDetailValue(key, value) {
    if (key === 'size_bytes') return formatBytes(value)
    if (key === 'duration_seconds') return `${value}s`
    if (Array.isArray(value)) return value.length ? value.join(', ') : '—'
    return String(value)
  }

  function detailLabel(key) {
    return key.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
  }
</script>

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Activity Log</h1>
      <p class="text-sm text-text-muted mt-1">System activity and backup operation history</p>
    </div>
    <button onclick={loadLogs} class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text transition-colors flex items-center gap-1.5" aria-label="Refresh">
      <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
      Refresh
    </button>
  </div>

  <!-- Filters -->
  <div class="flex items-center gap-2 mb-4">
    {#each categories as cat}
      <button
        type="button"
        onclick={() => (category = cat.value)}
        class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors {category === cat.value
          ? 'bg-vault text-white'
          : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}"
      >
        {cat.label}
      </button>
    {/each}
  </div>

  {#if loading}
    <Spinner text="Loading activity log..." />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{error}</span>
    </div>
  {:else if entries.length === 0}
    <EmptyState icon="📝" title="No activity yet" subtitle="Events are logged automatically" description="Activity from backup and restore operations will appear here." />
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
      <div class="divide-y divide-border">
        {#each entries as entry}
          <div class="px-5 py-3.5 hover:bg-surface-3/30 transition-colors">
            <div class="flex items-start gap-3">
              <div class="mt-0.5 shrink-0">
                <svg class="w-4 h-4 {entry.level === 'error' ? 'text-danger' : entry.level === 'warning' ? 'text-warning' : 'text-info'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={levelIcon(entry.level)} />
                </svg>
              </div>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 mb-0.5">
                  <span class="text-xs px-1.5 py-0.5 rounded font-medium {levelColor(entry.level)}">{entry.level}</span>
                  <span class="text-xs px-1.5 py-0.5 rounded font-medium {categoryBadge(entry.category)}">{entry.category}</span>
                  <span class="text-xs text-text-dim ml-auto shrink-0" title={formatDate(entry.created_at)}>{relTime(entry.created_at)}</span>
                </div>
                <p class="text-sm text-text">{entry.message}</p>
                {#if entry.details}
                  {@const parsed = tryParseJSON(entry.details)}
                  {#if parsed}
                    <!-- Structured JSON details -->
                    <button
                      type="button"
                      class="text-xs text-text-dim mt-1 flex items-center gap-1 hover:text-text transition-colors"
                      onclick={() => toggleExpand(entry.id)}
                    >
                      <svg class="w-3 h-3 transition-transform {expandedIds.has(entry.id) ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
                      </svg>
                      Details
                    </button>
                    {#if expandedIds.has(entry.id)}
                      <div class="mt-2 flex flex-wrap gap-1.5">
                        {#each Object.entries(parsed) as [key, value]}
                          {#if Array.isArray(value) && value.length > 0}
                            <span class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-danger/10 text-danger font-medium">
                              {detailLabel(key)}: {value.join(', ')}
                            </span>
                          {:else if !Array.isArray(value)}
                            <span class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-surface-4 text-text-dim">
                              <span class="text-text-dim/70">{detailLabel(key)}:</span> {formatDetailValue(key, value)}
                            </span>
                          {/if}
                        {/each}
                      </div>
                    {/if}
                  {:else}
                    <!-- Legacy plain-text details -->
                    <p class="text-xs text-text-dim mt-1 font-mono truncate">{entry.details}</p>
                  {/if}
                {/if}
              </div>
            </div>
          </div>
        {/each}
      </div>
    </div>
    <p class="text-xs text-text-dim mt-3 text-center">{entries.length} log entr{entries.length !== 1 ? 'ies' : 'y'}</p>
  {/if}
</div>
