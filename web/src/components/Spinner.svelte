<script>
  let { text = 'Loading...', size = 'md' } = $props()

  const sizes = {
    sm: 'w-5 h-5 border',
    md: 'w-8 h-8 border-2',
    lg: 'w-12 h-12 border-2',
  }
</script>

<!-- A status region, so the loading text is read out (#698); the ring is decoration. -->
<div role="status" class="flex flex-col items-center justify-center py-12 gap-3">
  <div aria-hidden="true" class="{sizes[size] || sizes.md} border-vault/30 border-t-vault rounded-full animate-spin"></div>
  <span class="text-sm text-text-muted">{text}</span>
</div>
