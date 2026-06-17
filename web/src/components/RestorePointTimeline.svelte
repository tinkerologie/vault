<script>
  import { formatBytes, relTime } from '../lib/utils.js'

  let {
    points = [],
    selectedId = null,
    recommendedId = null,
    onSelect,
    onDelete,
    deletingId = null,
    confirmDeleteId = null,
    /** Optional (rp) => bytes for the displayed size (e.g. selected-items only). */
    sizeFor,
  } = $props()

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
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }

  // Group newest-first by calendar day. points arrive newest-first.
  let days = $derived.by(() => {
    const groups = []
    const byKey = {}
    for (const rp of points) {
      const d = new Date(rp.created_at)
      const key = dayKey(d)
      let g = byKey[key]
      if (!g) {
        g = { key, date: d, label: dayLabel(d), points: [], size: 0, hasFull: false }
        byKey[key] = g
        groups.push(g)
      }
      g.points.push(rp)
      g.size += rp.size_bytes || 0
      if ((rp.backup_type || '').toLowerCase() === 'full') g.hasFull = true
    }
    return groups
  })

  // Density strip runs oldest -> newest (left -> right).
  let strip = $derived([...days].reverse())
  let maxDaySize = $derived(Math.max(1, ...days.map((d) => d.size)))

  function scrollToDay(key) {
    const el = document.getElementById(`rpday-${key}`)
    if (!el) return
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
  }
</script>

<div class="space-y-4">
  <!-- Density overview: one bar per day with backups (oldest -> newest). -->
  {#if strip.length > 1}
    <div class="bg-surface-2 border border-border rounded-xl p-3">
      <div class="flex items-end gap-1 h-12" role="group" aria-label="Backup activity by day">
        {#each strip as d (d.key)}
          <button
            type="button"
            onclick={() => scrollToDay(d.key)}
            title="{d.label}: {d.points.length} backup{d.points.length !== 1 ? 's' : ''} · {formatBytes(d.size)}"
            aria-label="{d.label}: {d.points.length} backups, {formatBytes(d.size)}"
            class="flex-1 min-w-[3px] rounded-t cursor-pointer transition-colors hover:opacity-100 {d.hasFull ? 'bg-vault/70 hover:bg-vault' : 'bg-vault/30 hover:bg-vault/50'}"
            style="height: {Math.max(8, Math.round((d.size / maxDaySize) * 100))}%"
          ></button>
        {/each}
      </div>
      <div class="flex justify-between text-[10px] text-text-dim mt-1.5">
        <span>{strip[0].label}</span>
        <span>{strip[strip.length - 1].label}</span>
      </div>
    </div>
  {/if}

  <!-- Grouped timeline (newest first). -->
  {#each days as day (day.key)}
    <div id="rpday-{day.key}" class="scroll-mt-4">
      <div class="flex items-baseline justify-between mb-2">
        <h4 class="text-xs font-semibold text-text-muted">{day.label}</h4>
        <span class="text-[11px] text-text-dim">{day.points.length} backup{day.points.length !== 1 ? 's' : ''} · {formatBytes(day.size)}</span>
      </div>
      <div class="space-y-1.5 border-l border-border pl-4 ml-1">
        {#each day.points as rp (rp.id)}
          {@const meta = parseMeta(rp)}
          {@const selected = rp.id === selectedId}
          {@const recommended = rp.id === recommendedId}
          <div
            role="button"
            tabindex="0"
            onclick={() => onSelect?.(rp)}
            onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect?.(rp) } }}
            class="relative bg-surface-2 border rounded-lg p-3 cursor-pointer transition-all hover:shadow-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-vault/50
              {selected ? 'border-vault' : recommended ? 'border-vault/40 hover:border-vault/60' : 'border-border hover:border-vault/30'}"
          >
            <!-- timeline node -->
            <span class="absolute -left-[1.30rem] top-4 w-2.5 h-2.5 rounded-full border-2 {dotClass(rp)}" aria-hidden="true"></span>

            <div class="flex items-center justify-between gap-2">
              <div class="flex items-center gap-2 min-w-0 flex-wrap">
                <span class="text-sm font-medium text-text tabular-nums">{timeLabel(new Date(rp.created_at))}</span>
                <span class="text-[11px] px-1.5 py-0.5 rounded-full font-medium uppercase bg-vault/10 text-vault">{rp.backup_type}</span>
                {#if recommended}
                  <span class="text-[11px] px-1.5 py-0.5 rounded-full bg-success/15 text-success font-medium">Recommended</span>
                {/if}
                {#if meta.verified}
                  <span class="text-[11px] px-1.5 py-0.5 rounded-full bg-info/15 text-info font-medium">Verified</span>
                {/if}
                {#if rp.chain_status === 'broken'}
                  <span class="text-[11px] px-1.5 py-0.5 rounded-full font-medium bg-danger/15 text-danger">Broken chain</span>
                {:else if chainDeps(rp) > 0}
                  <span class="text-[11px] px-1.5 py-0.5 rounded-full font-medium bg-info/15 text-info">Chain ×{rp.chain_depth}</span>
                {/if}
                {#if rp.retention_preserved}
                  <span class="text-[11px] px-1.5 py-0.5 rounded-full font-medium bg-warning/15 text-warning">Retained for chain</span>
                {/if}
              </div>
              <div class="flex items-center gap-2 shrink-0">
                <span class="text-xs text-text-dim" title={relTime(rp.created_at)}>{relTime(rp.created_at)}</span>
                {#if onDelete}
                  <button
                    type="button"
                    onclick={(e) => { e.stopPropagation(); onDelete(rp) }}
                    disabled={deletingId === rp.id}
                    title={confirmDeleteId === rp.id ? 'Click again to confirm' : 'Delete this backup'}
                    aria-label="Delete restore point"
                    class="p-1 rounded transition-colors {confirmDeleteId === rp.id ? 'text-danger hover:text-danger/80' : 'text-text-dim hover:text-danger'} disabled:opacity-40 cursor-pointer"
                  >
                    {#if deletingId === rp.id}
                      <svg class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                    {:else}
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
                    {/if}
                  </button>
                {/if}
              </div>
            </div>

            <div class="flex items-center gap-4 text-xs text-text-dim mt-1.5">
              <span>{formatBytes(displaySize(rp))}{displaySize(rp) !== rp.size_bytes ? ' selected' : ''}</span>
              {#if rp.jobName}<span class="text-text-muted truncate">{rp.jobName}</span>{/if}
              {#if meta.items}
                {@const itemCount = Array.isArray(meta.items) ? meta.items.length : meta.items}
                <span>{itemCount} item{itemCount !== 1 ? 's' : ''}</span>
              {/if}
            </div>

            {#if rp.chain_status === 'broken'}
              <p class="mt-2 text-xs text-danger">{rp.chain_warning}</p>
            {:else if chainDeps(rp) > 0}
              <p class="mt-2 text-xs text-info">Restore replays {chainDeps(rp)} earlier backup{chainDeps(rp) === 1 ? '' : 's'} in this chain.</p>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/each}
</div>
