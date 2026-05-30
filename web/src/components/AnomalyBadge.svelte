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
    <svg aria-hidden="true" class="w-2.5 h-2.5 shrink-0" fill="currentColor" viewBox="0 0 8 8">
      <circle cx="4" cy="4" r="4" />
    </svg>
    {count}
  </span>
{/if}
