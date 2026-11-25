<script>
  // The one switch (#698). Two markups share the track, thumb, boundary and
  // focus styling:
  // - button (default): a controlled `<button role="switch" aria-checked>`.
  //   The caller owns the state and saves it in `onchange(next)`.
  // - checkbox (`native`): a visually hidden `<input type="checkbox">` inside a
  //   `<label>`, bound with `bind:checked`, for switches inside forms that are
  //   labelled by a sibling `<label for>` or `aria-labelledby`.
  // The off track and its thumb carry a switch-edge ring and the on track a
  // focus-ring edge, so each part keeps a 3:1 boundary in every theme style (#684;
  // checked by src/lib/palette-contrast.test.js). Extra attributes (aria-*,
  // data-testid, title) go to the button or the input.
  let {
    checked = $bindable(false),
    onchange = undefined,
    native = false,
    size = 'sm',
    disabled = false,
    label = undefined,
    stopPropagation = false,
    title = undefined,
    class: className = '',
    ...rest
  } = $props()

  const SIZES = {
    sm: { track: 'w-9 h-5', thumb: 'w-4 h-4', on: 'translate-x-4' },
    md: { track: 'w-11 h-6', thumb: 'w-5 h-5', on: 'translate-x-5' },
  }
  let dims = $derived(SIZES[size] || SIZES.sm)
  let trackClass = $derived(`switch-track relative inline-block shrink-0 ${dims.track} rounded-full transition-colors ${checked
    ? 'bg-vault shadow-[inset_0_0_0_1px_var(--color-focus-ring)]'
    : 'bg-surface-4 shadow-[inset_0_0_0_1px_var(--color-switch-edge)]'}`)
  let thumbClass = $derived(`absolute top-0.5 left-0.5 ${dims.thumb} rounded-full bg-switch-thumb transition-transform ${checked
    ? `${dims.on} shadow-[0_0_0_1px_var(--color-on-vault)]`
    : 'shadow-[0_0_0_1px_var(--color-switch-edge)]'}`)

  function toggle(/** @type {MouseEvent} */ e) {
    if (stopPropagation) e.stopPropagation()
    if (disabled) return
    const next = !checked
    if (onchange) onchange(next)
    else checked = next
  }
</script>

{#if native}
  <label {title} class="relative inline-flex items-center shrink-0 {disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'} {className}">
    <input
      type="checkbox"
      bind:checked
      {disabled}
      aria-label={label}
      class="sr-only peer"
      onchange={(e) => onchange?.(e.currentTarget.checked)}
      {...rest}
    />
    <span aria-hidden="true" class={trackClass}><span class={thumbClass}></span></span>
  </label>
{:else}
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    {title}
    {disabled}
    onclick={toggle}
    class="relative inline-flex items-center shrink-0 cursor-pointer disabled:opacity-60 disabled:cursor-not-allowed {className}"
    {...rest}
  >
    <span aria-hidden="true" class={trackClass}><span class={thumbClass}></span></span>
  </button>
{/if}
