<script>
  /**
   * Tooltip — contextual info icon with hover/click-triggered tooltip popup.
   *
   * Props:
   * - text: string — tooltip content
   * - id: string (optional) — unique ID for aria-describedby linking
   */
  let { text = '', id = undefined } = $props()

  let visible = $state(false)
  let position = $state('above')  // 'above' | 'below'
  let triggerEl = $state(null)
  let tooltipEl = $state(null)
  let hoverTimeout = $state(null)
  let isTouch = $state(false)

  const _fallbackId = `tooltip-${crypto.randomUUID().slice(0, 8)}`
  let tooltipId = $derived(id ?? _fallbackId)

  function updatePosition() {
    if (!triggerEl) return
    const rect = triggerEl.getBoundingClientRect()
    position = rect.top < 100 ? 'below' : 'above'
  }

  function show() {
    if (isTouch) return
    if (hoverTimeout) clearTimeout(hoverTimeout)
    hoverTimeout = setTimeout(() => {
      updatePosition()
      visible = true
    }, 200)
  }

  function hide() {
    if (hoverTimeout) {
      clearTimeout(hoverTimeout)
      hoverTimeout = null
    }
    if (!isTouch) visible = false
  }

  function toggle(e) {
    e.preventDefault()
    e.stopPropagation()
    if (visible) {
      visible = false
    } else {
      updatePosition()
      visible = true
    }
  }

  function onKeydown(e) {
    if (e.key === 'Escape' && visible) {
      visible = false
      triggerEl?.focus()
    }
  }

  function onClickOutside(e) {
    if (visible && triggerEl && !triggerEl.contains(e.target) && tooltipEl && !tooltipEl.contains(e.target)) {
      visible = false
    }
  }

  function onTouchStart() {
    isTouch = true
  }

  $effect(() => {
    if (visible) {
      document.addEventListener('click', onClickOutside, true)
      document.addEventListener('keydown', onKeydown)
      window.addEventListener('scroll', updatePosition, true)
      window.addEventListener('resize', updatePosition)
      return () => {
        document.removeEventListener('click', onClickOutside, true)
        document.removeEventListener('keydown', onKeydown)
        window.removeEventListener('scroll', updatePosition, true)
        window.removeEventListener('resize', updatePosition)
      }
    }
  })

  $effect(() => {
    window.addEventListener('touchstart', onTouchStart, { once: true })
    return () => window.removeEventListener('touchstart', onTouchStart)
  })
</script>

<span class="inline-flex items-center relative" style="vertical-align: middle;">
  <button
    bind:this={triggerEl}
    type="button"
    class="tooltip-trigger"
    aria-describedby={visible ? tooltipId : undefined}
    onmouseenter={show}
    onmouseleave={hide}
    onfocus={show}
    onblur={hide}
    onclick={toggle}
  >
    <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
      <circle cx="12" cy="12" r="10" />
      <path d="M12 16v-4M12 8h.01" />
    </svg>
    <span class="sr-only">More info</span>
  </button>

  {#if visible}
    <span
      bind:this={tooltipEl}
      id={tooltipId}
      role="tooltip"
      class="tooltip-popup {position === 'above' ? 'tooltip-above' : 'tooltip-below'}"
    >
      {text}
      <span class="tooltip-arrow {position === 'above' ? 'tooltip-arrow-down' : 'tooltip-arrow-up'}"></span>
    </span>
  {/if}
</span>

<style>
  .tooltip-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--color-text-dim);
    cursor: help;
    padding: 2px;
    border-radius: 9999px;
    transition: color 0.15s;
    background: none;
    border: none;
    outline: none;
    margin-left: 4px;
  }
  .tooltip-trigger:hover,
  .tooltip-trigger:focus-visible {
    color: var(--color-vault);
  }
  .tooltip-trigger:focus-visible {
    box-shadow: 0 0 0 2px var(--color-vault);
  }

  .tooltip-popup {
    position: absolute;
    left: 50%;
    transform: translateX(-50%);
    width: max-content;
    max-width: 260px;
    padding: 6px 10px;
    font-size: 0.75rem;
    line-height: 1.4;
    color: var(--color-text);
    background: var(--color-surface-3);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    box-shadow: 0 4px 12px rgba(0,0,0,0.15);
    z-index: 50;
    pointer-events: none;
    animation: tooltip-in 0.15s ease-out;
  }
  .tooltip-above {
    bottom: calc(100% + 8px);
  }
  .tooltip-below {
    top: calc(100% + 8px);
  }

  .tooltip-arrow {
    position: absolute;
    left: 50%;
    transform: translateX(-50%);
    width: 0;
    height: 0;
    border-left: 5px solid transparent;
    border-right: 5px solid transparent;
  }
  .tooltip-arrow-down {
    bottom: -5px;
    border-top: 5px solid var(--color-border);
  }
  .tooltip-arrow-up {
    top: -5px;
    border-bottom: 5px solid var(--color-border);
  }

  @keyframes tooltip-in {
    from { opacity: 0; transform: translateX(-50%) translateY(4px); }
    to { opacity: 1; transform: translateX(-50%) translateY(0); }
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border-width: 0;
  }
</style>
