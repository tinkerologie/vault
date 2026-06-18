<script>
  import { formatBytes } from '../lib/utils.js'

  let { buckets = [], bucket = 'day' } = $props()

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

  // Each chart point is a server-bucketed time window (run/day/week,
  // depending on the selected trend period). Bucketing and date-range
  // selection now happen server-side, so we just map the shape.
  let dataPoints = $derived(
    (buckets || []).map(b => ({
      date: new Date(b.start),
      size: b.total_bytes,
      categories: b.categories || {},
    }))
  )

  // Which categories actually appear in the current dataset – used to
  // render only the legend chips that matter and to control which stacked
  // bands are drawn. Categories are kept in the canonical CATEGORIES order
  // so the colour assignment is stable across re-renders.
  let activeCategories = $derived.by(() => {
    const presentKeys = dataPoints.flatMap(p => Object.keys(p.categories).filter(k => p.categories[k] > 0))
    const present = new Set(presentKeys)
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
    return { pct: Math.round(Math.abs(pct)), direction, showPct: true }
  })

  // Week buckets span 7 days, so labelling just the start date is
  // ambiguous - prefix with "Wk of" to make that explicit.
  function formatDateShort(d) {
    const formatted = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    return bucket === 'week' ? `Wk of ${formatted}` : formatted
  }

  // Build the stacked bands for one bucket: each band's height is
  // proportional to its category's byte count, stacked bottom-up in
  // canonical CATEGORIES order so colours line up across bars.
  function bandsFor(p) {
    let cumulative = 0
    const bands = []
    for (const cat of activeCategories) {
      const value = p.categories[cat.key] || 0
      if (value <= 0) continue
      const bandHeight = (value / yMax) * chartHeight
      bands.push({
        key: cat.key,
        color: cat.color,
        value,
        yTop: chartHeight - cumulative - bandHeight,
        height: bandHeight,
      })
      cumulative += bandHeight
    }
    return bands
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

          <!-- One stacked bar per bucket: each band's height is proportional
               to that category's byte total within the bucket, so the full
               bar height represents total_bytes. The legend explains each
               colour and the tooltip gives the per-category breakdown. -->
          {#each dataPoints as p, i (i)}
            <g
              role="img"
              aria-label="{formatDateShort(p.date)}: {formatBytes(p.size)}"
              opacity={hoveredIndex === i ? 1 : 0.85}
              class="transition-opacity duration-150 cursor-pointer"
              onmouseenter={() => hoveredIndex = i}
            >
              <!-- Invisible full-height hit area so hovering anywhere along
                   the bar's column (including empty space above it) works. -->
              <rect x={xCenter(i) - barWidth / 2} y="0" width={barWidth} height={chartHeight} fill="transparent" />
              {#each bandsFor(p) as band (band.key)}
                <rect
                  x={xCenter(i) - barWidth / 2}
                  y={band.yTop}
                  width={barWidth}
                  height={band.height}
                  fill={band.color}
                  rx="1.5"
                />
              {/each}
            </g>
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
          <p class="text-text-dim mb-1">{formatDateShort(p.date)}</p>
          {#each activeCategories as cat (cat.key)}
            {#if p.categories[cat.key] > 0}
              <div class="flex items-center gap-1.5">
                <span class="inline-block w-2 h-2 rounded-sm" style="background: {cat.color}"></span>
                <span class="text-text-muted">{cat.label}</span>
                <span class="text-text ml-auto pl-3">{formatBytes(p.categories[cat.key])}</span>
              </div>
            {/if}
          {/each}
          <p class="font-semibold text-text mt-1 pt-1 border-t border-border">{formatBytes(p.size)} total</p>
        </div>
      {/if}
    </div>

    <div class="flex items-center justify-between mt-2 text-xs text-text-dim">
      <span>{dataPoints.length} data points</span>
      <span>Latest: {formatBytes(dataPoints[dataPoints.length - 1].size)}</span>
    </div>
  </div>
{/if}
