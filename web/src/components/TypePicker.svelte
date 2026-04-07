<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import Spinner from './Spinner.svelte'

  let { selectedTypes = $bindable([]) } = $props()

  let counts = $state({ containers: 0, vms: 0, folders: 0, flash: 0, plugins: 0, zfs: 0 })
  let available = $state({ containers: false, vms: false, folders: true, flash: false, plugins: false, zfs: false })
  let loading = $state(true)

  const backupTypes = [
    {
      id: 'containers',
      label: 'Containers',
      description: 'Docker containers on this server',
      colorBorder: 'border-blue-400',
      colorBg: 'bg-blue-400/5',
      colorText: 'text-blue-400',
      colorBadge: 'bg-blue-500/15 text-blue-400',
      icon: 'M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4',
    },
    {
      id: 'vms',
      label: 'Virtual Machines',
      description: 'Libvirt VMs and their disk images',
      colorBorder: 'border-purple-400',
      colorBg: 'bg-purple-400/5',
      colorText: 'text-purple-400',
      colorBadge: 'bg-purple-500/15 text-purple-400',
      icon: 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z',
    },
    {
      id: 'folders',
      label: 'Folders & Files',
      description: 'Custom paths on the array',
      colorBorder: 'border-amber-400',
      colorBg: 'bg-amber-400/5',
      colorText: 'text-amber-400',
      colorBadge: 'bg-amber-500/15 text-amber-400',
      icon: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
    },
    {
      id: 'flash',
      label: 'Flash Drive',
      description: 'Unraid USB boot drive configuration',
      colorBorder: 'border-amber-400',
      colorBg: 'bg-amber-400/5',
      colorText: 'text-amber-400',
      colorBadge: 'bg-amber-500/15 text-amber-400',
      icon: 'M8 7v8a2 2 0 002 2h6M8 7V5a2 2 0 012-2h4.586a1 1 0 01.707.293l4.414 4.414a1 1 0 01.293.707V15a2 2 0 01-2 2h-2M8 7H6a2 2 0 00-2 2v10a2 2 0 002 2h8a2 2 0 002-2v-2',
    },
    {
      id: 'plugins',
      label: 'Plugins',
      description: 'Installed Unraid plugin configs',
      colorBorder: 'border-emerald-400',
      colorBg: 'bg-emerald-400/5',
      colorText: 'text-emerald-400',
      colorBadge: 'bg-emerald-500/15 text-emerald-400',
      icon: 'M11 4a2 2 0 114 0v1a1 1 0 001 1h3a1 1 0 011 1v3a1 1 0 01-1 1h-1a2 2 0 100 4h1a1 1 0 011 1v3a1 1 0 01-1 1h-3a1 1 0 01-1-1v-1a2 2 0 10-4 0v1a1 1 0 01-1 1H7a1 1 0 01-1-1v-3a1 1 0 00-1-1H4a2 2 0 110-4h1a1 1 0 001-1V7a1 1 0 011-1h3a1 1 0 001-1V4z',
    },
    {
      id: 'zfs',
      label: 'ZFS Datasets',
      description: 'ZFS datasets and volumes',
      colorBorder: 'border-cyan-400',
      colorBg: 'bg-cyan-400/5',
      colorText: 'text-cyan-400',
      colorBadge: 'bg-cyan-500/15 text-cyan-400',
      icon: 'M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7M4 7c0 2.21 3.582 4 8 4s8-1.79 8-4M4 7c0-2.21 3.582-4 8-4s8 1.79 8 4m0 5c0 2.21-3.582 4-8 4s-8-1.79-8-4',
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
</script>

{#if loading}
  <div class="flex items-center justify-center py-8">
    <Spinner size="md" />
    <span class="ml-2 text-sm text-text-muted">Discovering available items...</span>
  </div>
{:else}
  <div class="space-y-3">
    <p class="text-sm text-text-muted">Select one or more backup types.</p>
    <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
      {#each backupTypes as type (type.id)}
        {@const sel = isSelected(type.id)}
        {@const count = getCount(type.id)}
        {@const avail = isAvailable(type.id)}
        <button
          type="button"
          onclick={() => toggle(type.id)}
          disabled={!avail && count === 0}
          class="relative flex flex-col items-start gap-2 p-4 rounded-xl border-2 transition-all text-left
            {sel ? `${type.colorBorder} ${type.colorBg}` : 'border-border hover:border-border-hover bg-surface-3/50'}
            {!avail && count === 0 ? 'opacity-40 cursor-not-allowed' : 'cursor-pointer'}"
        >
          <div class="flex items-center gap-2.5">
            <div class="w-8 h-8 rounded-lg {sel ? type.colorBg : 'bg-surface-4/80'} flex items-center justify-center shrink-0">
              <svg aria-hidden="true" class="w-4.5 h-4.5 {sel ? type.colorText : 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={type.icon} />
              </svg>
            </div>
            <span class="text-sm font-semibold {sel ? 'text-text' : 'text-text'}">{type.label}</span>
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
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7" />
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
