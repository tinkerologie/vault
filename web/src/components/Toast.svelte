<script>
  import { floatAboveDialogs } from '../lib/dialog.js'

  let { message = '', type = 'info', key = 0 } = $props()

  let show = $state(false)
  let timeout = null

  $effect(() => {
    if (key > 0) {
      show = true
      clearTimeout(timeout)
      // Errors persist until manually dismissed; others auto-dismiss after 4s
      if (type !== 'error') {
        timeout = setTimeout(() => { show = false }, 4000)
      }
    }
  })

  const colors = {
    success: 'bg-success/20 border-success/40 text-success',
    error: 'bg-danger/20 border-danger/40 text-danger',
    warning: 'bg-warning/20 border-warning/40 text-warning',
    info: 'bg-info/20 border-info/40 text-info',
  }

  // Colour is never the only cue: each type also has its own icon.
  const icons = {
    success: 'M5 13l4 4L19 7',
    error: 'M6 18L18 6M6 6l12 12',
    warning: 'M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z',
    info: 'M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z',
  }
</script>

{#if show}
  <div use:floatAboveDialogs class="fixed top-4 right-4 z-[100] animate-slide-in" role="alert" aria-live="polite">
    <!-- Opaque base, so text contrast never depends on the page under the toast. -->
    <div class="rounded-lg bg-surface-2 shadow-lg">
      <div class="px-4 py-3 rounded-lg border {colors[type] || colors.info} flex items-center gap-3 min-w-[280px]">
        <svg aria-hidden="true" data-toast-icon={icons[type] ? type : 'info'} class="w-4 h-4 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={icons[type] || icons.info}/></svg>
        <span class="text-sm font-medium">{message}</span>
        <button onclick={() => show = false} class="hit-target hit-target-touch ml-auto hover:opacity-70 transition-opacity" aria-label="Dismiss">
          <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  @keyframes slide-in {
    from { transform: translateX(100%); opacity: 0; }
    to { transform: translateX(0); opacity: 1; }
  }
  .animate-slide-in {
    animation: slide-in 0.3s ease-out;
  }
  @media (prefers-reduced-motion: reduce) {
    .animate-slide-in {
      animation: none;
    }
  }
</style>
