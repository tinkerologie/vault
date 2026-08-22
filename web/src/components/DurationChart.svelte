<script>
  import { formatDuration, formatInt } from '../lib/utils.js'

  let { buckets = [], bucket = 'day' } = $props()

  // Each chart point is a server-bucketed time window carrying the average
  // run duration (seconds) for that window. Zero-duration buckets are dropped.
  let dataPoints = $derived(
    (buckets || [])
      .map(b => ({
        date: new Date(b.start),
        duration: b.avg_duration_seconds || 0,
        runCount: b.run_count || 0,
      }))
      .filter(p => p.duration > 0)
  )

  let hoveredIndex = $state(-1)

  const width = 600
  const height = 140
  const padding = { top: 10, right: 10, bottom: 22, left: 54 }
  const chartWidth = width - padding.left - padding.right
  const chartHeight = height - padding.top - padding.bottom

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

  // Index of the bucket nearest the middle of the range — used for the
  // centre x-axis date label.
  let midIndex = $derived(Math.floor((dataPoints.length - 1) / 2))

  // "Nice" step sizes (seconds) for the y-axis, ascending: the first step that
  // splits the range into at most ~5 intervals wins, so tick labels stay round
  // ("15m", "1h") instead of arbitrary fractions.
  const DURATION_STEPS = [
    1, 2, 5, 10, 15, 30,
    60, 120, 300, 600, 900, 1800,
    3600, 7200, 10800, 21600, 43200, 86400,
  ]

  let yMax = $derived(Math.max(...dataPoints.map(p => p.duration), 1))

  // Round the top of the axis up to a whole number of steps and generate the
  // tick values — grid lines and labels are then staggered at those round
  // values derived from the data range instead of fixed 25% fractions.
  let yScale = $derived.by(() => {
    let step = DURATION_STEPS[DURATION_STEPS.length - 1]
    for (const s of DURATION_STEPS) {
      if (yMax / s <= 5) { step = s; break }
    }
    const ceil = Math.ceil(yMax / step) * step
    const ticks = []
    for (let v = 0; v <= ceil + 1e-9; v += step) ticks.push(v)
    return { step, ceil, ticks }
  })

  function y(duration) {
    return chartHeight - (duration / yScale.ceil) * chartHeight
  }

  // Compact duration for y-axis tick labels: drop zero components so "600s"
  // reads "10m" rather than "10m 0s".
  function formatAxisDuration(seconds) {
    if (seconds <= 0) return '0'
    const s = Math.round(seconds)
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    const sec = s % 60
    if (h > 0) return m === 0 ? `${h}h` : `${h}h ${m}m`
    if (m === 0) return `${sec}s`
    if (sec === 0) return `${m}m`
    return `${m}m ${sec}s`
  }

  let trend = $derived.by(() => {
    if (dataPoints.length < 2) return null
    const n = dataPoints.length
    const sumX = (n * (n - 1)) / 2
    const sumX2 = (n * (n - 1) * (2 * n - 1)) / 6
    const sumY = dataPoints.reduce((s, p) => s + p.duration, 0)
    const sumXY = dataPoints.reduce((s, p, i) => s + i * p.duration, 0)
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
</script>

{#if dataPoints.length >= 2}
  <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6">
    <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
      <h3 class="text-sm font-semibold text-text">Job Duration Trend</h3>
      <div class="flex items-center gap-3 flex-wrap">
        {#if trend}
          <div class="flex items-center gap-1.5 text-xs pl-3 border-l border-border">
            {#if trend.direction === 'up'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"/></svg>
              <span class="text-warning">+{formatInt(trend.pct)}% slower</span>
            {:else if trend.direction === 'down'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
              <span class="text-success">-{formatInt(trend.pct)}% faster</span>
            {:else}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14"/></svg>
              <span class="text-text-muted">Stable</span>
            {/if}
          </div>
        {/if}
      </div>
    </div>

    <div class="relative"
      onmouseleave={() => hoveredIndex = -1}>
      <svg aria-hidden="true" viewBox="0 0 {width} {height}" class="w-full h-auto" preserveAspectRatio="xMidYMid meet">
        <g transform="translate({padding.left},{padding.top})">
          <!-- Horizontal grid lines + y-axis value labels, staggered at round
               intervals derived from the data range. -->
          {#each yScale.ticks as tick (tick)}
            <line x1="0" y1={y(tick)} x2={chartWidth} y2={y(tick)}
              stroke="var(--color-border)" stroke-width="0.5" stroke-dasharray="4 4" />
            <text x="-6" y={y(tick) + 2} fill="var(--color-text-dim)" font-size="6" text-anchor="end">
              {formatAxisDuration(tick)}
            </text>
          {/each}

          <!-- One bar per bucket; height is proportional to the bucket's
               average run duration. -->
          {#each dataPoints as p, i (i)}
            <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
            <g
              role="img"
              aria-label="{formatDateShort(p.date)}: {formatDuration(p.duration)}"
              tabindex="0"
              opacity={hoveredIndex === i ? 1 : 0.85}
              class="transition-opacity duration-150 cursor-pointer"
              onmouseenter={() => hoveredIndex = i}
              onfocus={() => hoveredIndex = i}
              onblur={() => hoveredIndex = -1}
            >
              <!-- Invisible full-height hit area so hovering anywhere along
                   the bar's column works. -->
              <rect x={xCenter(i) - barWidth / 2} y="0" width={barWidth} height={chartHeight} fill="transparent" />
              <rect
                x={xCenter(i) - barWidth / 2}
                y={y(p.duration)}
                width={barWidth}
                height={chartHeight - y(p.duration)}
                fill="var(--color-vault, #8b5cf6)"
                rx="1.5"
              />
            </g>
          {/each}

          <!-- X axis labels (beginning, middle, end), centered on their bars -->
          <text x={xCenter(0)} y={chartHeight + 14} fill="var(--color-text-dim)" font-size="5" text-anchor="middle">
            {formatDateShort(dataPoints[0].date)}
          </text>
          {#if dataPoints.length > 2}
            <text x={xCenter(midIndex)} y={chartHeight + 14} fill="var(--color-text-dim)" font-size="5" text-anchor="middle">
              {formatDateShort(dataPoints[midIndex].date)}
            </text>
          {/if}
          <text x={xCenter(dataPoints.length - 1)} y={chartHeight + 14} fill="var(--color-text-dim)" font-size="5" text-anchor="middle">
            {formatDateShort(dataPoints[dataPoints.length - 1].date)}
          </text>
        </g>
      </svg>

      <!-- Tooltip -->
      {#if hoveredIndex >= 0 && hoveredIndex < dataPoints.length}
        {@const p = dataPoints[hoveredIndex]}
        {@const tooltipX = padding.left + xCenter(hoveredIndex)}
        <div class="absolute pointer-events-none bg-surface-3 border border-border rounded-lg shadow-lg p-2 text-xs -translate-x-1/2 -translate-y-full"
          style="left: {(tooltipX / width) * 100}%; top: {((padding.top + y(p.duration)) / height) * 100 - 5}%">
          <p class="text-text-dim mb-1">{formatDateShort(p.date)}</p>
          <p class="font-semibold text-text">{formatDuration(p.duration)}</p>
          {#if p.runCount}
            <p class="text-text-muted mt-0.5">{p.runCount} run{p.runCount !== 1 ? 's' : ''}</p>
          {/if}
        </div>
      {/if}
    </div>

    <table class="sr-only">
      <caption>Job duration trend by date</caption>
      <thead>
        <tr>
          <th scope="col">Date</th>
          <th scope="col">Average duration</th>
          <th scope="col">Runs</th>
        </tr>
      </thead>
      <tbody>
        {#each dataPoints as p, i (i)}
          <tr>
            <td>{formatDateShort(p.date)}</td>
            <td>{formatDuration(p.duration)}</td>
            <td>{p.runCount}</td>
          </tr>
        {/each}
      </tbody>
    </table>

    <div class="flex items-center justify-between mt-2 text-xs text-text-dim">
      <span>{dataPoints.length} data points</span>
      <span>Latest: {formatDuration(dataPoints[dataPoints.length - 1].duration)}</span>
    </div>
  </div>
{/if}
