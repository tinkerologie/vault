<script>
  import { formatBytes, formatInt } from '../lib/utils.js'
  import { CHART_HEIGHT_CLASS, barSlot, niceTicks, xLabelIndexes } from '../lib/chartScale.js'
  import { ICONS } from '../lib/icons.js'
  import { sizeTrendClass } from '../lib/outcome-colour.js'

  let { buckets = [], bucket = 'day' } = $props()

  // Four stable backup-target categories. We deliberately keep the palette
  // small (Veeam / Duplicati / Backblaze converge on the same idea) so the
  // chart is readable at a glance instead of an unreadable rainbow.
  const CATEGORIES = [
    { key: 'containers', label: 'Containers', color: 'var(--color-info)' },
    { key: 'vms',        label: 'VMs',        color: 'var(--color-vault)' },
    { key: 'folders',    label: 'Folders & Files', color: 'var(--color-success)' },
    { key: 'flash',      label: 'Flash',      color: 'var(--color-warning)' },
    { key: 'other',      label: 'Other',      color: 'var(--color-text-dim)' },
  ]

  // Each chart point is a server-bucketed time window (run/day/week,
  // depending on the selected trend period). Bucketing and date-range
  // selection happen server-side, so we just map the shape.
  let dataPoints = $derived(
    (buckets || []).map(b => ({
      date: new Date(b.start),
      size: b.total_bytes,
      categories: b.categories || {},
    }))
  )

  // Which categories appear in the current dataset, in canonical order so the
  // colour assignment is stable across re-renders.
  let activeCategories = $derived.by(() => {
    const present = new Set(dataPoints.flatMap(p => Object.keys(p.categories).filter(k => p.categories[k] > 0)))
    return CATEGORIES.filter(c => present.has(c.key))
  })

  let hoveredIndex = $state(-1)
  let focusedIndex = $state(-1)

  // "Nice" byte steps across the 1024-based units (B → PB): 1/2/5/10/…/500 ×
  // unit, so tick labels stay round ("200 GB") and the axis hugs the data.
  const SIZE_STEPS = (() => {
    const steps = []
    for (let exp = 0; exp <= 5; exp++) {
      const unit = 1024 ** exp
      for (const n of [1, 2, 5, 10, 20, 50, 100, 200, 500]) steps.push(n * unit)
    }
    return steps
  })()

  let yScale = $derived(niceTicks(Math.max(...dataPoints.map(p => p.size), 1), SIZE_STEPS))

  // The plot is drawn in a 100×100 box stretched to the container, so x and y
  // are percentages; the axis labels are HTML beside it, never scaled SVG text.
  function y(size) {
    return 100 - (size / yScale.ceil) * 100
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
    return { pct: Math.round(Math.abs(pct)), direction }
  })

  // Week buckets span 7 days, so labelling just the start date is ambiguous.
  function formatDateShort(d) {
    const formatted = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    return bucket === 'week' ? `Wk of ${formatted}` : formatted
  }

  // A bar's accessible name carries its whole breakdown, so a keyboard or
  // screen-reader user gets what the hover tooltip shows.
  function barLabel(p) {
    const parts = activeCategories.filter(c => p.categories[c.key] > 0).map(c => `${c.label} ${formatBytes(p.categories[c.key])}`)
    return `${formatDateShort(p.date)}: ${formatBytes(p.size)} total${parts.length ? ` (${parts.join(', ')})` : ''}`
  }

  // Stacked bands for one bucket, bottom-up in canonical order.
  function bandsFor(p) {
    let cumulative = 0
    const bands = []
    for (const cat of activeCategories) {
      const value = p.categories[cat.key] || 0
      if (value <= 0) continue
      const bandHeight = (value / yScale.ceil) * 100
      bands.push({ key: cat.key, color: cat.color, yTop: 100 - cumulative - bandHeight, height: bandHeight })
      cumulative += bandHeight
    }
    return bands
  }

  let shownIndex = $derived(focusedIndex >= 0 ? focusedIndex : hoveredIndex)
</script>

