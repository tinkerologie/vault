<script>
  import { formatBytes } from '../lib/utils.js'

  // samples: [{ sampled_at, free_bytes, total_bytes }] from the
  // /destinations/{id}/capacity-trajectory endpoint (last 90 days).
  let { samples = [] } = $props()

  const DAY_MS = 86_400_000

  // Normalise + sort samples into plottable points. used = total - free.
  let points = $derived.by(() => {
    return (samples || [])
      .map(s => {
        const total = s.total_bytes || 0
        const free = s.free_bytes || 0
        const used = Math.max(0, total - free)
        return { t: new Date(s.sampled_at).getTime(), used, total }
      })
      .filter(p => Number.isFinite(p.t))
      .sort((a, b) => a.t - b.t)
  })

  let hasQuota = $derived(points.length > 0 && points[points.length - 1].total > 0)
  let latest = $derived(points.length > 0 ? points[points.length - 1] : null)
  let latestPct = $derived(latest && latest.total > 0 ? (latest.used / latest.total) * 100 : 0)

  // The plotted series: used % when there's a quota, otherwise raw used bytes.
  let series = $derived(points.map(p => (hasQuota && p.total > 0 ? (p.used / p.total) * 100 : p.used)))

  // SVG polyline/area paths over a 100x40 viewBox (preserveAspectRatio none).
  // For a quota the Y axis is a fixed 0..100%; without one it's relative
  // min..max so the shape is still readable.
  let paths = $derived.by(() => {
    if (points.length < 2) return null
    const tMin = points[0].t
    const tMax = points[points.length - 1].t
    const span = Math.max(1, tMax - tMin)
    let lo = 0, hi = 100
    if (!hasQuota) {
      lo = Math.min(...series)
      hi = Math.max(...series)
      if (hi === lo) hi = lo + 1
    }
    const x = t => ((t - tMin) / span) * 100
    const y = v => 38 - ((v - lo) / (hi - lo)) * 36 // 2px top/bottom padding
    const line = points.map((p, i) => `${x(p.t).toFixed(2)},${y(series[i]).toFixed(2)}`).join(' ')
    const area = `0,40 ${line} 100,40`
    return { line, area }
  })

  // Least-squares slope of used bytes over time → a linear runway estimate.
  // Honest about uncertainty: needs a few samples spanning real time.
  let runway = $derived.by(() => {
    if (points.length < 3) return { kind: 'insufficient' }
    const spanDays = (points[points.length - 1].t - points[0].t) / DAY_MS
    if (spanDays < 1) return { kind: 'insufficient' }
    const n = points.length
    let sx = 0, sy = 0, sxx = 0, sxy = 0
    for (const p of points) {
      const xd = (p.t - points[0].t) / DAY_MS // days since first sample
      sx += xd; sy += p.used; sxx += xd * xd; sxy += xd * p.used
    }
    const denom = n * sxx - sx * sx
    if (denom === 0) return { kind: 'insufficient' }
    const slope = (n * sxy - sx * sy) / denom // bytes/day
    if (!hasQuota) {
      if (slope <= 0) return { kind: 'flat' }
      return { kind: 'growing', perDay: slope }
    }
    if (slope <= 0) return { kind: 'flat' }
    const remaining = latest.total - latest.used
    const days = remaining / slope
    if (!Number.isFinite(days) || days < 0) return { kind: 'flat' }
    return { kind: 'full', days, perDay: slope }
  })

  function runwayText(r) {
    switch (r.kind) {
      case 'insufficient': return 'Not enough history yet'
      case 'flat': return 'Usage stable or shrinking'
      case 'growing': return `Growing ~${formatBytes(r.perDay)}/day`
      case 'full': {
        const d = Math.round(r.days)
        if (d > 365 * 2) return 'Years of runway at current growth'
        if (d >= 60) return `~${Math.round(d / 30)} months until full at current growth`
        return `~${d} day${d === 1 ? '' : 's'} until full at current growth`
      }
      default: return ''
    }
  }

  // Tone the trend to the same thresholds as the capacity bar.
  let tone = $derived(
    !hasQuota ? 'text-vault'
      : latestPct >= 90 ? 'text-rose-500'
      : latestPct >= 80 ? 'text-amber-500'
      : 'text-vault'
  )
  // Runway urgency colour, only meaningful for a quota nearing full.
  let runwayTone = $derived(
    runway.kind === 'full' && runway.days < 30 ? 'text-rose-500'
      : runway.kind === 'full' && runway.days < 90 ? 'text-amber-500'
      : 'text-text-dim'
  )
  let trendDays = $derived(points.length < 2 ? 0 : Math.round((points[points.length - 1].t - points[0].t) / DAY_MS))
  let trendLabel = $derived(trendDays > 0 ? `Last ${trendDays}-day trend` : 'Recent trend')
  let trendSummary = $derived(`Capacity usage trend: ${trendLabel}. ${runwayText(runway)}`)
</script>

{#if points.length < 2}
  <p class="text-text-dim italic">Not enough history yet for a trend.</p>
{:else}
  <div class="space-y-1">
    <div class="flex items-center justify-between text-text-muted">
      <span>{trendLabel}</span>
      <span class={runwayTone}>{runwayText(runway)}</span>
    </div>
    <svg viewBox="0 0 100 40" preserveAspectRatio="none" class="w-full h-10" role="img"
      aria-label={trendSummary}>
      <polygon points={paths.area} class="{tone} opacity-10" fill="currentColor" />
      <polyline points={paths.line} class={tone} fill="none" stroke="currentColor"
        stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
    </svg>
  </div>
{/if}
