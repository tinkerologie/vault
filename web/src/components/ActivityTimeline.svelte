<script>
  import { statusBadge, relTime, formatBytes, getFailureReason } from '../lib/utils.js'

  let { runs = [], maxItems = 8 } = $props()

  // Group runs by date
  let grouped = $derived.by(() => {
    const groups = {}
    const today = new Date().toDateString()
    const yesterday = new Date(Date.now() - 86400000).toDateString()

    for (const run of runs.slice(0, maxItems)) {
      const d = new Date(run.started_at).toDateString()
      const label = d === today ? 'Today' : d === yesterday ? 'Yesterday' : new Date(run.started_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
      if (!groups[label]) groups[label] = []
      groups[label].push(run)
    }
    return Object.entries(groups)
  })

  function durationStr(run) {
    if (!run.started_at || !run.completed_at) return ''
    const start = new Date(run.started_at)
    const end = new Date(run.completed_at)
    const sec = Math.round((end - start) / 1000)
    if (sec < 60) return `${sec}s`
    if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
    return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
  }
</script>

<div class="bg-surface-2 border border-border rounded-xl">
  <div class="px-5 py-4 border-b border-border">
    <h2 class="text-base font-semibold text-text">Recent Activity</h2>
  </div>
  {#if runs.length === 0}
    <div class="px-5 py-8 text-center text-sm text-text-muted">No backup runs yet</div>
  {:else}
    <div class="divide-y divide-border">
      {#each grouped as [label, groupRuns]}
        <div>
          <div class="px-5 py-2 bg-surface-3/50">
            <span class="text-xs font-medium text-text-dim uppercase tracking-wide">{label}</span>
          </div>
          {#each groupRuns as run}
            <div class="px-5 py-3 flex items-start gap-3">
              <!-- Timeline dot -->
              <div class="mt-1.5 shrink-0">
                <div class="w-2.5 h-2.5 rounded-full {run.status === 'success' || run.status === 'completed' ? 'bg-success' : run.status === 'running' ? 'bg-info animate-pulse' : 'bg-danger'}"></div>
              </div>
              <!-- Content -->
              <div class="flex-1 min-w-0">
                <div class="flex items-center justify-between gap-2">
                  <div class="flex items-center gap-2 min-w-0">
                    <span class="text-sm font-medium text-text truncate">{run.jobName || 'Job'}</span>
                    <span class="text-xs px-2 py-0.5 rounded-full font-medium shrink-0 {statusBadge(run.status)}">{run.status}</span>
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
