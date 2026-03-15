<script>
  import { statusBadge, relTime, formatBytes, formatSpeed, formatDurationFromDates, getFailureReason } from '../lib/utils.js'

  let { runs = [], maxItems = 8 } = $props()

  // Group runs by date
  let grouped = $derived.by(() => {
    const groups = {}
    const today = new Date().toDateString()
    const yesterday = new Date(Date.now() - 86400000).toDateString()

    for (const run of runs.slice(0, maxItems)) {
      const d = new Date(run.started_at).toDateString()
      const label = d === today ? 'Today' : d === yesterday ? 'Yesterday' : new Date(run.started_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
      if (!groups[label]) groups[label] = []
      groups[label].push(run)
    }
    return Object.entries(groups)
  })

  function durationStr(run) {
    const d = formatDurationFromDates(run.started_at, run.completed_at)
    return d === '—' ? '' : d
  }

  function activityDotClass(run) {
    const status = run.status?.toLowerCase()
    if (status === 'success' || status === 'completed') return 'bg-success'
    if (status === 'running') return 'bg-info animate-pulse'
    if (status === 'partial') return 'bg-warning'
    return 'bg-danger'
  }

  function activityStatusLabel(run) {
    if (run.run_type !== 'restore') return run.status

    const status = run.status?.toLowerCase()
    if (status === 'success' || status === 'completed') return 'restored'
    if (status === 'running') return 'running'
    return run.status
  }

  function activityStatusClass(run) {
    if (run.run_type !== 'restore') return statusBadge(run.status)

    const status = run.status?.toLowerCase()
    if (status === 'success' || status === 'completed') return 'badge badge-success'
    if (status === 'running') return 'badge badge-info'
    if (status === 'partial') return 'badge badge-warning'
    return statusBadge(run.status)
  }
</script>

<div class="bg-surface-2 border border-border rounded-xl">
  <div class="px-5 py-4 border-b border-border">
    <h2 class="text-base font-semibold text-text">Recent Activity</h2>
  </div>
  {#if runs.length === 0}
    <div class="px-5 py-8 text-center text-sm text-text-muted">No recent activity</div>
  {:else}
    <div class="divide-y divide-border">
      {#each grouped as [label, groupRuns] (label)}
        <div>
          <div class="px-5 py-2 bg-surface-3/50">
            <span class="text-xs font-medium text-text-dim uppercase tracking-wide">{label}</span>
          </div>
          {#each groupRuns as run (run.id)}
            <div class="px-5 py-3 flex items-start gap-3">
              <!-- Timeline dot -->
              <div class="mt-1.5 shrink-0">
                <div class="w-2.5 h-2.5 rounded-full {activityDotClass(run)}"></div>
              </div>
              <!-- Content -->
              <div class="flex-1 min-w-0">
                <div class="flex items-center justify-between gap-2">
                  <div class="flex items-center gap-2 min-w-0">
                    {#if run.run_type === 'restore'}
                      <svg class="w-3.5 h-3.5 text-info shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6"/></svg>
                    {/if}
                    <span class="text-sm font-medium text-text truncate">{run.jobName || 'Job'}</span>
                    <span class="text-xs px-2 py-0.5 rounded-full font-medium shrink-0 {activityStatusClass(run)}">{activityStatusLabel(run)}</span>
                  </div>
                  <span class="text-xs text-text-dim shrink-0">{relTime(run.started_at)}</span>
                </div>
                <div class="flex items-center gap-3 mt-1 text-xs text-text-dim">
                  {#if durationStr(run)}
                    <span>{durationStr(run)}</span>
                  {/if}
                  {#if run.size_bytes}
                    <span>{formatBytes(run.size_bytes)}</span>
                  {/if}
                  {#if run.duration_seconds && run.size_bytes}
                    <span>{formatSpeed(run.size_bytes, run.duration_seconds)}</span>
                  {/if}
                  {#if run.items_total}
                    <span>{run.items_done || 0}/{run.items_total} items</span>
                  {/if}
                </div>
                {#if getFailureReason(run)}
                  <p class="text-xs text-danger mt-1 truncate">{getFailureReason(run)}</p>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/each}
    </div>
  {/if}
</div>
