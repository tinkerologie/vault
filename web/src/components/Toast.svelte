<script>
  import { untrack } from 'svelte'
  import { floatAboveDialogs } from '../lib/dialog.js'
  import { ICONS } from '../lib/icons.js'

  // A page raises a toast by setting `message` and `type` and bumping `key`.
  // Toasts stack rather than replace each other (#685): errors and warnings stay
  // until dismissed, success and info leave after AUTO_DISMISS_MS, and the
  // pointer or focus anywhere in the stack holds every timer.
  let { message = '', type = 'info', key = 0 } = $props()

  const AUTO_DISMISS_MS = 4000
  const MAX_TOASTS = 5
  const PERSISTENT = new Set(['error', 'warning'])

  /** @typedef {{ id: number, message: string, type: string, remaining: number, startedAt: number, timer: ReturnType<typeof setTimeout> | null }} Item */
  /** @type {Item[]} */
  let items = $state([])
  let nextId = 0
  let hovered = false
  let focused = false

  const paused = () => hovered || focused

  /** @param {Item} item */
  function start(item) {
    if (PERSISTENT.has(item.type) || item.timer || paused()) return
    item.startedAt = Date.now()
    item.timer = setTimeout(() => dismiss(item.id), item.remaining)
  }

  /** @param {Item} item */
  function stop(item) {
    if (!item.timer) return
    clearTimeout(item.timer)
    item.timer = null
    item.remaining = Math.max(0, item.remaining - (Date.now() - item.startedAt))
  }

  /** @param {number} id */
  function dismiss(id) {
    const item = items.find((i) => i.id === id)
    if (item?.timer) clearTimeout(item.timer)
    items = items.filter((i) => i.id !== id)
    // The stack unmounts with its last toast, so the pointer or focus never
    // gets to leave it: clear the pause, or later toasts would never go.
    if (items.length === 0) {
      hovered = false
      focused = false
    }
  }

  function push(text, kind) {
    /** @type {Item} */
    const item = { id: ++nextId, message: text, type: kind, remaining: AUTO_DISMISS_MS, startedAt: 0, timer: null }
    let list = [...items, item]
    // Over the cap, the oldest passing toast goes first; a warning or error
    // only when nothing else is left to drop.
    while (list.length > MAX_TOASTS) {
      const drop = list.find((i) => !PERSISTENT.has(i.type)) ?? list[0]
      if (drop.timer) clearTimeout(drop.timer)
      list = list.filter((i) => i !== drop)
    }
    items = list
    start(item)
  }

  // Only a new key raises a toast; a message edited in place does not.
  let shownKey = 0
  $effect(() => {
    const k = key
    untrack(() => {
      if (k > 0 && k !== shownKey) {
        shownKey = k
        push(message, type)
      }
    })
  })

  $effect(() => () => items.forEach((i) => i.timer && clearTimeout(i.timer)))

  function setPaused(next) {
    const was = paused()
    next()
    if (was === paused()) return
    for (const item of items) paused() ? stop(item) : start(item)
  }

  const onmouseenter = () => setPaused(() => { hovered = true })
  const onmouseleave = () => setPaused(() => { hovered = false })
  const onfocusin = () => setPaused(() => { focused = true })
  /** @param {FocusEvent} e */
  function onfocusout(e) {
    const to = /** @type {Node | null} */ (e.relatedTarget)
    if (to && /** @type {HTMLElement} */ (e.currentTarget).contains(to)) return
    setPaused(() => { focused = false })
  }

  const colors = {
    success: 'bg-success/20 border-success/40 text-success',
    error: 'bg-danger/20 border-danger/40 text-danger',
    warning: 'bg-warning/20 border-warning/40 text-warning',
    info: 'bg-info/20 border-info/40 text-info',
  }

  // Colour is never the only cue: each type also has its own icon.
  const icons = {
    success: ICONS.check,
    error: ICONS.close,
    warning: ICONS.warning,
    info: ICONS.info,
  }
</script>

{#if items.length > 0}
  <!-- Below the connection banner when it shows (App.svelte sets the offset). -->
  <div
    use:floatAboveDialogs
    class="vault-toasts fixed right-4 z-[100] flex flex-col items-end gap-2 max-w-[calc(100vw-2rem)]"
    role="presentation"
    {onmouseenter}
    {onmouseleave}
    {onfocusin}
    {onfocusout}
  >
    {#each items as item (item.id)}
      <!-- Errors and warnings interrupt; success and info wait their turn. -->
      <div data-toast={item.type} role={PERSISTENT.has(item.type) ? 'alert' : 'status'} class="animate-slide-in">
        <!-- Opaque base, so text contrast never depends on the page under the toast. -->
        <div class="rounded-lg bg-surface-2 shadow-lg">
          <div class="px-4 py-3 rounded-lg border {colors[item.type] || colors.info} flex items-center gap-3 min-w-[280px] max-w-md">
            <svg aria-hidden="true" data-toast-icon={icons[item.type] ? item.type : 'info'} class="w-4 h-4 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={icons[item.type] || icons.info}/></svg>
            <span class="text-sm font-medium">{item.message}</span>
            <button type="button" onclick={() => dismiss(item.id)} class="hit-target hit-target-touch ml-auto shrink-0 hover:opacity-70 transition-opacity" aria-label="Dismiss">
              <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.close}/></svg>
            </button>
          </div>
        </div>
      </div>
    {/each}
  </div>
{/if}

<style>
  .vault-toasts {
    top: calc(1rem + var(--vault-banner-offset, 0px));
  }
  :global(html.vault-embedded) .vault-toasts {
    top: calc(var(--vault-visible-top) + 1rem + var(--vault-banner-offset, 0px));
  }
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
