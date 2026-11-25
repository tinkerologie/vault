<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import Spinner from './Spinner.svelte'
  import { ICONS, ITEM_TYPE_ICONS } from '../lib/icons.js'
  import { cardClass } from '../lib/selectable.js'

  let { selectedTypes = $bindable([]) } = $props()

  let counts = $state({ containers: 0, vms: 0, folders: 0, flash: 0, plugins: 0, zfs: 0 })
  let available = $state({ containers: false, vms: false, folders: true, flash: false, plugins: false, zfs: false })
  let loading = $state(true)

  const backupTypes = [
    {
      id: 'containers',
      label: 'Containers',
      description: 'Docker containers on this server',
      colorText: 'text-hue-blue',
      colorBadge: 'bg-hue-blue-bg/15 text-hue-blue',
      colorTile: 'bg-hue-blue-bg/15',
      icon: ITEM_TYPE_ICONS.container,
    },
    {
      id: 'vms',
      label: 'Virtual Machines',
      description: 'Libvirt VMs and their disk images',
      colorText: 'text-hue-purple',
      colorBadge: 'bg-hue-purple-bg/15 text-hue-purple',
      colorTile: 'bg-hue-purple-bg/15',
      icon: ITEM_TYPE_ICONS.vm,
    },
    {
      id: 'folders',
      label: 'Folders & Files',
      description: 'Custom paths on the array',
      colorText: 'text-hue-amber',
      colorBadge: 'bg-hue-amber-bg/15 text-hue-amber',
      colorTile: 'bg-hue-amber-bg/15',
      icon: ITEM_TYPE_ICONS.folder,
    },
    {
      id: 'flash',
      label: 'Flash Drive',
      description: 'Unraid USB boot drive configuration',
      colorText: 'text-hue-amber',
      colorBadge: 'bg-hue-amber-bg/15 text-hue-amber',
      colorTile: 'bg-hue-amber-bg/15',
      icon: ITEM_TYPE_ICONS.flash,
    },
    {
      id: 'plugins',
      label: 'Plugins',
      description: 'Installed Unraid plugin configs',
      colorText: 'text-hue-emerald',
      colorBadge: 'bg-hue-emerald-bg/15 text-hue-emerald',
      colorTile: 'bg-hue-emerald-bg/15',
      icon: ITEM_TYPE_ICONS.plugin,
    },
    {
      id: 'zfs',
      label: 'ZFS Datasets',
      description: 'ZFS datasets and volumes',
      colorText: 'text-hue-cyan',
      colorBadge: 'bg-hue-cyan-bg/15 text-hue-cyan',
      colorTile: 'bg-hue-cyan-bg/15',
      icon: ITEM_TYPE_ICONS.zfs,
    },
  ]

  function isSelected(typeId) {
    return selectedTypes.includes(typeId)
  }

  function toggle(typeId) {
    if (isSelected(typeId)) {
      selectedTypes = selectedTypes.filter(t => t !== typeId)
    } else {
      selectedTypes = [...selectedTypes, typeId]
    }
  }

  function getCount(typeId) {
    return counts[typeId] ?? 0
  }

  function getCountLabel(typeId, count) {
    const labels = {
      containers: 'container',
      vms: 'VM',
      folders: 'folder',
      flash: 'flash drive',
      plugins: 'plugin',
      zfs: 'dataset',
    }
    const label = labels[typeId] || typeId
    return `${count} ${label}${count !== 1 ? 's' : ''} detected`
  }

  function isAvailable(typeId) {
    return available[typeId] ?? false
  }

  onMount(async () => {
    try {
      const [cRes, vRes, fRes, pluginRes, zfsRes] = await Promise.all([
        api.listContainers().catch(() => ({ items: [], available: false })),
        api.listVMs().catch(() => ({ items: [], available: false })),
        api.listFolders().catch(() => ({ items: [], available: true })),
        api.listPlugins().catch(() => ({ items: [], available: false })),
        api.listZFSDatasets().catch(() => ({ items: [], available: false })),
      ])
      const allFolders = fRes.items || []
      const flashItems = allFolders.filter(f => f.settings?.preset === 'flash')
      const normalFolders = allFolders.filter(f => f.settings?.preset !== 'flash')

      counts = {
        containers: (cRes.items || []).length,
        vms: (vRes.items || []).length,
        folders: normalFolders.length,
        flash: flashItems.length,
        plugins: (pluginRes.items || []).length,
        zfs: (zfsRes.items || []).length,
      }
      available = {
        containers: cRes.available,
        vms: vRes.available,
        folders: true,
        flash: flashItems.length > 0,
        plugins: pluginRes.available,
        zfs: zfsRes.available,
      }
    } catch {
      // Discovery errors are non-fatal; cards still render with 0 counts
    } finally {
      loading = false
    }
  })
  const uid = $props.id()
</script>

{#if loading}
  <div class="flex items-center justify-center py-8">
    <Spinner size="md" />
    <span class="ml-2 text-sm text-text-muted">Discovering available items...</span>
  </div>
{:else}
  <div class="space-y-3">
    <p id="{uid}-hint" class="text-sm text-text-muted">Select one or more backup types.</p>
    <div role="group" aria-label="Backup types" aria-describedby="{uid}-hint" class="grid grid-cols-2 sm:grid-cols-3 gap-3">
      {#each backupTypes as type (type.id)}
        {@const sel = isSelected(type.id)}
        {@const count = getCount(type.id)}
        {@const avail = isAvailable(type.id)}
        <button
          type="button"
          onclick={() => toggle(type.id)}
          aria-pressed={sel}
          disabled={!avail && count === 0}
          class="relative flex flex-col items-start gap-2 p-4 rounded-xl border transition-colors text-left
            {cardClass(sel)}
            {!avail && count === 0 ? 'opacity-40 cursor-not-allowed' : 'cursor-pointer'}"
        >
          <div class="flex items-center gap-2.5">
            <div class="w-8 h-8 rounded-lg {sel ? type.colorTile : 'bg-surface-4/80'} flex items-center justify-center shrink-0">
              <svg aria-hidden="true" class="w-4.5 h-4.5 {sel ? type.colorText : 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={type.icon} />
              </svg>
            </div>
            <span class="text-sm font-semibold text-text">{type.label}</span>
          </div>
          <p class="text-xs text-text-muted leading-relaxed">{type.description}</p>
          {#if count > 0}
            <span class="text-xs {type.colorBadge} px-2 py-0.5 rounded-full">{getCountLabel(type.id, count)}</span>
          {:else if !avail}
            <span class="text-xs text-text-dim">Not available</span>
          {/if}
          {#if sel}
            <div class="absolute top-2 right-2">
              <svg aria-hidden="true" class="w-5 h-5 {type.colorText}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d={ICONS.check} />
              </svg>
            </div>
          {/if}
        </button>
      {/each}
    </div>
    {#if selectedTypes.length > 0}
      <p class="text-xs text-text-muted">
        {selectedTypes.length} type{selectedTypes.length !== 1 ? 's' : ''} selected
      </p>
    {/if}
  </div>
{/if}
