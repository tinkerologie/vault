<script>
  import { formatBytes } from '../lib/utils.js'

  let { runs = [] } = $props()

  // Extract size data points from runs, ordered by date ascending
  let dataPoints = $derived.by(() => {
    const points = runs
      .filter(r => r.size_bytes > 0 && r.started_at)
      .map(r => ({ date: new Date(r.started_at), size: r.size_bytes, name: r.jobName }))
      .sort((a, b) => a.date - b.date)
      .slice(-30) // Last 30 data points
    return points
  })

  let hoveredIndex = $state(-1)

  // Chart dimensions
  const width = 600
  const height = 120
  const padding = { top: 10, right: 10, bottom: 20, left: 10 }

  let chartWidth = width - padding.left - padding.right
  let chartHeight = height - padding.top - padding.bottom

  // Scale helpers
  let yMax = $derived(Math.max(...dataPoints.map(p => p.size), 1))

  function x(i) {
    if (dataPoints.length <= 1) return chartWidth / 2
    return (i / (dataPoints.length - 1)) * chartWidth
  }

  function y(size) {
    return chartHeight - (size / yMax) * chartHeight
  }

  // Build SVG path
  let linePath = $derived.by(() => {
    if (dataPoints.length < 2) return ''
    return dataPoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i)},${y(p.size)}`).join(' ')
  })

  // Fill area path
  let areaPath = $derived.by(() => {
    if (dataPoints.length < 2) return ''
    const lineSegments = dataPoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i)},${y(p.size)}`).join(' ')
    return `${lineSegments} L${x(dataPoints.length - 1)},${chartHeight} L${x(0)},${chartHeight} Z`
  })

  // Trend indicator
  let trend = $derived.by(() => {
    if (dataPoints.length < 2) return null
    const recent = dataPoints.slice(-5)
    const first = recent[0]?.size || 0
    const last = recent[recent.length - 1]?.size || 0
    if (first === 0) return null
    const pct = ((last - first) / first) * 100
    return { pct: Math.round(pct), direction: pct > 5 ? 'up' : pct < -5 ? 'down' : 'stable' }
  })

  function formatDateShort(d) {
    return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
  }
</script>

{#if dataPoints.length >= 2}
  <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6">
    <div class="flex items-center justify-between mb-3">
      <h3 class="text-sm font-semibold text-text">Backup Size Trend</h3>
      {#if trend}
        <div class="flex items-center gap-1.5 text-xs">
          {#if trend.direction === 'up'}
            <svg class="w-3.5 h-3.5 text-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"/></svg>
            <span class="text-warning">+{trend.pct}%</span>
          {:else if trend.direction === 'down'}
            <svg class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
            <span class="text-success">{trend.pct}%</span>
          {:else}
            <svg class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14"/></svg>
            <span class="text-text-muted">Stable</span>
          {/if}
        </div>
      {/if}
    </div>

    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="relative"
      onmouseleave={() => hoveredIndex = -1}>
      <svg viewBox="0 0 {width} {height}" class="w-full h-auto" preserveAspectRatio="xMidYMid meet">
        <g transform="translate({padding.left},{padding.top})">
          <!-- Horizontal grid lines -->
          {#each [0, 0.25, 0.5, 0.75, 1] as frac}
            <line x1="0" y1={chartHeight * (1 - frac)} x2={chartWidth} y2={chartHeight * (1 - frac)}
              stroke="var(--color-border)" stroke-width="0.5" stroke-dasharray="4 4" />
          {/each}

          <!-- Area fill -->
          <path d={areaPath} fill="url(#sizeGradient)" opacity="0.3" />

          <!-- Line -->
          <path d={linePath} fill="none" stroke="var(--color-vault)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />

          <!-- Data points (dots) -->
          {#each dataPoints as p, i}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <circle
              cx={x(i)} cy={y(p.size)} r={hoveredIndex === i ? 5 : 3}
              fill={hoveredIndex === i ? 'var(--color-vault)' : 'var(--color-surface-2)'}
              stroke="var(--color-vault)" stroke-width="2"
              class="transition-all duration-150 cursor-pointer"
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

          <!-- Gradient definition -->
          <defs>
            <linearGradient id="sizeGradient" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stop-color="var(--color-vault)" stop-opacity="0.4" />
              <stop offset="100%" stop-color="var(--color-vault)" stop-opacity="0" />
            </linearGradient>
          </defs>
        </g>
      </svg>

      <!-- Tooltip -->
      {#if hoveredIndex >= 0 && hoveredIndex < dataPoints.length}
        {@const p = dataPoints[hoveredIndex]}
        {@const tooltipX = padding.left + x(hoveredIndex)}
        <div class="absolute pointer-events-none bg-surface-3 border border-border rounded-lg shadow-lg p-2 text-xs -translate-x-1/2 -translate-y-full"
          style="left: {(tooltipX / width) * 100}%; top: {((padding.top + y(p.size)) / height) * 100 - 5}%">
          <p class="font-semibold text-text">{formatBytes(p.size)}</p>
          <p class="text-text-dim">{formatDateShort(p.date)}</p>
          {#if p.name}
            <p class="text-text-muted truncate max-w-[120px]">{p.name}</p>
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
