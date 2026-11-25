<script>
  // A failed load, said near the data it affects, with a Retry that reloads only
  // that data (#681). Use it instead of an empty or "all clear" state: unknown is
  // not green (DESIGN.md).
  /** @type {{ title: string, message?: string, onretry?: (() => unknown) | null, retrying?: boolean, compact?: boolean }} */
  let { title, message = '', onretry = null, retrying = false, compact = false } = $props()
</script>

<div data-load-error class="bg-danger/10 border border-danger/30 rounded-xl flex items-start gap-3 {compact ? 'p-3' : 'p-4'}">
  <svg aria-hidden="true" class="w-5 h-5 shrink-0 text-danger {compact ? '' : 'mt-0.5'}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
  <div class="min-w-0 flex-1 break-words">
    <p class="text-sm font-medium text-danger">{title}</p>
    {#if message}<p class="text-xs text-text-muted mt-0.5">{message}</p>{/if}
  </div>
  {#if onretry}
    <button type="button" onclick={() => onretry?.()} disabled={retrying}
      class="shrink-0 min-h-[36px] px-3 py-1.5 text-sm font-medium text-text bg-surface-2 border border-border hover:bg-surface-3 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
      {retrying ? 'Retrying…' : 'Retry'}
    </button>
  {/if}
</div>
