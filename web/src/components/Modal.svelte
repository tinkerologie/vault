<script>
  import { closeOnBackdropClick, modalDialog } from '../lib/dialog.js'

  // The only dialog implementation: everything modal composes this component.
  // With a `title` it renders the standard header and padded body. Without one,
  // the content owns its whole layout and names the dialog through `label` or
  // `labelledby`. A descendant marked `data-autofocus` takes initial focus.
  /** @type {{
   *   show: boolean,
   *   title?: string,
   *   label?: string,
   *   labelledby?: string,
   *   describedby?: string,
   *   size?: 'default' | 'md' | 'lg',
   *   placement?: 'center' | 'top' | 'sheet',
   *   id?: string,
   *   onclose?: () => void,
   *   children?: import('svelte').Snippet,
   *   stepper?: import('svelte').Snippet,
   * }} */
  let {
    show = false,
    title = '',
    label = undefined,
    labelledby = undefined,
    describedby = undefined,
    size = 'default',
    placement = 'center',
    id = undefined,
    onclose = () => {},
    children,
    stepper,
  } = $props()

  const titleId = $props.id()
  const sizeClass = { default: 'max-w-lg', md: 'max-w-md', lg: 'max-w-2xl' }
  const isSheet = $derived(placement === 'sheet')
  // A sheet holds no work to lose, so tapping outside it can dismiss it.
  const backdropClose = $derived(isSheet ? () => onclose() : null)
</script>

{#if show}
  <dialog
    use:modalDialog={() => onclose()}
    use:closeOnBackdropClick={backdropClose}
    {id}
    class="vault-dialog"
    class:vault-dialog-top={placement === 'top'}
    class:vault-dialog-sheet={isSheet}
    aria-label={label}
    aria-labelledby={title ? titleId : labelledby}
    aria-describedby={describedby}
  >
    <div class="bg-surface-2 border border-border shadow-2xl w-full {sizeClass[size]} max-h-[90vh] flex flex-col animate-panel-up {isSheet ? 'rounded-t-xl border-b-0 pb-[env(safe-area-inset-bottom)]' : 'rounded-xl mx-4'}" class:overflow-hidden={!title}>
      {#if title}
        <div class="flex items-center justify-between px-6 py-4 border-b border-border">
          <h2 id={titleId} class="text-lg font-semibold text-text">{title}</h2>
          <button type="button" onclick={() => onclose()} class="text-text-muted hover:text-text transition-colors p-1 rounded-lg hover:bg-surface-3" aria-label="Close">
            <svg aria-hidden="true" class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
          </button>
        </div>
      {/if}
      {#if stepper}
        <div class="px-6 py-3 border-b border-border">
          {@render stepper()}
        </div>
      {/if}
      <div class={title ? 'px-6 py-4 overflow-y-auto overflow-x-hidden flex-1' : 'flex flex-col min-h-0 flex-1'}>
        {@render children?.()}
      </div>
    </div>
  </dialog>
{/if}
