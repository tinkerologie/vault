<script>
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
</script>

{#if show}
  <div class="fixed top-4 right-4 z-[100] animate-slide-in" role="alert" aria-live="polite">
    <div class="px-4 py-3 rounded-lg border shadow-lg {colors[type] || colors.info} flex items-center gap-3 min-w-[280px]">
      <span class="text-sm font-medium">{message}</span>
      <button onclick={() => show = false} class="ml-auto opacity-60 hover:opacity-100 transition-opacity" aria-label="Dismiss">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
      </button>
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
</style>
