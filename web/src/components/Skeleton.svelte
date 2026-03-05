<script>
  /** @type {'stats' | 'card' | 'table'} */
  let { variant = 'card', count = 3 } = $props()
</script>

{#if variant === 'stats'}
  <div class="flex gap-4 overflow-x-auto pb-2 snap-x snap-mandatory lg:grid lg:grid-cols-5 lg:overflow-visible lg:pb-0 mb-8">
    {#each { length: 5 } as _s, i (i)}
      <div class="bg-surface-2 border border-border rounded-xl p-5 snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink animate-pulse">
        <div class="h-4 w-16 bg-surface-3 rounded mb-2"></div>
        <div class="h-7 w-12 bg-surface-3 rounded mb-2"></div>
        <div class="h-3 w-20 bg-surface-3 rounded"></div>
      </div>
    {/each}
  </div>
{:else if variant === 'card'}
  <div class="space-y-3">
    {#each { length: count } as _s, i (i)}
      <div class="bg-surface-2 border border-border rounded-xl p-5 animate-pulse">
        <div class="flex items-start justify-between">
          <div class="flex-1">
            <div class="h-4 w-40 bg-surface-3 rounded mb-3"></div>
            <div class="h-3 w-64 bg-surface-3 rounded mb-2"></div>
            <div class="flex gap-4 mt-2">
              <div class="h-3 w-24 bg-surface-3 rounded"></div>
              <div class="h-3 w-20 bg-surface-3 rounded"></div>
              <div class="h-3 w-28 bg-surface-3 rounded"></div>
            </div>
          </div>
          <div class="flex gap-1">
            <div class="w-8 h-8 bg-surface-3 rounded-lg"></div>
            <div class="w-8 h-8 bg-surface-3 rounded-lg"></div>
          </div>
        </div>
      </div>
    {/each}
  </div>
{:else if variant === 'table'}
  <div class="bg-surface-2 border border-border rounded-xl overflow-hidden animate-pulse">
    <div class="px-5 py-3 border-b border-border flex gap-8">
      {#each { length: 5 } as _s, i (i)}
        <div class="h-3 w-16 bg-surface-3 rounded"></div>
      {/each}
    </div>
    {#each { length: count } as _s, i (i)}
      <div class="px-5 py-4 border-b border-border/50 flex gap-8">
        <div class="h-4 w-32 bg-surface-3 rounded"></div>
        <div class="h-4 w-16 bg-surface-3 rounded"></div>
        <div class="h-4 w-12 bg-surface-3 rounded"></div>
        <div class="h-4 w-20 bg-surface-3 rounded"></div>
        <div class="h-4 w-24 bg-surface-3 rounded"></div>
      </div>
    {/each}
  </div>
{/if}
