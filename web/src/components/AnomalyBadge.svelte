<script>
  /** Pure-presentation anomaly count pill.
   *  Renders nothing when count === 0. */

  /** @type {{ count: number, severity: string }} */
  let { count = 0, severity = 'info' } = $props()

  const colorMap = {
    critical: 'bg-danger/15 text-danger border-danger/30',
    warning:  'bg-warning/15 text-warning border-warning/30',
    info:     'bg-info/15 text-info border-info/30',
  }

  let colorClass = $derived(colorMap[severity] ?? colorMap.info)
</script>

{#if count > 0}
  <span
    class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border text-[11px] font-semibold {colorClass}"
    aria-label="{count} {severity} {count === 1 ? 'anomaly' : 'anomalies'}"
  >
    <svg aria-hidden="true" class="w-3 h-3 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
    </svg>
    {count}
  </span>
{/if}
