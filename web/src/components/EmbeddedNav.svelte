<script>
  import { isMobileNavActive } from '../lib/mobile-nav.js'

  // The embedded view's only navigation (#526): it wraps, so every page stays
  // reachable at any width without scrolling sideways.
  /** @type {{ items: Array<{ path: string, label: string, external?: boolean, href?: string }>, route: string }} */
  let { items, route } = $props()

  const linkClass = 'inline-flex items-center min-h-11 lg:min-h-9 px-3 rounded-lg text-sm font-medium transition-colors'
</script>

<nav aria-label="Main navigation" class="flex flex-wrap gap-1 px-4 sm:px-6 lg:px-8 py-2 bg-surface-2 border-b border-border">
  {#each items as item (item.path)}
    {#if item.external}
      <a href={item.href} target="_blank" rel="noopener"
        aria-label="{item.label} (opens in a new tab)"
        class="{linkClass} text-text-muted hover:text-text hover:bg-surface-3">{item.label}</a>
    {:else}
      {@const active = isMobileNavActive(item.path, route)}
      <a href="#{item.path}"
        aria-current={active ? 'page' : undefined}
        class="{linkClass} {active ? 'bg-vault/10 text-vault-text' : 'text-text-muted hover:text-text hover:bg-surface-3'}">{item.label}</a>
    {/if}
  {/each}
</nav>
