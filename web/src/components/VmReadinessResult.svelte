<script>
  let { readiness } = $props()

  const outcomes = { passed: 'Passed', failed: 'Failed', not_attempted: 'Not attempted' }
</script>

<div class="mt-2" role="group" aria-label="VM readiness">
  <h4 class="font-medium text-text">VM readiness</h4>
  <dl class="mt-1 space-y-1 text-text-muted">
    <div class="flex flex-wrap gap-x-2"><dt>VM startup</dt><dd>{outcomes[readiness.startup]}</dd></div>
    <div class="flex flex-wrap gap-x-2"><dt>Libvirt running state</dt><dd>{outcomes[readiness.running]}</dd></div>
    {#if readiness.guest_check === 'guest_agent'}
      <div class="flex flex-wrap gap-x-2"><dt>QEMU guest agent</dt><dd>{outcomes[readiness.guest_outcome]}</dd></div>
    {:else if readiness.guest_check === 'tcp'}
      <div class="flex flex-wrap gap-x-2"><dt>TCP port {readiness.tcp_port}</dt><dd>{outcomes[readiness.guest_outcome]}</dd></div>
    {/if}
    {#if readiness.cleanup_stop !== 'not_attempted'}
      <div class="flex flex-wrap gap-x-2"><dt>VM stop check after startup or readiness failure</dt><dd>{outcomes[readiness.cleanup_stop]}</dd></div>
    {/if}
    <div class="flex flex-wrap gap-x-2"><dt>Application health</dt><dd>Unchecked</dd></div>
  </dl>
</div>
