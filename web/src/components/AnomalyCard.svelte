<script>
  import { onMount } from 'svelte'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { getAnomalies, setOpenList } from '../lib/anomalies.svelte.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import AnomalyBadge from './AnomalyBadge.svelte'
  import { prettyAnomalySummary } from '../lib/utils.js'

  const anomalies = getAnomalies()

  let loading = $state(true)
  let ackingId = $state(null)
  /** @type {{ message: string, type: string } | null} */
  let inlineError = $state(null)

  /** Top 5 critical open anomalies + top 3 warning open anomalies shown in card */
  let topCritical = $derived(
    anomalies.openList
      .filter(a => a.severity === 'critical')
      .slice(0, 5)
  )
  let topWarning = $derived(
    anomalies.openList
      .filter(a => a.severity === 'warning')
      .slice(0, 3)
  )
  let displayed = $derived([...topCritical, ...topWarning])

  let totalOpen = $derived(anomalies.openList.length)

  let worstSeverity = $derived.by(() => {
    for (const sev of ['critical', 'warning', 'info']) {
      if (anomalies.openList.some(a => a.severity === sev)) return sev
    }
    return 'info'
  })

  onMount(() => {
    loadOpen()
    // Re-fetch when bulk_resolved marks state stale (full list may have changed).
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'anomaly.bulk_resolved') loadOpen()
    })
    return unsub
  })

  async function loadOpen() {
    loading = true
    try {
      const res = await api.listAnomalies({ state: 'open', limit: 100 })
      setOpenList(res?.anomalies ?? [])
    } catch {
      // Non-fatal – card just shows empty state; we don't surface the error
      // prominently since the main content is still useful.
    } finally {
      loading = false
    }
  }

  async function ack(anomaly, action) {
    ackingId = anomaly.id
    inlineError = null
    try {
      await api.ackAnomaly(anomaly.id, action, '', 'user')
      // Optimistically remove from the open list
      setOpenList(anomalies.openList.filter(a => a.id !== anomaly.id))
    } catch (e) {
      inlineError = { message: e.message || 'Failed to acknowledge', type: 'error' }
      setTimeout(() => { inlineError = null }, 4000)
    } finally {
      ackingId = null
    }
  }

  function scopeLabel(a) {
    if (a.scope_kind === 'job') return `Job #${a.scope_id}`
    if (a.scope_kind === 'destination') return `Dest #${a.scope_id}`
    return a.scope_kind || '–'
  }

  const severityBg = { critical: 'bg-danger/10 border-l-2 border-danger', warning: 'bg-warning/10 border-l-2 border-warning', info: 'bg-info/10 border-l-2 border-info' }
</script>

<div class="bg-surface-2 border border-border rounded-xl mb-8">
  <!-- Header -->
  <div class="px-5 py-4 border-b border-border flex items-center justify-between">
    <div class="flex items-center gap-3">
      <svg aria-hidden="true" class="w-4 h-4 text-text-muted shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
      </svg>
      <h2 class="text-base font-semibold text-text">Anomalies</h2>
      {#if totalOpen > 0}
        <AnomalyBadge count={totalOpen} severity={worstSeverity} />
      {/if}
    </div>
    <button
      onclick={() => navigate('/anomalies')}
      class="text-xs text-vault hover:text-vault-dark transition-colors font-medium"
    >
      View all
    </button>
  </div>

  <!-- Body -->
  <div class="divide-y divide-border">
    {#if loading}
      <div class="px-5 py-6 text-center text-sm text-text-muted">Loading...</div>

    {:else}
      {#if inlineError}
        <div class="px-5 py-2 text-xs text-danger bg-danger/5 border-b border-danger/20">{inlineError.message}</div>
      {/if}

      {#if displayed.length === 0}
        <!-- Empty / all-clear state -->
        <div class="px-5 py-8 text-center">
          <div class="w-10 h-10 rounded-full bg-success/10 flex items-center justify-center mx-auto mb-3">
            <svg aria-hidden="true" class="w-5 h-5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
            </svg>
          </div>
          <p class="text-sm font-medium text-text">All clear</p>
          <p class="text-xs text-text-dim mt-1">No open anomalies detected</p>
        </div>

      {:else}
        {#each displayed as anomaly (anomaly.id)}
          <div class="px-5 py-3 {severityBg[anomaly.severity] ?? ''}">
            <div class="flex items-start gap-3">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <AnomalyBadge count={1} severity={anomaly.severity} />
                  <span class="text-xs text-text-dim">{scopeLabel(anomaly)}</span>
                  <span class="text-xs text-text-dim">&middot; {anomaly.detector}</span>
                </div>
                <p class="text-sm text-text mt-1 leading-snug">{prettyAnomalySummary(anomaly.summary)}</p>
              </div>
              <!-- Ack actions -->
              <div class="flex items-center gap-1 shrink-0">
                <button
                  onclick={() => ack(anomaly, 'dismiss')}
                  disabled={ackingId === anomaly.id}
                  class="text-xs px-2 py-1 rounded-lg bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text transition-colors disabled:opacity-40"
                  title="Dismiss anomaly"
                >
                  Dismiss
                </button>
                <button
                  onclick={() => ack(anomaly, 'mark_expected')}
                  disabled={ackingId === anomaly.id}
                  class="text-xs px-2 py-1 rounded-lg bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text transition-colors disabled:opacity-40"
                  title="Mark as expected"
                >
                  Expected
                </button>
              </div>
            </div>
          </div>
        {/each}

        {#if totalOpen > displayed.length}
          <div class="px-5 py-3 text-center">
            <button
              onclick={() => navigate('/anomalies')}
              class="text-xs text-vault hover:text-vault-dark transition-colors font-medium"
            >
              + {totalOpen - displayed.length} more – view all anomalies
            </button>
          </div>
        {/if}
      {/if}
    {/if}
  </div>
</div>
