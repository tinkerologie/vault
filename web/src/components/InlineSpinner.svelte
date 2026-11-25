<script>
  // Small inline spinner for use INSIDE buttons and inline text. Unlike the
  // section-level Spinner (which adds py-12 padding and a "Loading…" label),
  // this renders only the ring and inherits the surrounding text colour via
  // currentColor, so it sits correctly next to button labels.
  // It is decorative (#698): the label beside it ("Saving…") says what is
  // happening, and a name here would be merged into it ("Loading Saving…").
  let { size = 'sm' } = $props()
  const sizes = { sm: 'w-4 h-4', md: 'w-5 h-5' }
</script>

<span
  class="inline-block {sizes[size] || sizes.sm} border-2 border-current/30 border-t-current rounded-full animate-spin align-[-0.125em]"
  aria-hidden="true"
></span>
