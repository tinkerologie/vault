<script>
  // eslint-disable-next-line no-unused-vars -- children is an implicit Svelte slot prop
  let { icon = '', title = '', description = '', subtitle = '', actionLabel = '', onaction = null, iconSlot, children } = $props()
</script>

<div class="flex flex-col items-center justify-center py-16 text-center">
  {#if iconSlot}
    <div class="mb-3 opacity-30">{@render iconSlot()}</div>
  {:else if icon}
    <div class="text-4xl mb-3 opacity-30">{icon}</div>
  {/if}
  <h3 class="text-lg font-medium text-text-muted">{title}</h3>
  {#if subtitle}
    <p class="text-xs text-text-dim mt-0.5">{subtitle}</p>
  {/if}
  {#if description}
    <p class="text-sm text-text-dim mt-1 max-w-sm">{description}</p>
  {/if}
  {#if actionLabel && onaction}
    <button onclick={onaction} class="mt-4 px-4 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors">
      {actionLabel}
    </button>
  {/if}
</div>