<div class="bg-surface-2 border border-border rounded-xl p-4 mb-6" data-chart="size">
  <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
    <h2 class="text-sm font-semibold text-text">Backup Size Trend</h2>
    {#if dataPoints.length >= 2}
      <div class="flex items-center gap-3 flex-wrap">
        {#each activeCategories as cat (cat.key)}
          <div class="flex items-center gap-1.5 text-xs text-text-dim">
            <span aria-hidden="true" class="inline-block w-2.5 h-2.5 rounded-sm" style="background: {cat.color}"></span>
            <span>{cat.label}</span>
          </div>
        {/each}
        {#if trend}
          <div class="flex items-center gap-1.5 text-xs pl-3 border-l border-border tabular-nums">
            {#if trend.direction === 'up'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 {sizeTrendClass(trend.direction)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronUp}/></svg>
              <span class={sizeTrendClass(trend.direction)}>+{formatInt(trend.pct)}%</span>
            {:else if trend.direction === 'down'}
              <svg aria-hidden="true" class="w-3.5 h-3.5 {sizeTrendClass(trend.direction)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronDown}/></svg>
              <span class={sizeTrendClass(trend.direction)}>-{formatInt(trend.pct)}%</span>
            {:else}
              <svg aria-hidden="true" class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.minus}/></svg>
              <span class="text-text-muted">Stable</span>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>

  {#if dataPoints.length < 2}
    <p class="text-sm text-text-muted py-8 text-center" data-chart-empty>Not enough data for this period. Pick a longer period, or check back after more backups run.</p>
  {:else}
    <div class="flex gap-2">
      <!-- Y axis: HTML text, so it stays readable at any chart width. -->
      <div aria-hidden="true" class="relative w-14 shrink-0 {CHART_HEIGHT_CLASS}">
        {#each yScale.ticks as tick (tick)}
          <span data-axis-label class="absolute right-0 -translate-y-1/2 text-xs leading-none text-text-dim tabular-nums whitespace-nowrap" style="top: {y(tick)}%">{formatBytes(tick)}</span>
        {/each}
      </div>
      <div class="relative flex-1 min-w-0">
        <div class="relative {CHART_HEIGHT_CLASS}" role="presentation" onmouseleave={() => hoveredIndex = -1}>
          <svg role="group" aria-label="Backup size by date" viewBox="0 0 100 100" preserveAspectRatio="none" class="absolute inset-0 w-full h-full overflow-visible">
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
                aria-label={barLabel(p)}
                tabindex="0"
                opacity={shownIndex === i ? 1 : 0.85}
                class="cursor-pointer"
                onmouseenter={() => hoveredIndex = i}
                onfocus={() => focusedIndex = i}
                onblur={() => focusedIndex = -1}
              >
                <rect x={slot.x} y="0" width={slot.width} height="100" fill="transparent" />
                {#each bandsFor(p) as band (band.key)}
                  <rect x={slot.x} y={band.yTop} width={slot.width} height={band.height} fill={band.color} />
                {/each}
                {#if focusedIndex === i}
                  <rect x={slot.x} y="0" width={slot.width} height="100" fill="none" stroke="var(--color-text)" stroke-width="2" vector-effect="non-scaling-stroke" />
                {/if}
              </g>
            {/each}
          </svg>

          {#if shownIndex >= 0 && shownIndex < dataPoints.length}
            {@const p = dataPoints[shownIndex]}
            {@const slot = barSlot(shownIndex, dataPoints.length)}
            <div data-chart-tooltip class="absolute z-10 pointer-events-none bg-surface-3 border border-border rounded-lg shadow p-2 text-xs -translate-x-1/2 -translate-y-full tabular-nums"
              style="left: {slot.center}%; top: {Math.max(0, y(p.size) - 4)}%">
              <p class="text-text-muted mb-1">{formatDateShort(p.date)}</p>
              {#each activeCategories as cat (cat.key)}
                {#if p.categories[cat.key] > 0}
                  <div class="flex items-center gap-1.5">
                    <span aria-hidden="true" class="inline-block w-2 h-2 rounded-sm" style="background: {cat.color}"></span>
                    <span class="text-text-muted">{cat.label}</span>
                    <span class="text-text ml-auto pl-3">{formatBytes(p.categories[cat.key])}</span>
                  </div>
                {/if}
              {/each}
              <p class="font-semibold text-text mt-1 pt-1 border-t border-border">{formatBytes(p.size)} total</p>
            </div>
          {/if}
        </div>
        <!-- X axis: first, middle and last bucket. -->
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
      <caption>Backup size by date</caption>
      <thead>
        <tr>
          <th scope="col">Date</th>
          <th scope="col">Total</th>
          {#each activeCategories as cat (cat.key)}<th scope="col">{cat.label}</th>{/each}
        </tr>
      </thead>
      <tbody>
        {#each dataPoints as p, i (i)}
          <tr>
            <td>{formatDateShort(p.date)}</td>
            <td>{formatBytes(p.size)}</td>
            {#each activeCategories as cat (cat.key)}<td>{formatBytes(p.categories[cat.key] || 0)}</td>{/each}
          </tr>
        {/each}
      </tbody>
    </table>

    <div class="flex items-center justify-between mt-2 text-xs text-text-dim tabular-nums">
      <span>{formatInt(dataPoints.length)} data points</span>
      <span>Latest: {formatBytes(dataPoints[dataPoints.length - 1].size)}</span>
    </div>
  {/if}
</div>
