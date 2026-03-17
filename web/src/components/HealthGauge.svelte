<script>
  let { score = 0, summary = '', avgSpeed = null } = $props()

  let color = $derived(
    score >= 80 ? 'var(--color-success)' :
    score >= 50 ? 'var(--color-warning)' :
    'var(--color-danger)'
  )

  // SVG arc calculation for circular progress
  const circumference = 2 * Math.PI * 45
  let dashOffset = $derived(circumference - (score / 100) * circumference)
</script>

<div class="flex items-center gap-6 bg-surface-2 border border-border rounded-xl p-6 mb-8">
  <div class="relative w-28 h-28 shrink-0">
    <svg aria-hidden="true" viewBox="0 0 100 100" class="w-full h-full -rotate-90">
      <circle cx="50" cy="50" r="45" fill="none" stroke="var(--color-border)" stroke-width="8" />
      <circle cx="50" cy="50" r="45" fill="none" stroke={color}
        stroke-width="8" stroke-linecap="round"
        stroke-dasharray={circumference} stroke-dashoffset={dashOffset}
        class="transition-all duration-1000 ease-out" />
    </svg>
    <div class="absolute inset-0 flex items-center justify-center">
      <span class="text-2xl font-bold text-text">{score}%</span>
    </div>
  </div>
  <div>
    <h3 class="text-lg font-semibold text-text">Backup Health</h3>
    <p class="text-sm text-text-muted mt-1">{summary}</p>
    {#if avgSpeed}
      <p class="text-xs text-text-dim mt-1.5">Avg. speed: {avgSpeed}</p>
    {/if}
  </div>
</div>
