<script>
  import { api } from '../lib/api.js'
  import { formatBytes, relTime } from '../lib/utils.js'

  let { destId, destName = '', onclose = () => {} } = $props()

  let entries = $state([])
  let currentPrefix = $state('')
  let loading = $state(true)
  let error = $state('')
  let breadcrumbs = $derived(
    currentPrefix
      ? currentPrefix.split('/').filter(Boolean)
          .map((seg, i, arr) => ({ name: seg, prefix: arr.slice(0, i + 1).join('/') }))
      : []
  )

  let _browseSeq = 0

  async function browse(prefix = '') {
    const seq = ++_browseSeq
    loading = true
    error = ''
    try {
      const res = await api.listStorageFiles(destId, prefix)
      if (seq !== _browseSeq) return // a newer navigation superseded this one
      // Directories first, then files, both alphabetical.
      entries = (res || []).slice().sort((a, b) =>
        (a.is_dir === b.is_dir) ? a.path.localeCompare(b.path) : (a.is_dir ? -1 : 1))
      currentPrefix = prefix
    } catch (e) {
      if (seq !== _browseSeq) return
      entries = []
      error = e.message
    }
    loading = false
  }

  function baseName(p) {
    const parts = p.split('/').filter(Boolean)
    return parts[parts.length - 1] || p
  }

  $effect(() => { if (destId) browse('') })
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm animate-backdrop" role="presentation">
  <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-2xl mx-4 max-h-[80vh] flex flex-col animate-panel-up">
    <div class="flex items-center justify-between px-5 py-3 border-b border-border">
      <h3 class="text-base font-semibold text-text">Browse {destName || 'destination'}</h3>
      <button onclick={onclose} class="text-text-muted hover:text-text p-1" aria-label="Close browser">
        <svg aria-hidden="true" class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
      </button>
    </div>

    <!-- Breadcrumbs -->
    <div class="flex items-center gap-1 px-5 py-2 border-b border-border text-sm overflow-x-auto whitespace-nowrap">
      <button onclick={() => browse('')} class="text-vault hover:underline shrink-0">root</button>
      {#each breadcrumbs as crumb (crumb.prefix)}
        <span class="text-text-dim shrink-0">/</span>
        <button onclick={() => browse(crumb.prefix)} class="text-vault hover:underline shrink-0">{crumb.name}</button>
      {/each}
    </div>

    <div class="flex-1 overflow-y-auto px-2 py-2">
      {#if loading}
        <p class="text-sm text-text-dim px-3 py-4">Loading…</p>
      {:else if error}
        <p class="text-sm text-danger px-3 py-4">{error}</p>
      {:else if entries.length === 0}
        <p class="text-sm text-text-dim px-3 py-4">This folder is empty.</p>
      {:else}
        {#each entries as entry (`${entry.is_dir ? 'd' : 'f'}:${entry.path}`)}
          {#if entry.is_dir}
            <button onclick={() => browse(entry.path)}
              class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text hover:bg-surface-3 transition-colors text-left">
              <svg aria-hidden="true" class="w-4 h-4 text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
              <span class="truncate">{baseName(entry.path)}</span>
            </button>
          {:else}
            <div class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-muted">
              <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
              <span class="truncate flex-1">{baseName(entry.path)}</span>
              <span class="text-xs text-text-dim tabular-nums shrink-0">{formatBytes(entry.size)}</span>
              {#if entry.mod_time}<span class="text-xs text-text-dim shrink-0">{relTime(entry.mod_time)}</span>{/if}
            </div>
          {/if}
        {/each}
      {/if}
    </div>

    <div class="px-5 py-2.5 border-t border-border">
      <p class="text-[11px] text-text-dim">Read-only view of the files Vault sees on this destination.</p>
    </div>
  </div>
</div>
