<script>
  import { onMount } from 'svelte'
  import { SvelteMap } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import Spinner from './Spinner.svelte'
  import PathBrowser from './PathBrowser.svelte'

  /** @type {{ items: Array<{item_type: string, item_name: string, item_id: string, settings: string, sort_order?: number}>, onchange?: (items: any[]) => void }} */
  let { items = $bindable([]), onchange = () => {} } = $props()

  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  let plugins = $state([])
  let containersAvailable = $state(false)
  let vmsAvailable = $state(false)
  let foldersAvailable = $state(false)
  let pluginsAvailable = $state(false)
  let loading = $state(true)
  let error = $state('')
  let search = $state('')
  let activeTab = $state('containers')
  let showAddFolder = $state(false)
  let customFolderPath = $state('')

  // Selected items tracked as map: "type:name" -> item
  let selected = $state(new SvelteMap())

  // Drag state for reorder
  let dragIndex = $state(-1)

  // Initialize selected from prop
  $effect(() => {
    if (items.length > 0 && selected.size === 0) {
      const m = new SvelteMap()
      for (const it of items) {
        m.set(`${it.item_type}:${it.item_name}`, it)
      }
      selected = m
    }
  })

  async function discover() {
    loading = true
    error = ''
    try {
      const [cRes, vRes, fRes, pluginRes] = await Promise.all([
        api.listContainers(),
        api.listVMs(),
        api.listFolders().catch(() => ({ items: [], available: true })),
        api.listPlugins().catch(() => ({ items: [], available: false })),
      ])
      containers = cRes.items || []
      containersAvailable = cRes.available
      vms = vRes.items || []
      vmsAvailable = vRes.available
      folders = fRes.items || []
      foldersAvailable = true
      plugins = pluginRes.items || []
      pluginsAvailable = pluginRes.available
      if (!containersAvailable && vmsAvailable) activeTab = 'vms'
      else if (!containersAvailable && !vmsAvailable) activeTab = 'folders'
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  // Discover on mount
  onMount(() => {
    discover()
  })

  function filteredContainers() {
    if (!search) return containers
    const q = search.toLowerCase()
    return containers.filter(
      (c) => c.name.toLowerCase().includes(q) || (c.settings?.image || '').toLowerCase().includes(q),
    )
  }

  function filteredVMs() {
    if (!search) return vms
    const q = search.toLowerCase()
    return vms.filter((v) => v.name.toLowerCase().includes(q))
  }

  let customFolders = $derived(folders.filter(f => f.settings?.preset !== 'flash'))
  let flashItems = $derived(folders.filter(f => f.settings?.preset === 'flash'))

  function filteredFolders() {
    if (!search) return customFolders
    const q = search.toLowerCase()
    return customFolders.filter((f) => f.name.toLowerCase().includes(q) || (f.settings?.path || '').toLowerCase().includes(q))
  }

  function filteredFlash() {
    if (!search) return flashItems
    const q = search.toLowerCase()
    return flashItems.filter((f) => f.name.toLowerCase().includes(q))
  }

  function filteredPlugins() {
    if (!search) return plugins
    const q = search.toLowerCase()
    return plugins.filter((p) => p.name.toLowerCase().includes(q))
  }

  function isSelected(type, name) {
    return selected.has(`${type}:${name}`)
  }

  function toggle(type, item) {
    const _key = `${type}:${item.name}`
    if (selected.has(_key)) {
      selected.delete(_key)
    } else {
      selected.set(_key, {
        item_type: type,
        item_name: item.name,
        item_id: item.settings?.id || item.name,
        settings: JSON.stringify(item.settings || {}),
      })
    }
    emitChange()
  }

  function selectAll(type) {
    let list, itemType = type
    if (type === 'flash') {
      list = filteredFlash()
      itemType = 'folder'
    } else if (type === 'container') {
      list = filteredContainers()
    } else if (type === 'vm') {
      list = filteredVMs()
    } else if (type === 'plugin') {
      list = filteredPlugins()
    } else {
      list = filteredFolders()
    }
    const allSelected = list.every((it) => selected.has(`${itemType}:${it.name}`))
    for (const it of list) {
      const key = `${itemType}:${it.name}`
      if (allSelected) {
        selected.delete(key)
      } else {
        selected.set(key, {
          item_type: itemType,
          item_name: it.name,
          item_id: it.settings?.id || it.name,
          settings: JSON.stringify(it.settings || {}),
        })
      }
    }
    emitChange()
  }

  function addCustomFolder() {
    if (!customFolderPath.trim()) return
    const name = customFolderPath.split('/').filter(Boolean).pop() || customFolderPath
    const fKey = `folder:${name}`
    selected.set(fKey, {
      item_type: 'folder',
      item_name: name,
      item_id: customFolderPath,
      settings: JSON.stringify({ path: customFolderPath, preset: '' }),
    })
    customFolderPath = ''
    showAddFolder = false
    emitChange()
  }

  function emitChange() {
    const arr = Array.from(selected.values()).map((it, i) => ({ ...it, sort_order: i }))
    items = arr
    onchange(arr)
  }

  function moveItem(fromIdx, toIdx) {
    const arr = Array.from(selected.entries())
    const [moved] = arr.splice(fromIdx, 1)
    arr.splice(toIdx, 0, moved)
    selected.clear()
    for (const [k, v] of arr) {
      selected.set(k, v)
    }
    emitChange()
  }

  let selectedCount = $derived(selected.size)
  let containerCount = $derived(
    Array.from(selected.values()).filter((i) => i.item_type === 'container').length,
  )
  let vmCount = $derived(Array.from(selected.values()).filter((i) => i.item_type === 'vm').length)
  function safeParseSettings(s) {
    if (!s) return {}
    try { return JSON.parse(s) } catch { return {} }
  }
  let folderCount = $derived(
    Array.from(selected.values()).filter((i) => i.item_type === 'folder' && safeParseSettings(i.settings).preset !== 'flash').length,
  )
  let flashCount = $derived(
    Array.from(selected.values()).filter((i) => i.item_type === 'folder' && safeParseSettings(i.settings).preset === 'flash').length,
  )
  let pluginCount = $derived(Array.from(selected.values()).filter((i) => i.item_type === 'plugin').length)
  let selectedArray = $derived(Array.from(selected.entries()))
</script>

<div class="space-y-3">
  {#if loading}
    <div class="flex items-center justify-center py-8">
      <Spinner size="md" />
      <span class="ml-2 text-sm text-text-muted">Discovering items...</span>
    </div>
  {:else if error}
    <div class="px-4 py-3 bg-red-500/10 border border-red-500/30 rounded-lg text-sm text-red-400">
      Failed to discover items: {error}
      <button onclick={discover} class="ml-2 underline hover:no-underline">Retry</button>
    </div>
  {:else}
    <!-- Tabs -->
    <div class="flex items-center gap-1 border-b border-border">
      {#if containersAvailable}
        <button
          type="button"
          onclick={() => (activeTab = 'containers')}
          class="px-4 py-2.5 text-sm font-medium border-b-2 transition-colors {activeTab === 'containers'
            ? 'border-vault text-vault'
            : 'border-transparent text-text-muted hover:text-text'}"
        >
          Containers
          {#if containerCount > 0}
            <span class="ml-1.5 px-1.5 py-0.5 bg-vault/20 text-vault text-xs rounded-full"
              >{containerCount}</span
            >
          {/if}
        </button>
      {/if}
      {#if vmsAvailable}
        <button
          type="button"
          onclick={() => (activeTab = 'vms')}
          class="px-4 py-2.5 text-sm font-medium border-b-2 transition-colors {activeTab === 'vms'
            ? 'border-vault text-vault'
            : 'border-transparent text-text-muted hover:text-text'}"
        >
          Virtual Machines
          {#if vmCount > 0}
            <span class="ml-1.5 px-1.5 py-0.5 bg-vault/20 text-vault text-xs rounded-full"
              >{vmCount}</span
            >
          {/if}
        </button>
      {/if}
      {#if foldersAvailable}
        <button
          type="button"
          onclick={() => (activeTab = 'folders')}
          class="px-4 py-2.5 text-sm font-medium border-b-2 transition-colors {activeTab === 'folders'
            ? 'border-vault text-vault'
            : 'border-transparent text-text-muted hover:text-text'}"
        >
          Folders
          {#if folderCount > 0}
            <span class="ml-1.5 px-1.5 py-0.5 bg-vault/20 text-vault text-xs rounded-full"
              >{folderCount}</span
            >
          {/if}
        </button>
      {/if}
      {#if flashItems.length > 0}
        <button
          type="button"
          onclick={() => (activeTab = 'flash')}
          class="px-4 py-2.5 text-sm font-medium border-b-2 transition-colors {activeTab === 'flash'
            ? 'border-vault text-vault'
            : 'border-transparent text-text-muted hover:text-text'}"
        >
          Flash Drive
          {#if flashCount > 0}
            <span class="ml-1.5 px-1.5 py-0.5 bg-vault/20 text-vault text-xs rounded-full"
              >{flashCount}</span
            >
          {/if}
        </button>
      {/if}
      {#if pluginsAvailable}
        <button
          type="button"
          onclick={() => (activeTab = 'plugins')}
          class="px-4 py-2.5 text-sm font-medium border-b-2 transition-colors {activeTab === 'plugins'
            ? 'border-vault text-vault'
            : 'border-transparent text-text-muted hover:text-text'}"
        >
          Plugins
          {#if pluginCount > 0}
            <span class="ml-1.5 px-1.5 py-0.5 bg-vault/20 text-vault text-xs rounded-full"
              >{pluginCount}</span
            >
          {/if}
        </button>
      {/if}
      <div class="flex-1"></div>
      <span class="text-xs text-text-muted pr-2">{selectedCount} selected</span>
    </div>

    <!-- Search -->
    <div class="relative">
      <svg
        class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-dim"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        ><path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
        /></svg
      >
      <input
        type="text"
        bind:value={search}
        placeholder="Search {activeTab === 'containers' ? 'containers' : activeTab === 'vms' ? 'VMs' : activeTab === 'plugins' ? 'plugins' : activeTab === 'flash' ? 'flash drive' : 'folders'}..."
        class="w-full bg-surface-3 border border-border rounded-lg pl-10 pr-3 py-2 text-sm text-text placeholder-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50"
      />
    </div>

    <!-- Item list -->
    {#if activeTab === 'containers' && containersAvailable}
      {@const filtered = filteredContainers()}
      {#if filtered.length > 0}
        <!-- Select all -->
        <button
          type="button"
          onclick={() => selectAll('container')}
          class="text-xs text-vault hover:text-vault/80 transition-colors"
        >
          {filtered.every((c) => isSelected('container', c.name)) ? 'Deselect all' : 'Select all'} ({filtered.length})
        </button>
        <div class="space-y-1 max-h-64 overflow-y-auto pr-1">
          {#each filtered as container (container.name)}
            {@const sel = isSelected('container', container.name)}
            <button
              type="button"
              onclick={() => toggle('container', container)}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all text-left {sel
                ? 'border-vault/50 bg-vault/5'
                : 'border-border hover:border-border-hover bg-surface-3/50'}"
            >
              <div
                class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors {sel
                  ? 'bg-vault border-vault'
                  : 'border-border'}"
              >
                {#if sel}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                    ><path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="3"
                      d="M5 13l4 4L19 7"
                    /></svg
                  >
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium text-text truncate">{container.name}</div>
                <div class="text-xs text-text-muted truncate">
                  {container.settings?.image || 'Unknown image'}
                </div>
              </div>
              <span
                class="text-xs px-2 py-0.5 rounded-full shrink-0 {container.settings?.state === 'running'
                  ? 'bg-green-500/15 text-green-400'
                  : 'bg-yellow-500/15 text-yellow-400'}"
              >
                {container.settings?.state || 'unknown'}
              </span>
            </button>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-text-muted py-4 text-center">
          {search ? 'No containers match your search' : 'No containers found'}
        </p>
      {/if}
    {:else if activeTab === 'vms' && vmsAvailable}
      {@const filtered = filteredVMs()}
      {#if filtered.length > 0}
        <button
          type="button"
          onclick={() => selectAll('vm')}
          class="text-xs text-vault hover:text-vault/80 transition-colors"
        >
          {filtered.every((v) => isSelected('vm', v.name)) ? 'Deselect all' : 'Select all'} ({filtered.length})
        </button>
        <div class="space-y-1 max-h-64 overflow-y-auto pr-1">
          {#each filtered as vm (vm.name)}
            {@const sel = isSelected('vm', vm.name)}
            <button
              type="button"
              onclick={() => toggle('vm', vm)}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all text-left {sel
                ? 'border-vault/50 bg-vault/5'
                : 'border-border hover:border-border-hover bg-surface-3/50'}"
            >
              <div
                class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors {sel
                  ? 'bg-vault border-vault'
                  : 'border-border'}"
              >
                {#if sel}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                    ><path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="3"
                      d="M5 13l4 4L19 7"
                    /></svg
                  >
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium text-text truncate">{vm.name}</div>
                <div class="text-xs text-text-muted">Virtual Machine</div>
              </div>
              <span
                class="text-xs px-2 py-0.5 rounded-full shrink-0 {vm.settings?.state === 'running'
                  ? 'bg-green-500/15 text-green-400'
                  : 'bg-gray-500/15 text-gray-400'}"
              >
                {vm.settings?.state || 'shutoff'}
              </span>
            </button>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-text-muted py-4 text-center">
          {search ? 'No VMs match your search' : 'No virtual machines found'}
        </p>
      {/if}
    {:else if activeTab === 'folders' && foldersAvailable}
      {@const filtered = filteredFolders()}
      <div class="flex items-center justify-between mb-1">
        {#if filtered.length > 0}
          <button
            type="button"
            onclick={() => selectAll('folder')}
            class="text-xs text-vault hover:text-vault/80 transition-colors"
          >
            {filtered.every((f) => isSelected('folder', f.name)) ? 'Deselect all' : 'Select all'} ({filtered.length})
          </button>
        {:else}
          <span></span>
        {/if}
        <button
          type="button"
          onclick={() => (showAddFolder = !showAddFolder)}
          class="text-xs text-vault hover:text-vault/80 transition-colors flex items-center gap-1"
        >
          <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
          Add Custom Folder
        </button>
      </div>
      {#if showAddFolder}
        <div class="bg-surface-3/50 border border-border rounded-lg p-3 mb-2 space-y-2">
          <p class="text-xs text-text-muted">Browse to a folder on the server to add it as a backup item.</p>
          <PathBrowser bind:value={customFolderPath} />
          <div class="flex justify-end gap-2">
            <button type="button" onclick={() => (showAddFolder = false)} class="px-3 py-1.5 text-xs text-text-muted hover:text-text bg-surface-3 rounded-lg">Cancel</button>
            <button type="button" onclick={addCustomFolder} disabled={!customFolderPath.trim()} class="px-3 py-1.5 text-xs text-white bg-vault hover:bg-vault-dark rounded-lg disabled:opacity-40">Add Folder</button>
          </div>
        </div>
      {/if}
      {#if filtered.length > 0}
        <div class="space-y-1 max-h-64 overflow-y-auto pr-1">
          {#each filtered as folder (folder.name)}
            {@const sel = isSelected('folder', folder.name)}
            <button
              type="button"
              onclick={() => toggle('folder', folder)}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all text-left {sel
                ? 'border-vault/50 bg-vault/5'
                : 'border-border hover:border-border-hover bg-surface-3/50'}"
            >
              <div
                class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors {sel
                  ? 'bg-vault border-vault'
                  : 'border-border'}"
              >
                {#if sel}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                    ><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7" /></svg
                  >
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium text-text truncate">{folder.name}</div>
                <div class="text-xs text-text-muted truncate">{folder.settings?.path || ''}</div>
              </div>
              {#if folder.settings?.preset}
                <span class="text-xs px-2 py-0.5 rounded-full shrink-0 bg-amber-500/15 text-amber-400">{folder.settings.preset}</span>
              {/if}
            </button>
          {/each}
        </div>
      {:else if !showAddFolder}
        <p class="text-sm text-text-muted py-4 text-center">
          {search ? 'No folders match your search' : 'No preset folders found. Add a custom folder above.'}
        </p>
      {/if}
    {:else if activeTab === 'flash'}
      {@const filtered = filteredFlash()}
      {#if filtered.length > 0}
        <button
          type="button"
          onclick={() => selectAll('flash')}
          class="text-xs text-vault hover:text-vault/80 transition-colors"
        >
          {filtered.every((f) => isSelected('folder', f.name)) ? 'Deselect all' : 'Select all'} ({filtered.length})
        </button>
        <div class="space-y-1 max-h-64 overflow-y-auto pr-1">
          {#each filtered as flash (flash.name)}
            {@const sel = isSelected('folder', flash.name)}
            <button
              type="button"
              onclick={() => toggle('folder', flash)}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all text-left {sel
                ? 'border-vault/50 bg-vault/5'
                : 'border-border hover:border-border-hover bg-surface-3/50'}"
            >
              <div
                class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors {sel
                  ? 'bg-vault border-vault'
                  : 'border-border'}"
              >
                {#if sel}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                    ><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7" /></svg
                  >
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium text-text truncate">{flash.name}</div>
                <div class="text-xs text-text-muted truncate">{flash.settings?.path || '/boot'}</div>
              </div>
              <span class="text-xs px-2 py-0.5 rounded-full shrink-0 bg-amber-500/15 text-amber-400">USB boot drive</span>
            </button>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-text-muted py-4 text-center">
          {search ? 'No flash drive matches your search' : 'Flash drive not detected'}
        </p>
      {/if}
    {:else if activeTab === 'plugins' && pluginsAvailable}
      {@const filtered = filteredPlugins()}
      {#if filtered.length > 0}
        <button
          type="button"
          onclick={() => selectAll('plugin')}
          class="text-xs text-vault hover:text-vault/80 transition-colors"
        >
          {filtered.every((p) => isSelected('plugin', p.name)) ? 'Deselect all' : 'Select all'} ({filtered.length})
        </button>
        <div class="space-y-1 max-h-64 overflow-y-auto pr-1">
          {#each filtered as plugin (plugin.name)}
            {@const sel = isSelected('plugin', plugin.name)}
            <button
              type="button"
              onclick={() => toggle('plugin', plugin)}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all text-left {sel
                ? 'border-vault/50 bg-vault/5'
                : 'border-border hover:border-border-hover bg-surface-3/50'}"
            >
              <div
                class="w-5 h-5 rounded border-2 flex items-center justify-center shrink-0 transition-colors {sel
                  ? 'bg-vault border-vault'
                  : 'border-border'}"
              >
                {#if sel}
                  <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                    ><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7" /></svg
                  >
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium text-text truncate">{plugin.name}</div>
                <div class="text-xs text-text-muted">Unraid Plugin</div>
              </div>
              {#if plugin.settings?.has_config}
                <span class="text-xs px-2 py-0.5 rounded-full shrink-0 bg-blue-500/15 text-blue-400">config</span>
              {/if}
            </button>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-text-muted py-4 text-center">
          {search ? 'No plugins match your search' : 'No plugins found'}
        </p>
      {/if}
    {:else}
      <p class="text-sm text-text-muted py-4 text-center">
        {activeTab === 'containers' ? 'Docker is not available' : activeTab === 'vms' ? 'libvirt is not available' : activeTab === 'plugins' ? 'Plugins not available' : activeTab === 'flash' ? 'Flash drive not available' : 'No folders available'}
      </p>
    {/if}

    <!-- Selected Items Order -->
    {#if selectedCount > 1}
      <div class="mt-4 pt-3 border-t border-border">
        <p class="text-xs font-medium text-text-muted mb-2">Backup Order (drag to reorder)</p>
        <div class="space-y-1" role="list">
          {#each selectedArray as [key, item], idx (key)}
            <div
              class="flex items-center gap-2 px-3 py-2 rounded-lg bg-surface-3/50 border border-border text-sm {dragIndex === idx ? 'opacity-50' : ''}"
              draggable="true"
              ondragstart={() => (dragIndex = idx)}
              ondragover={(e) => e.preventDefault()}
              ondrop={() => { if (dragIndex >= 0 && dragIndex !== idx) moveItem(dragIndex, idx); dragIndex = -1 }}
              ondragend={() => (dragIndex = -1)}
              role="listitem"
            >
              <svg aria-hidden="true" class="w-4 h-4 text-text-dim cursor-grab shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 8h16M4 16h16"/></svg>
              <span class="text-xs px-1.5 py-0.5 rounded bg-surface-4 text-text-dim shrink-0">{item.item_type}</span>
              <span class="text-text truncate">{item.item_name}</span>
              <div class="ml-auto flex items-center gap-1 shrink-0">
                <button
                  type="button"
                  onclick={() => { if (idx > 0) moveItem(idx, idx - 1) }}
                  disabled={idx === 0}
                  class="p-0.5 text-text-dim hover:text-text disabled:opacity-30"
                  aria-label="Move up"
                >
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"/></svg>
                </button>
                <button
                  type="button"
                  onclick={() => { if (idx < selectedArray.length - 1) moveItem(idx, idx + 1) }}
                  disabled={idx === selectedArray.length - 1}
                  class="p-0.5 text-text-dim hover:text-text disabled:opacity-30"
                  aria-label="Move down"
                >
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
                </button>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</div>
