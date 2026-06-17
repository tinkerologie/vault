<script>
  import { formatBytes } from '../lib/utils.js'

  let { runs = [], jobs = [] } = $props()

  // Four stable backup-target categories. We deliberately keep the palette
  // small (Veeam / Duplicati / Backblaze converge on the same idea) so the
  // chart is readable at a glance instead of an unreadable rainbow.
  const CATEGORIES = [
    { key: 'containers', label: 'Containers', color: 'var(--color-info, #3b82f6)' },
    { key: 'vms',        label: 'VMs',        color: 'var(--color-vault, #8b5cf6)' },
    { key: 'folders',    label: 'Folders & Files', color: 'var(--color-success, #10b981)' },
    { key: 'flash',      label: 'Flash',      color: 'var(--color-warning, #f59e0b)' },
    { key: 'other',      label: 'Other',      color: 'var(--color-text-dim, #6b7280)' },
  ]

  // Map a raw JobItem.item_type to one of our four headline categories.
  // Anything we don't recognise falls into "other" so the chart can still
  // attribute a size to it instead of dropping the run silently.
  function classifyItemType(itemType) {
    const t = String(itemType || '').toLowerCase()
    if (t === 'container' || t === 'docker') return 'containers'
    if (t === 'vm' || t === 'libvirt') return 'vms'
    if (t === 'folder' || t === 'folders' || t === 'file' || t === 'files' || t === 'path') return 'folders'
    if (t === 'flash' || t === 'usb' || t === 'boot') return 'flash'
    return 'other'
  }

  // Build {jobId -> dominant category} so each run can be coloured by
  // the kind of data it backed up. Mixed jobs pick their largest item type;
  // ties go to whichever appears first in CATEGORIES.
  let jobCategory = $derived.by(() => {
    const m = new Map()
    for (const j of jobs || []) {
      const counts = new Map()
      for (const it of j.items || []) {
        const c = classifyItemType(it.item_type)
        counts.set(c, (counts.get(c) || 0) + 1)
      }
      let best = 'other'
      let bestN = -1
      for (const cat of CATEGORIES) {
        const n = counts.get(cat.key) || 0
        if (n > bestN) { bestN = n; best = cat.key }
      }
      m.set(j.id, best)
    }
    return m
  })

  // Extract completed runs and tag each with its category. Failed runs have
  // partial/misleading sizes so we keep the original filter.
  let dataPoints = $derived.by(() => {
    return runs
      .filter(r => r.size_bytes > 0 && r.started_at && (r.status === 'completed' || r.status === 'success'))
      .map(r => ({
        date: new Date(r.started_at),
        size: r.size_bytes,
        name: r.jobName,
        category: jobCategory.get(r.job_id) || 'other',
      }))
      .sort((a, b) => a.date.getTime() - b.date.getTime())
      .slice(-30)
  })

  // Which categories actually appear in the current dataset – used to
  // render only the legend chips that matter and to control which stacked
  // bands are drawn. Categories are kept in the canonical CATEGORIES order
  // so the colour assignment is stable across re-renders.
  let activeCategories = $derived.by(() => {
    const present = new Set(dataPoints.map(p => p.category))
    return CATEGORIES.filter(c => present.has(c.key))
  })

  let hoveredIndex = $state(-1)

  const width = 600
  const height = 140
  const padding = { top: 10, right: 10, bottom: 22, left: 10 }
  let chartWidth = width - padding.left - padding.right
  let chartHeight = height - padding.top - padding.bottom

  // Per-bar width – using bars (one stacked column per run) reads more
  // clearly than a stacked line when each x-value is a single run.
  let barWidth = $derived.by(() => {
    if (dataPoints.length === 0) return 0
    const slotWidth = chartWidth / dataPoints.length
    return Math.max(2, slotWidth * 0.7)
  })

  function xCenter(i) {
    if (dataPoints.length <= 1) return chartWidth / 2
    const slotWidth = chartWidth / dataPoints.length
    return slotWidth * (i + 0.5)
  }

  let yMax = $derived(Math.max(...dataPoints.map(p => p.size), 1))

  function y(size) {
    return chartHeight - (size / yMax) * chartHeight
  }

  function colorFor(catKey) {
    const c = CATEGORIES.find(c => c.key === catKey)
    return c ? c.color : 'var(--color-text-dim)'
  }

  function labelFor(catKey) {
    const c = CATEGORIES.find(c => c.key === catKey)
    return c ? c.label : 'Other'
  }

  // Same single-job heuristic as before – the linear-regression trend
  // percentage is only meaningful when every point belongs to one job.
  let isSingleJob = $derived(new Set(dataPoints.map(p => p.name).filter(Boolean)).size <= 1)

  let trend = $derived.by(() => {
    if (dataPoints.length < 2) return null
    const n = dataPoints.length
    const sumX = (n * (n - 1)) / 2
    const sumX2 = (n * (n - 1) * (2 * n - 1)) / 6
    const sumY = dataPoints.reduce((s, p) => s + p.size, 0)
    const sumXY = dataPoints.reduce((s, p, i) => s + i * p.size, 0)
    const slope = (n * sumXY - sumX * sumY) / (n * sumX2 - sumX * sumX)
    const intercept = (sumY - slope * sumX) / n
    const firstPred = intercept
    const lastPred = intercept + slope * (n - 1)
    if (firstPred <= 0) return null
    const pct = ((lastPred - firstPred) / firstPred) * 100
    const direction = pct > 5 ? 'up' : pct < -5 ? 'down' : 'stable'
    return { pct: Math.round(Math.abs(pct)), direction, showPct: isSingleJob }
  })

  function formatDateShort(d) {
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
</script>

{#if dataPoints.length >= 2}
  <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6">
    <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
      <h3 class="text-sm font-semibold text-text">Backup Size Trend</h3>
      <div class="flex items-center gap-3 flex-wrap">
        <!-- Legend: only show categories present in current data. -->
        {#each activeCategories as cat (cat.key)}
          <div class="flex items-center gap-1.5 text-xs text-text-dim">
            <span class="inline-block w-2.5 h-2.5 rounded-sm" style="background: {cat.color}"></span>
            <span>{cat.label}</span>
          </div>
        {/each}
        {#if trend}
          <div class="flex items-center gap-1.5 text-xs pl-3 border-l border-border">
            {#if trend.direction === 'up'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"/></svg>
              <span class="text-warning">{trend.showPct ? `+${trend.pct}%` : 'Growing'}</span>
            {:else if trend.direction === 'down'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
              <span class="text-success">{trend.showPct ? `-${trend.pct}%` : 'Shrinking'}</span>
            {:else}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14"/></svg>
              <span class="text-text-muted">Stable</span>
            {/if}
          </div>
        {/if}
      </div>
    </div>

    <div class="relative"
      onmouseleave={() => hoveredIndex = -1}
      role="img" aria-label="Backup size trend chart">
      <svg aria-hidden="true" viewBox="0 0 {width} {height}" class="w-full h-auto" preserveAspectRatio="xMidYMid meet">
        <g transform="translate({padding.left},{padding.top})">
          <!-- Horizontal grid lines -->
          {#each [0, 0.25, 0.5, 0.75, 1] as frac (frac)}
            <line x1="0" y1={chartHeight * (1 - frac)} x2={chartWidth} y2={chartHeight * (1 - frac)}
              stroke="var(--color-border)" stroke-width="0.5" stroke-dasharray="4 4" />
          {/each}

          <!-- One vertical bar per run, coloured by its job's dominant
               backup-target category. Each bar is the entire run size; we
               don't subdivide the bar because we don't store per-item-type
               sizes on a JobRun (size_bytes is a total). The legend tells
               the user what each colour means and the tooltip names the
               specific job. -->
          {#each dataPoints as p, i (i)}
            <rect
              x={xCenter(i) - barWidth / 2}
              y={y(p.size)}
              width={barWidth}
              height={chartHeight - y(p.size)}
              fill={colorFor(p.category)}
              opacity={hoveredIndex === i ? 1 : 0.85}
              class="transition-opacity duration-150 cursor-pointer"
              rx="1.5"
              role="img"
              aria-label="{p.name}: {formatBytes(p.size)}"
              onmouseenter={() => hoveredIndex = i}
            />
          {/each}

          <!-- X axis labels (first and last) -->
          <text x="0" y={chartHeight + 14} fill="var(--color-text-dim)" font-size="10" text-anchor="start">
            {formatDateShort(dataPoints[0].date)}
          </text>
          <text x={chartWidth} y={chartHeight + 14} fill="var(--color-text-dim)" font-size="10" text-anchor="end">
            {formatDateShort(dataPoints[dataPoints.length - 1].date)}
          </text>
        </g>
      </svg>

      <!-- Tooltip -->
      {#if hoveredIndex >= 0 && hoveredIndex < dataPoints.length}
        {@const p = dataPoints[hoveredIndex]}
        {@const tooltipX = padding.left + xCenter(hoveredIndex)}
        <div class="absolute pointer-events-none bg-surface-3 border border-border rounded-lg shadow-lg p-2 text-xs -translate-x-1/2 -translate-y-full"
          style="left: {(tooltipX / width) * 100}%; top: {((padding.top + y(p.size)) / height) * 100 - 5}%">
          <div class="flex items-center gap-1.5 mb-0.5">
            <span class="inline-block w-2 h-2 rounded-sm" style="background: {colorFor(p.category)}"></span>
            <span class="text-text-muted">{labelFor(p.category)}</span>
          </div>
          <p class="font-semibold text-text">{formatBytes(p.size)}</p>
          <p class="text-text-dim">{formatDateShort(p.date)}</p>
          {#if p.name}
            <p class="text-text-muted truncate max-w-[160px]">{p.name}</p>
          {/if}
        </div>
      {/if}
    </div>

    <div class="flex items-center justify-between mt-2 text-xs text-text-dim">
      <span>{dataPoints.length} data points</span>
      <span>Latest: {formatBytes(dataPoints[dataPoints.length - 1].size)}</span>
    </div>
  </div>
{/if}
