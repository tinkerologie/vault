<script>
  import Modal from './Modal.svelte'

  // Cancel takes initial focus, so a stray Enter can never confirm. A field in
  // `children` marked `data-autofocus` takes it instead, being earlier in the DOM.
  // `description` replaces the plain `message` when the text needs markup.
  let {
    show = false,
    title = 'Confirm',
    message = 'Are you sure?',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    variant = 'danger',
    confirmDisabled = false,
    onconfirm = () => {},
    oncancel = () => {},
    description = undefined,
    children = undefined,
  } = $props()

  const id = $props.id()

  const variants = {
    danger: 'bg-danger text-on-status hover:bg-danger/90',
    warning: 'bg-warning text-on-status hover:bg-warning/90',
    vault: 'bg-vault text-on-vault hover:bg-vault-dark',
  }
</script>

<Modal {show} size="md" labelledby="{id}-title" describedby="{id}-message" onclose={() => oncancel()}>
  <div class="p-6 overflow-y-auto">
    <h2 id="{id}-title" class="text-lg font-semibold text-text">{title}</h2>
    <p id="{id}-message" class="text-sm text-text-muted mt-2">
      {#if description}{@render description()}{:else}{message}{/if}
    </p>
    {@render children?.()}
    <div class="flex justify-end gap-3 mt-6">
      <button
        type="button"
        data-autofocus
        onclick={() => oncancel()}
        class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors"
      >
        {cancelLabel}
      </button>
      <button
        type="button"
        disabled={confirmDisabled}
        onclick={() => onconfirm()}
        class="px-4 py-2 text-sm font-medium rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed {variants[variant] || variants.danger}"
      >
        {confirmLabel}
      </button>
    </div>
  </div>
</Modal>
