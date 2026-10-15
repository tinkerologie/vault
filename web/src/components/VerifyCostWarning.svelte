<script>
  /**
   * Discloses the cost of per-backup verification on a remote destination
   * (#457). Advisory only: it does not use role="alert"; the verification
   * toggle references it through aria-describedby instead.
   *
   * Props:
   * - id: string – element id for aria-describedby
   * - providerCharges: boolean – add the S3 request/retrieval/egress note
   * - toggleLocation: string (optional) – where the toggle is, for placements
   *   away from it (express form, review step)
   */
  let { id, providerCharges = false, toggleLocation = '' } = $props()
</script>

<div {id} class="mt-3 bg-warning/10 border border-warning/30 rounded-xl p-4 flex items-start gap-3">
  <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
  </svg>
  <div class="space-y-1">
    <p class="text-sm font-medium text-warning">Verification can read a large amount of data from this destination</p>
    <p class="text-xs text-text-muted">After each backup, Vault reads the backup back from this remote destination to check it. For deduplicated backups this includes every chunk the backup references, including chunks reused from earlier backups, not only new data.</p>
    <p class="text-xs text-text-muted">For large backups this can use significant bandwidth and add a long time after each backup, while the job progress stays near the end.</p>
    {#if providerCharges}
      <p class="text-xs text-text-muted">S3 and S3-compatible providers can also charge for requests, retrieval, or egress.</p>
    {/if}
    <p class="text-xs text-text-muted">For large backups, consider turning off <strong>Verify backups after writing</strong>{toggleLocation ? ` (${toggleLocation})` : ''} and using <strong>Scheduled verification</strong> instead. Quick mode only checks that the data exists; deep mode reads the latest restore point in full.</p>
  </div>
</div>
