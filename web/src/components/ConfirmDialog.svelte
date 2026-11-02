<script>
  import Modal from './Modal.svelte'

  // Cancel takes initial focus, so a stray Enter can never confirm. A field in
  // `children` marked `data-autofocus` takes it instead, being earlier in the DOM.
  // `description` replaces the plain `message` when the text needs markup.
  // `confirmWord` makes this a typed confirmation (#525): confirm stays disabled
  // until the exact word is typed. Focus still starts on Cancel, so Enter there
  // cancels, and the field is one Tab away. `source` ({label, value}[]) names what
  // is affected and `consequences` (string[]) states the loss. `busy` locks the
  // dialog while the request runs, so it can't be dismissed or sent twice.
  let {
    show = false,
    title = 'Confirm',
    message = 'Are you sure?',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    variant = 'danger',
    confirmDisabled = false,
    confirmWord = '',
    source = [],
    consequences = [],
    busy = false,
    onconfirm = () => {},
    oncancel = () => {},
    description = undefined,
    children = undefined,
  } = $props()

  const id = $props.id()

  let typed = $state('')
  let canConfirm = $derived(!busy && !confirmDisabled && (!confirmWord || typed === confirmWord))

  $effect(() => {
    if (!show) typed = ''
  })

  function confirm() {
    if (canConfirm) onconfirm()
  }

  function cancel() {
    if (!busy) oncancel()
  }

  function onWordKeydown(e) {
    if (e.key !== 'Enter') return
    e.preventDefault()
    confirm()
  }

  const variants = {
    danger: 'bg-danger text-on-status hover:bg-danger/90',
    warning: 'bg-warning text-on-status hover:bg-warning/90',
    vault: 'bg-vault text-on-vault hover:bg-vault-dark',
  }
</script>

<Modal {show} size="md" labelledby="{id}-title" describedby="{id}-message" onclose={cancel}>
  <div class="flex flex-col min-h-0" aria-busy={busy ? 'true' : undefined}>
    <!-- Only the text scrolls, so focusing Cancel can't push the title off a small screen. -->
    <div class="px-6 pt-6 pb-1 overflow-y-auto">
      <h2 id="{id}-title" class="text-lg font-semibold text-text">{title}</h2>
      <div id="{id}-message" class="mt-2">
        <p class="text-sm text-text-muted">
          {#if description}{@render description()}{:else}{message}{/if}
        </p>
        {#if source.length > 0}
          <dl class="mt-3 p-3 bg-surface-3 rounded-lg text-sm space-y-2">
            {#each source as { label, value }, i (i)}
              <div>
                <dt class="text-xs text-text-muted">{label}</dt>
                <dd class="text-text font-medium [overflow-wrap:anywhere]">{value}</dd>
              </div>
            {/each}
          </dl>
        {/if}
        {#if consequences.length > 0}
          <ul class="mt-3 pl-5 list-disc text-sm text-text space-y-1">
            {#each consequences as consequence, i (i)}
              <li>{consequence}</li>
            {/each}
          </ul>
        {/if}
      </div>
      {#if confirmWord}
        <label class="block mt-4 text-sm text-text">
          Type <strong class="font-mono font-semibold">{confirmWord}</strong> to confirm
          <input
            type="text"
            bind:value={typed}
            disabled={busy}
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            onkeydown={onWordKeydown}
            class="mt-1.5 w-full px-3 py-2 text-base sm:text-sm font-mono bg-surface-3 border border-border rounded-lg text-text focus:outline-none focus:border-vault disabled:opacity-50"
          />
        </label>
      {/if}
      {@render children?.()}
    </div>
    <div class="flex justify-end gap-3 px-6 pt-5 pb-6">
      <button
        type="button"
        data-autofocus
        disabled={busy}
        onclick={cancel}
        class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {cancelLabel}
      </button>
      <button
        type="button"
        disabled={!canConfirm}
        onclick={confirm}
        class={`px-4 py-2 text-sm font-medium rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed ${variants[variant] || variants.danger}`}
      >
        {confirmLabel}
      </button>
    </div>
  </div>
</Modal>
