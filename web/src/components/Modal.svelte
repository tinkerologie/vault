<script>
  /** @type {{ show: boolean, title?: string, onclose?: () => void, children?: import('svelte').Snippet }} */
  let { show = false, title = '', onclose = () => {}, children } = $props()

  let dialogEl = $state(null)

  function handleBackdrop(e) {
    if (e.target === e.currentTarget) onclose()
  }

  function handleKey(e) {
    if (e.key === 'Escape') onclose()
    // Focus trap: cycle focus within the modal
    if (e.key === 'Tab' && dialogEl) {
      const focusable = dialogEl.querySelectorAll(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
      )
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
  }

  // Auto-focus first focusable element when opened
  $effect(() => {
    if (show && dialogEl) {
      requestAnimationFrame(() => {
        const first = dialogEl.querySelector(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        )
        if (first) first.focus()
      })
    }
  })
</script>

<svelte:window onkeydown={handleKey} />

{#if show}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
    onclick={handleBackdrop}
    onkeydown={handleKey}
    role="dialog"
    aria-modal="true"
    aria-labelledby="modal-title"
    tabindex="-1"
  >
    <div bind:this={dialogEl} class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-lg mx-4 max-h-[90vh] flex flex-col">
      <div class="flex items-center justify-between px-6 py-4 border-b border-border">
        <h2 id="modal-title" class="text-lg font-semibold text-text">{title}</h2>
        <button onclick={onclose} class="text-text-muted hover:text-text transition-colors p-1 rounded-lg hover:bg-surface-3" aria-label="Close">
          <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
        </button>
      </div>
      <div class="px-6 py-4 overflow-y-auto flex-1">
        {@render children()}
      </div>
    </div>
  </div>
{/if}
