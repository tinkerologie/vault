<script>
  import { formatBytes, formatDate, relTime, itemTypeCountLabel } from '../lib/utils.js'
  import { getPointCoverage } from '../lib/restore-plan.js'
  import { isEmbedded } from '../lib/runtime-config.js'
  import { scrollHostTo } from '../lib/embed.js'
  import InlineSpinner from './InlineSpinner.svelte'
  import { ICONS } from '../lib/icons.js'

  let {
    points = [],
    selectedId = null,
    chosenPoints = null,
    recommendedId = null,
    onSelect,
    onDelete,
    deletingId = null,
    /** Optional (rp) => bytes for the displayed size (e.g. selected-items only). */
    sizeFor,
    /** Optional item type for type-specific nouns in count labels */
    itemType = '',
    selectedItems = [],
  } = $props()

  function isSelected(rp) {
    if (rp.id === selectedId) return true
    if (chosenPoints) {
      if (chosenPoints instanceof Set) return chosenPoints.has(rp.id)
      if (chosenPoints instanceof Map) {
        const chosen = chosenPoints.get(rp.jobId ?? rp.job_id)
        return chosen?.id === rp.id || chosen === rp.id
      }
      if (Array.isArray(chosenPoints)) return chosenPoints.includes(rp.id)
      if (typeof chosenPoints === 'object') {
        const chosen = chosenPoints[rp.jobId ?? rp.job_id]
        return chosen?.id === rp.id || chosen === rp.id
      }
    }
    return false
  }

  function parseMeta(rp) {
    if (!rp?.metadata) return {}
    try { return JSON.parse(rp.metadata) } catch { return {} }
  }
  function chainDeps(rp) {
    return Math.max(0, (rp?.chain_depth || 1) - 1)
  }
  function displaySize(rp) {
    return sizeFor ? sizeFor(rp) : rp.size_bytes
  }

  const uid = $props.id()

  // Type marker: ● full / ◐ differential / ○ incremental.
  function dotClass(rp) {
    const t = (rp.backup_type || '').toLowerCase()
    if (t === 'full') return 'bg-vault border-vault'
    if (t === 'differential') return 'bg-warning border-warning'
    return 'bg-transparent border-vault' // incremental
  }

  function dayKey(d) {
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  }
  function dayLabel(d) {
    const today = new Date()
    const k = dayKey(d)
    if (k === dayKey(today)) return 'Today'
    if (k === dayKey(new Date(today.getTime() - 86400000))) return 'Yesterday'
    return d.toLocaleDateString([], { weekday: 'short', year: 'numeric', month: 'short', day: 'numeric' })
  }
  function timeLabel(d) {
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  // Normalise metadata item count to a number, or null when absent/unknown.
  function itemCountOf(rp) {
    const items = parseMeta(rp).items
    if (Array.isArray(items)) return items.length
    return typeof items === 'number' ? items : null
  }

  // Group newest-first by calendar day. points arrive newest-first.
  let days = $derived.by(() => {
    const groups = []
    const byKey = {}
    for (const rp of points) {
      const d = new Date(rp.created_at)
      const valid = !Number.isNaN(d.getTime())
      const key = valid ? dayKey(d) : 'unknown'
      let g = byKey[key]
      if (!g) {
        g = { key, label: valid ? dayLabel(d) : 'Unknown date', points: [], size: 0, hasFull: false, types: {} }
        byKey[key] = g
        groups.push(g)
      }
      g.points.push(rp)
      g.size += rp.size_bytes || 0
      const type = (rp.backup_type || 'unknown').toLowerCase()
      if (type === 'full') g.hasFull = true
      g.types[type] = (g.types[type] || 0) + 1
    }
    return groups
  })

  // Density strip runs oldest -> newest (left -> right).
  let strip = $derived([...days].reverse())
  let maxDaySize = $derived(Math.max(1, ...days.map((d) => d.size)))

  let graphLabels = $derived.by(() => {
    if (strip.length <= 1) return []
    if (strip.length === 2) {
      return [
        { text: strip[0].label, align: 'text-left' },
        { text: strip[1].label, align: 'text-right' },
      ]
    }
    const midIdx = Math.floor((strip.length - 1) / 2)
    return [
      { text: strip[0].label, align: 'text-left' },
      { text: strip[midIdx].label, align: 'text-center' },
      { text: strip[strip.length - 1].label, align: 'text-right' },
    ]
  })

  const TYPE_ORDER = ['full', 'differential', 'incremental']

  function plural(n, one, many = `${one}s`) {
    return `${n} ${n === 1 ? one : many}`
  }

  // "2 restore points (1 full, 1 incremental)": the strip names its backup
  // types in words, not by bar style alone (#704).
  function dayCountLabel(day) {
    const rank = (k) => (TYPE_ORDER.indexOf(k) < 0 ? 99 : TYPE_ORDER.indexOf(k))
    const kinds = Object.keys(day.types).sort((a, b) => rank(a) - rank(b))
    const detail = kinds.length === 1 ? kinds[0] : kinds.map((k) => `${day.types[k]} ${k}`).join(', ')
    return `${plural(day.points.length, 'restore point')} (${detail})`
  }

  function pointLabel(rp) {
    const d = new Date(rp.created_at)
    const when = Number.isNaN(d.getTime()) ? 'Unknown date' : `${dayLabel(d)} ${timeLabel(d)}`
    return [`Restore point ${when}`, rp.backup_type, rp.jobName].filter(Boolean).join(', ')
  }

  function coverageText(coverage) {
    if (coverage.coversAll) {
      return coverage.coverageCount === 1 ? 'Contains the selected item' : `Contains all ${coverage.coverageCount} selected items`
    }
    return `Contains ${coverage.coverageCount} of ${plural(coverage.totalSelected, 'selected item')}`
  }

  function scrollToDay(key) {
    const el = document.getElementById(`rpday-${key}`)
    if (!el) return
    if (isEmbedded()) {
      // Nothing in the frame scrolls: the host page does.
      scrollHostTo(el.getBoundingClientRect().top)
      return
    }
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
  }
</script>

<div class="space-y-4">
  <!-- Density overview: one bar per day with backups (oldest -> newest). Each
       bar is a full-height target at least 24px wide; a long history scrolls. -->
  {#if strip.length > 1}
    <div class="bg-surface-2 border border-border rounded-xl p-3">
      <div class="overflow-x-auto">
        <div class="flex items-end gap-1 h-12 min-w-full w-max" role="group" aria-label="Backup activity by day">
          {#each strip as d (d.key)}
            <button
              type="button"
              onclick={() => scrollToDay(d.key)}
              title="{d.label}: {dayCountLabel(d)} · {formatBytes(d.size)}"
              aria-label="{d.label}: {dayCountLabel(d)}, {formatBytes(d.size)}"
              class="group flex-1 min-w-6 h-full flex items-end cursor-pointer rounded-t"
            >
              <span
                aria-hidden="true"
                class="block w-full rounded-t transition-colors {d.hasFull ? 'bg-vault/70 group-hover:bg-vault' : 'border-2 border-b-0 border-vault bg-transparent group-hover:bg-vault/20'}"
                style="height: {Math.max(8, Math.round((d.size / maxDaySize) * 100))}%"
              ></span>
            </button>
          {/each}
        </div>
      </div>
      <div class="flex justify-between text-xs text-text-dim mt-1.5">
        {#each graphLabels as label (label.align)}
          <span class={label.align}>{label.text}</span>
        {/each}
      </div>
      <p class="flex items-center gap-3 flex-wrap text-xs text-text-dim mt-1">
        <span class="inline-flex items-center gap-1"><span aria-hidden="true" class="inline-block w-2.5 h-2.5 rounded-sm bg-vault/70"></span>Includes a full backup</span>
        <span class="inline-flex items-center gap-1"><span aria-hidden="true" class="inline-block w-2.5 h-2.5 rounded-sm border-2 border-vault"></span>Incremental only</span>
      </p>
    </div>
  {/if}

  <!-- Grouped timeline (newest first). -->
  {#each days as day (day.key)}
    <div id="rpday-{day.key}" class="scroll-mt-4">
      <div class="flex items-baseline justify-between mb-2">
        <h3 class="text-xs font-semibold text-text-muted">{day.label}</h3>
        <span class="text-xs text-text-dim font-medium tabular-nums">{formatBytes(day.size)}</span>
      </div>
      <ul class="space-y-1.5 border-l border-border pl-4 ml-1">
        {#each day.points as rp (rp.id)}
          {@const meta = parseMeta(rp)}
          {@const selected = isSelected(rp)}
          {@const recommended = rp.id === recommendedId}
          {@const dsz = displaySize(rp)}
          {@const itemCount = itemCountOf(rp)}
          {@const coverage = selectedItems.length > 0 ? getPointCoverage(rp, selectedItems) : null}
          <li class="relative flex items-stretch gap-1 bg-surface-2 border rounded-lg transition-colors
              {selected ? 'border-vault ring-1 ring-vault' : recommended ? 'border-vault/40 hover:border-vault/60' : 'border-border hover:border-vault/30'}">
            <!-- timeline node -->
            <span class="absolute -left-[1.30rem] top-4 w-2.5 h-2.5 rounded-full border-2 {dotClass(rp)}" aria-hidden="true"></span>

            <!-- The point is one pressed/not-pressed button; its actions sit beside it, never inside (#704). -->
            <button
              type="button"
              aria-pressed={selected}
              aria-label={pointLabel(rp)}
              aria-describedby="{uid}-rp-{rp.id}"
              onclick={() => onSelect?.(rp)}
              class="flex-1 min-w-0 text-left p-3 rounded-lg cursor-pointer"
            >
              <span id="{uid}-rp-{rp.id}" class="block">
                <span class="flex items-center gap-2 min-w-0 flex-wrap">
                  <span class="text-sm font-medium text-text tabular-nums">{timeLabel(new Date(rp.created_at))}</span>
                  <span class="text-xs px-1.5 py-0.5 rounded-full font-medium uppercase bg-vault/10 text-vault-text">{rp.backup_type}</span>
                  {#if selected}
                    <span class="text-xs px-1.5 py-0.5 rounded-full bg-vault text-on-vault font-medium">Selected</span>
                  {/if}
                  {#if rp.jobName}
                    <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text font-medium">{rp.jobName}</span>
                  {/if}
                  {#if recommended}
                    <span class="text-xs px-1.5 py-0.5 rounded-full bg-surface-3 text-text-muted font-medium">Recommended</span>
                  {/if}
                  {#if meta.verified}
                    <span class="text-xs px-1.5 py-0.5 rounded-full bg-success/15 text-success font-medium">Verified</span>
                  {/if}
                  {#if rp.chain_status === 'broken'}
                    <span class="text-xs px-1.5 py-0.5 rounded-full font-medium bg-danger/15 text-danger">Broken chain</span>
                  {:else if chainDeps(rp) > 0}
                    <span class="text-xs px-1.5 py-0.5 rounded-full font-medium bg-info/15 text-info">Chain ×{rp.chain_depth}</span>
                  {/if}
                  {#if rp.retention_preserved}
                    <span class="text-xs px-1.5 py-0.5 rounded-full font-medium bg-warning/15 text-warning">Retained for chain</span>
                  {/if}
                  <span class="text-xs text-text-dim ml-auto" title={formatDate(rp.created_at)}>{relTime(rp.created_at)}</span>
                </span>

                <span class="flex items-center gap-4 text-xs text-text-dim mt-1.5 flex-wrap tabular-nums">
                  {#if dsz !== rp.size_bytes}
                    <span title="Estimated restore size for selected items"><span class="text-text-muted">Restore:</span> {formatBytes(dsz)}</span>
                    <span title="Total backup archive size"><span class="text-text-muted">Backup:</span> {formatBytes(rp.size_bytes)}</span>
                  {:else}
                    <span title="Backup archive size"><span class="text-text-muted">Size:</span> {formatBytes(rp.size_bytes)}</span>
                  {/if}
                  {#if itemCount != null}
                    <span>{itemTypeCountLabel(itemCount, itemType)}</span>
                  {/if}
                </span>

                {#if coverage}
                  <span class="block mt-2 text-xs">
                    {#if coverage.isLegacy}
                      <span class="px-1.5 py-0.5 rounded bg-surface-3 text-text-muted">Item list not recorded for this backup</span>
                    {:else if coverage.coversAll}
                      <span class="text-hue-emerald font-medium">{coverageText(coverage)}</span>
                    {:else}
                      <span class="text-hue-amber font-medium">{coverageText(coverage)}</span>
                    {/if}
                    {#if coverage.missingItems.length > 0}
                      <span class="block mt-1 space-y-0.5 text-text-dim">
                        {#each coverage.missingItems as missing (`${missing.item?.type || missing.item?.item_type || 'item'}:${missing.name}`)}
                          <span class="block"><span class="text-text-muted">{missing.name}:</span> {missing.reason}</span>
                        {/each}
                      </span>
                    {/if}
                  </span>
                {/if}

                {#if rp.chain_status === 'broken'}
                  <span class="block mt-2 text-xs text-danger">{rp.chain_warning}</span>
                {:else if chainDeps(rp) > 0}
                  <span class="block mt-2 space-y-0.5 text-xs">
                    <span class="block text-info">Restore replays {chainDeps(rp)} earlier backup{chainDeps(rp) === 1 ? '' : 's'} in this chain.</span>
                    {#if rp.base_full_created_at}
                      <span class="block text-text-muted">Based on the full backup from {formatDate(rp.base_full_created_at)}{rp.base_full_size_bytes ? ` (${formatBytes(rp.base_full_size_bytes)})` : ''}.</span>
                    {/if}
                  </span>
                {/if}
              </span>
            </button>

            {#if onDelete}
              <div class="shrink-0 flex items-start p-2">
                <button
                  type="button"
                  onclick={() => onDelete(rp)}
                  disabled={deletingId === rp.id}
                  title="Delete this restore point"
                  aria-label="{deletingId === rp.id ? 'Deleting' : 'Delete'} restore point {pointLabel(rp).replace(/^Restore point /, '')}"
                  class="hit-target inline-flex items-center justify-center w-8 h-8 rounded-lg text-text-dim hover:text-danger hover:bg-danger/10 transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  {#if deletingId === rp.id}
                    <InlineSpinner size="sm" />
                  {:else}
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.trash}/></svg>
                  {/if}
                </button>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    </div>
  {/each}
</div>
