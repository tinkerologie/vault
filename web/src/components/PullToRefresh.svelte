<script>
  let { onrefresh = async () => {}, children } = $props()

  let pulling = $state(false)
  let pullDistance = $state(0)
  let refreshing = $state(false)
  let startY = 0
  const threshold = 80

  function handleTouchStart(e) {
    if (window.scrollY === 0) {
      startY = e.touches[0].clientY
      pulling = true
    }
  }

  function handleTouchMove(e) {
    if (!pulling) return
    const diff = e.touches[0].clientY - startY
    if (diff > 0) {
      pullDistance = Math.min(diff * 0.5, threshold * 1.5)
      if (diff > 10) e.preventDefault()
    }
  }

  async function handleTouchEnd() {
    if (pullDistance >= threshold) {
      refreshing = true
      await onrefresh()
      refreshing = false
    }
    pulling = false
    pullDistance = 0
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  ontouchstart={handleTouchStart}
  ontouchmove={handleTouchMove}
  ontouchend={handleTouchEnd}
  class="relative"
>
  {#if pullDistance > 0 || refreshing}
    <div class="flex justify-center items-center py-3 transition-all" style="height: {refreshing ? 40 : pullDistance}px">
      {#if refreshing}
        <div class="w-5 h-5 border-2 border-vault/30 border-t-vault rounded-full animate-spin"></div>
      {:else}
        <div class="w-5 h-5 text-vault transition-transform"
          style="transform: rotate({(pullDistance / threshold) * 360}deg); opacity: {pullDistance / threshold}">
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path stroke-linecap="round" stroke-linejoin="round" d="M19 14l-7 7m0 0l-7-7m7 7V3" />
          </svg>
        </div>
      {/if}
    </div>
  {/if}
  {@render children()}
</div>
