<script>
  import Modal from './Modal.svelte'
  import { groupBySection, isMobileNavActive } from '../lib/mobile-nav.js'

  // The pages the mobile bottom bar has no room for (#530). Modal owns focus,
  // Escape and inertness; this only lists the links.
  /** @type {{
   *   show: boolean,
   *   id?: string,
   *   items: { path: string, label: string, icon: string, section?: string, external?: boolean, href?: string }[],
   *   route: string,
   *   onclose?: () => void,
   * }} */
  let { show = false, id = undefined, items = [], route = '', onclose = () => {} } = $props()

  let groups = $derived(groupBySection(items))
  let firstPath = $derived(items[0]?.path)
</script>

{#snippet icon(d)}
  <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" {d}/></svg>
{/snippet}

<Modal {show} {id} title="More" placement="sheet" {onclose}>
  <nav aria-label="More pages">
    {#each groups as group (group.section ?? '_top')}
      {#if group.section}
        <div class="px-3 pt-3 pb-1 text-xs font-semibold uppercase tracking-wider text-text-muted select-none">{group.section}</div>
      {/if}
      {#each group.items as item (item.path)}
        {#if item.external}
          <a href={item.href} target="_blank" rel="noopener" onclick={() => onclose()}
            data-autofocus={item.path === firstPath ? '' : undefined}
            aria-label="{item.label} (opens in a new tab)"
            class="flex items-center gap-3 px-3 min-h-[44px] rounded-lg text-sm font-medium text-text-muted hover:text-text hover:bg-surface-3">
            {@render icon(item.icon)}
            {item.label}
          </a>
        {:else}
          {@const active = isMobileNavActive(item.path, route)}
          <a href="#{item.path}" onclick={() => onclose()}
            data-autofocus={item.path === firstPath ? '' : undefined}
            aria-current={active ? 'page' : undefined}
            class="flex items-center gap-3 px-3 min-h-[44px] rounded-lg text-sm font-medium
              {active ? 'bg-vault/10 text-vault-text nav-active' : 'text-text-muted hover:text-text hover:bg-surface-3'}">
            {@render icon(item.icon)}
            {item.label}
          </a>
        {/if}
      {/each}
    {/each}
  </nav>
</Modal>
