<script>
  import { SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { formatBytes, relTime } from '../lib/utils.js'
  import InlineSpinner from './InlineSpinner.svelte'
  import Modal from './Modal.svelte'
  import { ICONS } from '../lib/icons.js'

  let { destId, destName = '', onclose = () => {}, ontoast = () => {} } = $props()

  const titleId = $props.id()
  let entries = $state([])
  let currentPrefix = $state('')
  let loading = $state(true)
  let error = $state('')
  const downloadingPaths = new SvelteSet()
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

  async function download(entry) {
    if (downloadingPaths.has(entry.path)) return
    downloadingPaths.add(entry.path)
    try {
      const blob = await api.downloadStorageFile(destId, entry.path)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = baseName(entry.path)
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) {
      ontoast(e?.message || 'Failed to download file', 'error')
    } finally {
      downloadingPaths.delete(entry.path)
    }
  }

  $effect(() => { if (destId) browse('') })
</script>

<Modal show size="lg" labelledby={titleId} {onclose}>
  <div class="max-h-[80vh] flex flex-col min-h-0">
    <div class="flex items-center justify-between px-5 py-3 border-b border-border">
      <h3 id={titleId} class="text-base font-semibold text-text">Browse {destName || 'destination'}</h3>
      <button type="button" onclick={() => onclose()} class="text-text-muted hover:text-text p-1" aria-label="Close browser">
        <svg aria-hidden="true" class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.close}/></svg>
      </button>
    </div>

    <!-- Breadcrumbs -->
    <div class="flex items-center gap-1 px-5 py-2 border-b border-border text-sm overflow-x-auto whitespace-nowrap">
      <button type="button" onclick={() => browse('')} class="text-vault-text hover:underline shrink-0">root</button>
      {#each breadcrumbs as crumb (crumb.prefix)}
        <span class="text-text-dim shrink-0">/</span>
        <button type="button" onclick={() => browse(crumb.prefix)} class="text-vault-text hover:underline shrink-0">{crumb.name}</button>
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
            <button type="button" onclick={() => browse(entry.path)}
              class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text hover:bg-surface-3 transition-colors text-left">
              <svg aria-hidden="true" class="w-4 h-4 text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={ICONS.folder}/></svg>
              <span class="truncate">{baseName(entry.path)}</span>
            </button>
          {:else}
            <div class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-muted hover:bg-surface-3/40 transition-colors">
              <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={ICONS.file}/></svg>
              <span class="truncate flex-1">{baseName(entry.path)}</span>
              <span class="text-xs text-text-dim tabular-nums shrink-0">{formatBytes(entry.size)}</span>
              {#if entry.mod_time}<span class="text-xs text-text-dim shrink-0">{relTime(entry.mod_time)}</span>{/if}
              <button
                type="button"
                onclick={() => download(entry)}
                disabled={downloadingPaths.has(entry.path)}
                class="p-1 text-text-muted hover:text-vault-text hover:bg-vault/10 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed shrink-0"
                title="Download file"
                aria-label={`Download ${baseName(entry.path)}`}
              >
                {#if downloadingPaths.has(entry.path)}
                  <InlineSpinner size="sm" />
                {:else}
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.download} /></svg>
                {/if}
              </button>
            </div>
          {/if}
        {/each}
      {/if}
    </div>

    <div class="px-5 py-2.5 border-t border-border">
      <p class="text-2xs text-text-dim">View and download files Vault sees on this destination.</p>
    </div>
  </div>
</Modal>
