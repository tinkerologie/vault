<script>
  import { formatDuration, formatInt } from '../lib/utils.js'
  import { CHART_HEIGHT_CLASS, barSlot, niceTicks, xLabelIndexes } from '../lib/chartScale.js'
  import { ICONS } from '../lib/icons.js'

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
  let focusedIndex = $state(-1)

  // "Nice" step sizes (seconds), so tick labels stay round ("15m", "1h").
  const DURATION_STEPS = [
    1, 2, 5, 10, 15, 30,
    60, 120, 300, 600, 900, 1800,
    3600, 7200, 10800, 21600, 43200, 86400,
  ]

  let yScale = $derived(niceTicks(Math.max(...dataPoints.map(p => p.duration), 1), DURATION_STEPS))

  // Percent of the plot height; see chartScale.js.
  function y(duration) {
    return 100 - (duration / yScale.ceil) * 100
  }

  // Compact duration for y-axis tick labels: "10m" rather than "10m 0s".
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
    return { pct: Math.round(Math.abs(pct)), direction }
  })

  // Week buckets span 7 days, so labelling just the start date is ambiguous.
  function formatDateShort(d) {
    const formatted = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    return bucket === 'week' ? `Wk of ${formatted}` : formatted
  }

  function runs(count) {
    return `${formatInt(count)} run${count !== 1 ? 's' : ''}`
  }

  let shownIndex = $derived(focusedIndex >= 0 ? focusedIndex : hoveredIndex)
</script>

<div class="bg-surface-2 border border-border rounded-xl p-4 mb-6" data-chart="duration">
  <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
    <h2 class="text-sm font-semibold text-text">Job Duration Trend</h2>
    {#if trend}
      <div class="flex items-center gap-1.5 text-xs pl-3 border-l border-border tabular-nums">
        {#if trend.direction === 'up'}
          <svg aria-hidden="true" class="w-3.5 h-3.5 text-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronUp}/></svg>
          <span class="text-warning">+{formatInt(trend.pct)}% slower</span>
        {:else if trend.direction === 'down'}
          <svg aria-hidden="true" class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronDown}/></svg>
          <span class="text-success">-{formatInt(trend.pct)}% faster</span>
        {:else}
          <svg aria-hidden="true" class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.minus}/></svg>
          <span class="text-text-muted">Stable</span>
        {/if}
      </div>
    {/if}
  </div>

  {#if dataPoints.length < 2}
    <p class="text-sm text-text-muted py-8 text-center" data-chart-empty>Not enough data for this period. Pick a longer period, or check back after more backups run.</p>
  {:else}
    <div class="flex gap-2">
      <div aria-hidden="true" class="relative w-14 shrink-0 {CHART_HEIGHT_CLASS}">
        {#each yScale.ticks as tick (tick)}
          <span data-axis-label class="absolute right-0 -translate-y-1/2 text-xs leading-none text-text-dim tabular-nums whitespace-nowrap" style="top: {y(tick)}%">{formatAxisDuration(tick)}</span>
        {/each}
      </div>
      <div class="relative flex-1 min-w-0">
        <div class="relative {CHART_HEIGHT_CLASS}" role="presentation" onmouseleave={() => hoveredIndex = -1}>
          <svg role="group" aria-label="Average job duration by date" viewBox="0 0 100 100" preserveAspectRatio="none" class="absolute inset-0 w-full h-full overflow-visible">
            <g aria-hidden="true">
              {#each yScale.ticks as tick (tick)}
                <line x1="0" y1={y(tick)} x2="100" y2={y(tick)} stroke="var(--color-border)" stroke-width="1" stroke-dasharray="4 4" vector-effect="non-scaling-stroke" />
              {/each}
            </g>
            {#each dataPoints as p, i (i)}
              {@const slot = barSlot(i, dataPoints.length)}
              <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
              <g
                role="img"
                aria-label="{formatDateShort(p.date)}: {formatDuration(p.duration)} average, {runs(p.runCount)}"
                tabindex="0"
                opacity={shownIndex === i ? 1 : 0.85}
                class="cursor-pointer"
                onmouseenter={() => hoveredIndex = i}
                onfocus={() => focusedIndex = i}
                onblur={() => focusedIndex = -1}
              >
                <rect x={slot.x} y="0" width={slot.width} height="100" fill="transparent" />
                <rect x={slot.x} y={y(p.duration)} width={slot.width} height={100 - y(p.duration)} fill="var(--color-vault)" />
                {#if focusedIndex === i}
                  <rect x={slot.x} y="0" width={slot.width} height="100" fill="none" stroke="var(--color-text)" stroke-width="2" vector-effect="non-scaling-stroke" />
                {/if}
              </g>
            {/each}
          </svg>

          {#if shownIndex >= 0 && shownIndex < dataPoints.length}
            {@const p = dataPoints[shownIndex]}
            {@const slot = barSlot(shownIndex, dataPoints.length)}
            <div data-chart-tooltip class="absolute z-10 pointer-events-none bg-surface-3 border border-border rounded-lg shadow-lg p-2 text-xs -translate-x-1/2 -translate-y-full tabular-nums"
              style="left: {slot.center}%; top: {Math.max(0, y(p.duration) - 4)}%">
              <p class="text-text-muted mb-1">{formatDateShort(p.date)}</p>
              <p class="font-semibold text-text">{formatDuration(p.duration)}</p>
              {#if p.runCount}<p class="text-text-muted mt-0.5">{runs(p.runCount)}</p>{/if}
            </div>
          {/if}
        </div>
        <div aria-hidden="true" class="relative h-6 mt-1">
          {#each xLabelIndexes(dataPoints.length) as i (i)}
            {@const slot = barSlot(i, dataPoints.length)}
            <span data-axis-label class="absolute top-1 text-xs leading-none text-text-dim whitespace-nowrap {i === 0 ? '' : i === dataPoints.length - 1 ? '-translate-x-full' : '-translate-x-1/2'}"
              style="left: {i === 0 ? slot.x : i === dataPoints.length - 1 ? slot.x + slot.width : slot.center}%">{formatDateShort(dataPoints[i].date)}</span>
          {/each}
        </div>
      </div>
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
            <td>{formatInt(p.runCount)}</td>
          </tr>
        {/each}
      </tbody>
    </table>

    <div class="flex items-center justify-between mt-2 text-xs text-text-dim tabular-nums">
      <span>{formatInt(dataPoints.length)} data points</span>
      <span>Latest: {formatDuration(dataPoints[dataPoints.length - 1].duration)}</span>
    </div>
  {/if}
</div>
