<script>
  import { api } from '../lib/api.js'

  let { value = $bindable(''), onselect = () => {} } = $props()

  let open = $state(false)
  let entries = $state([])
  let currentPath = $state('')
  let loading = $state(false)
  let breadcrumbs = $derived(
    currentPath
      ? currentPath.split('/').filter(Boolean)
          .slice(1) // skip 'mnt' — already shown as the root breadcrumb
          .map((seg, i, arr) => ({
            name: seg,
            path: '/mnt/' + arr.slice(0, i + 1).join('/'),
          }))
      : []
  )

  async function browse(path = '') {
    loading = true
    try {
      const res = await api.browse(path)
      entries = res.entries || []
      currentPath = res.path || ''
    } catch {
      entries = []
    }
    loading = false
  }

  function openBrowser() {
    open = true
    browse(value || '')
  }

  function selectDir(entry) {
    browse(entry.path)
  }

  function confirm() {
    value = currentPath
    onselect(currentPath)
    open = false
  }

  function goTo(path) {
    browse(path)
  }
</script>

<div class="space-y-1">
  <div class="flex gap-2">
    <input
      type="text"
      bind:value
      placeholder="/mnt/user/backups"
      class="flex-1 bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text placeholder-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50"
    />
    <button
      type="button"
      onclick={openBrowser}
      class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-2 transition-colors"
      aria-label="Browse directories"
    >
      <svg aria-hidden="true" class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
    </button>
  </div>

  {#if open}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm animate-backdrop" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) open = false }}>
      <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-lg mx-4 max-h-[80vh] flex flex-col animate-panel-up">
        <!-- Header -->
        <div class="flex items-center justify-between px-5 py-3 border-b border-border">
          <h3 class="text-base font-semibold text-text">Browse Server</h3>
          <button onclick={() => open = false} class="text-text-muted hover:text-text p-1" aria-label="Close">
            <svg aria-hidden="true" class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
          </button>
        </div>

        <!-- Breadcrumbs -->
        <div class="px-5 py-2 border-b border-border/50 flex items-center gap-1 text-xs text-text-muted overflow-x-auto">
          <button onclick={() => goTo('')} class="hover:text-vault shrink-0">/mnt</button>
          {#each breadcrumbs as crumb (crumb.path)}
            <span class="text-text-dim">/</span>
            <button onclick={() => goTo(crumb.path)} class="hover:text-vault shrink-0">{crumb.name}</button>
          {/each}
        </div>

        <!-- Current path -->
        <div class="px-5 py-2 bg-surface-3/50">
          <div class="text-xs text-text-dim">Selected path</div>
          <div class="text-sm font-mono text-vault">{currentPath || '/mnt'}</div>
        </div>

        <!-- Directory listing -->
        <div class="flex-1 overflow-y-auto min-h-[200px] max-h-[400px]">
          {#if loading}
            <div class="flex items-center justify-center py-8 text-text-dim text-sm">Loading...</div>
          {:else if entries.length === 0}
            <div class="flex items-center justify-center py-8 text-text-dim text-sm">No subdirectories</div>
          {:else}
            <div class="divide-y divide-border/30">
              {#each entries as entry (entry.path)}
                <button
                  onclick={() => selectDir(entry)}
                  class="w-full flex items-center gap-3 px-5 py-2.5 text-left hover:bg-surface-3 transition-colors group"
                >
                  <svg aria-hidden="true" class="w-5 h-5 text-vault/70 group-hover:text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
                  <div class="min-w-0">
                    <div class="text-sm text-text group-hover:text-vault truncate">{entry.name}</div>
                    <div class="text-xs text-text-dim truncate">{entry.path}</div>
                  </div>
                </button>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Footer: select button -->
        <div class="flex justify-end gap-3 px-5 py-3 border-t border-border">
          <button onclick={() => open = false} class="px-4 py-2 text-sm text-text-muted hover:text-text rounded-lg">Cancel</button>
          <button onclick={confirm} class="px-4 py-2 text-sm font-medium bg-vault text-white rounded-lg hover:bg-vault/90 transition-colors">
            Select This Path
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>
