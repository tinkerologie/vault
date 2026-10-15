<script>
  /**
   * Phase, counters, rate, ETA, and a stall hint for the item currently
   * running (#456). Renders nothing when the daemon sent no structured
   * progress, so older daemons keep the existing milestone display.
   *
   * Props:
   * - item: progress store currentItem ({ name, detail, ... })
   * - runType: 'backup' | 'restore'
   * - size: 'sm' (dashboard card) | 'md' (restore wizard)
   */
  import { describeItemProgress } from '../lib/item-progress.js'

  let { item, runType = 'backup', size = 'sm' } = $props()

  // Re-evaluate the stall hint as time passes even when no event arrives.
  let now = $state(Date.now())
  $effect(() => {
    const id = setInterval(() => { now = Date.now() }, 15000)
    return () => clearInterval(id)
  })

  let view = $derived(describeItemProgress(item?.detail, runType, now))
  let textSize = $derived(size === 'md' ? 'text-xs' : 'text-[11px]')
</script>

{#if view}
  <div class="mt-1.5">
    <div class="flex items-baseline justify-between gap-2 {textSize}">
      <span class="text-text-muted truncate"><span class="text-text font-medium">{view.label}</span>{#if view.text}{' '}<span class="tabular-nums">{view.text}</span>{/if}</span>
      {#if view.rate || view.eta}
        <span class="text-text-dim tabular-nums shrink-0">{[view.rate, view.eta].filter(Boolean).join(' · ')}</span>
      {/if}
    </div>
    {#if view.percent != null}
      <div class="w-full h-1.5 bg-surface-4 rounded-full overflow-hidden mt-1"
        role="progressbar" aria-label="{view.label} {item.name}" aria-valuemin="0" aria-valuemax="100" aria-valuenow={view.percent}>
        <div class="h-full rounded-full bg-vault transition-all duration-300" style="width: {view.percent}%"></div>
      </div>
    {:else}
      <div class="w-full h-1.5 bg-surface-4 rounded-full overflow-hidden mt-1" role="progressbar" aria-label="{view.label} {item.name}">
        <div class="h-full w-full rounded-full shimmer-bar"></div>
      </div>
    {/if}
    {#if view.stall}
      <p class="{textSize} text-text-dim mt-1">{view.stall}</p>
    {/if}
  </div>
{/if}
