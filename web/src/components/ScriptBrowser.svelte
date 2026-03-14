<script>
  import { api } from '../lib/api.js'

  let { value = $bindable(''), label = 'Script', placeholder = '/path/to/script.sh' } = $props()
  let inputId = $derived(`script-browser-${label.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`)

  let open = $state(false)
  let entries = $state([])
  let currentPath = $state('')
  let loading = $state(false)
  let breadcrumbs = $derived(
    currentPath
      ? currentPath.split('/').filter(Boolean).map((seg, i, arr) => ({
          name: seg,
          path: '/' + arr.slice(0, i + 1).join('/'),
        }))
      : []
  )

  async function browse(path = '') {
    loading = true
    try {
      const res = await api.browseFiles(path)
      entries = res.entries || []
      currentPath = res.path || ''
    } catch {
      entries = []
    }
    loading = false
  }

  function openBrowser() {
    open = true
    // If we have a value, browse to its parent directory.
    const startDir = value ? value.substring(0, value.lastIndexOf('/')) || '' : ''
    browse(startDir)
  }

  function selectEntry(entry) {
    if (entry.is_dir) {
      browse(entry.path)
    } else {
      value = entry.path
      open = false
    }
  }

  function goTo(path) {
    browse(path)
  }

  function clearValue() {
    value = ''
  }
</script>

<div class="space-y-1">
  <label for={inputId} class="block text-xs font-medium text-text-muted mb-1">{label}</label>
  <div class="flex gap-2">
    <div class="relative flex-1">
      <input
        id={inputId}
        type="text"
        bind:value
        {placeholder}
        class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text font-mono placeholder-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 pr-8"
      />
      {#if value}
        <button
          type="button"
          onclick={clearValue}
          class="absolute right-2 top-1/2 -translate-y-1/2 text-text-dim hover:text-text"
          aria-label="Clear"
        >
          <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
        </button>
      {/if}
    </div>
    <button
      type="button"
      onclick={openBrowser}
      class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-2 transition-colors"
      aria-label="Browse for script"
    >
      <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
    </button>
  </div>

  {#if open}
    <!-- eslint-disable-next-line -->
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) open = false }} onkeydown={() => {}}>
      <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-lg mx-4 max-h-[80vh] flex flex-col">
        <!-- Header -->
        <div class="flex items-center justify-between px-5 py-3 border-b border-border">
          <h3 class="text-base font-semibold text-text">Select Script</h3>
          <button onclick={() => open = false} class="text-text-muted hover:text-text p-1" aria-label="Close">
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
          </button>
        </div>

        <!-- Breadcrumbs -->
        <div class="px-5 py-2 border-b border-border/50 flex items-center gap-1 text-xs text-text-muted overflow-x-auto">
          <button onclick={() => goTo('')} class="hover:text-vault shrink-0">/</button>
          {#each breadcrumbs as crumb (crumb.path)}
            <span class="text-text-dim">/</span>
            <button onclick={() => goTo(crumb.path)} class="hover:text-vault shrink-0">{crumb.name}</button>
          {/each}
        </div>

        <!-- Directory listing -->
        <div class="flex-1 overflow-y-auto min-h-[200px] max-h-[400px]">
          {#if loading}
            <div class="flex items-center justify-center py-8 text-text-dim text-sm">Loading...</div>
          {:else if entries.length === 0}
            <div class="flex items-center justify-center py-8 text-text-dim text-sm">Empty directory</div>
          {:else}
            <div class="divide-y divide-border/30">
              {#each entries as entry (entry.path)}
                <button
                  onclick={() => selectEntry(entry)}
                  class="w-full flex items-center gap-3 px-5 py-2.5 text-left hover:bg-surface-3 transition-colors group"
                >
                  {#if entry.is_dir}
                    <svg class="w-5 h-5 text-vault/70 group-hover:text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
                  {:else}
                    <svg class="w-5 h-5 text-text-muted group-hover:text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
                  {/if}
                  <div class="min-w-0 flex-1">
                    <div class="text-sm text-text group-hover:text-vault truncate">{entry.name}</div>
                  </div>
                  {#if !entry.is_dir}
                    <span class="text-xs text-text-dim shrink-0">Select</span>
                  {/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Footer -->
        <div class="flex justify-end gap-3 px-5 py-3 border-t border-border">
          <button onclick={() => open = false} class="px-4 py-2 text-sm text-text-muted hover:text-text rounded-lg">Cancel</button>
        </div>
      </div>
    </div>
  {/if}
</div>
