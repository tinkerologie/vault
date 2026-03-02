<script>
  let {
    show = false,
    title = 'Confirm',
    message = 'Are you sure?',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    variant = 'danger',
    onconfirm = () => {},
    oncancel = () => {},
  } = $props()

  function handleBackdrop(e) {
    if (e.target === e.currentTarget) oncancel()
  }

  function handleKey(e) {
    if (e.key === 'Escape') oncancel()
  }

  const variants = {
    danger: 'bg-danger text-white hover:bg-danger/90',
    warning: 'bg-warning text-white hover:bg-warning/90',
    vault: 'bg-vault text-white hover:bg-vault-dark',
  }
</script>

<svelte:window onkeydown={handleKey} />

{#if show}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
    onclick={handleBackdrop}
    onkeydown={handleKey}
    role="dialog"
    aria-modal="true"
    aria-labelledby="confirm-title"
    aria-describedby="confirm-message"
    tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
      <h2 id="confirm-title" class="text-lg font-semibold text-text">{title}</h2>
      <p id="confirm-message" class="text-sm text-text-muted mt-2">{message}</p>
      <div class="flex justify-end gap-3 mt-6">
        <button
          type="button"
          onclick={oncancel}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors"
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          onclick={onconfirm}
          class="px-4 py-2 text-sm font-medium rounded-lg transition-colors {variants[variant] || variants.danger}"
        >
          {confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}
