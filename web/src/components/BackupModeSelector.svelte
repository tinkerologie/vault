<script>
  /**
   * BackupModeSelector — radio card component for selecting backup mode.
   * Shows different options based on whether containers, VMs, or both are selected.
   *
   * Props:
   * - containerMode: 'one_by_one' | 'stop_all' (bindable)
   * - vmMode: 'snapshot' | 'cold' (bindable)
   * - hasContainers: boolean — whether the job includes containers
   * - hasVMs: boolean — whether the job includes VMs
   */
  let {
    containerMode = $bindable('one_by_one'),
    vmMode = $bindable('snapshot'),
    hasContainers = false,
    hasVMs = false,
  } = $props()

  import Tooltip from './Tooltip.svelte'

  const containerModes = [
    {
      value: 'one_by_one',
      label: 'Sequential',
      description: 'Stop, backup, then start each container individually. Minimizes downtime per container.',
      icon: 'M4 6h16M4 10h16M4 14h16M4 18h16',
    },
    {
      value: 'stop_all',
      label: 'Batch',
      description: 'Stop all containers, backup everything, then start all. Faster for many containers but more downtime.',
      icon: 'M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10',
    },
  ]

  const vmModes = [
    {
      value: 'snapshot',
      label: 'Live Snapshot',
      description: 'Create a snapshot while the VM is running. No downtime. QEMU guest agent is recommended for cleaner guest state, but not required.',
      icon: 'M3 9a2 2 0 012-2h.93a2 2 0 001.664-.89l.812-1.22A2 2 0 0110.07 4h3.86a2 2 0 011.664.89l.812 1.22A2 2 0 0018.07 7H19a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2V9z',
    },
    {
      value: 'cold',
      label: 'Cold Backup',
      description: 'Shut down the VM, copy disk files, then restart. Most reliable but causes downtime.',
      icon: 'M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z',
    },
  ]
</script>

<div class="space-y-5">
  {#if hasContainers}
    <div>
      <span class="block text-sm font-medium text-text mb-2">Container Backup Mode <Tooltip text="Sequential stops and backs up each container one at a time, minimising downtime per container. Batch stops all containers at once — faster overall but more total downtime." /></span>
      <div class="grid grid-cols-1 gap-2">
        {#each containerModes as mode (mode.value)}
          <button
            type="button"
            onclick={() => (containerMode = mode.value)}
            class="flex items-start gap-3 p-3 rounded-lg border-2 transition-all text-left {containerMode === mode.value
              ? 'border-vault bg-vault/5'
              : 'border-border hover:border-border-hover bg-surface-3/50'}"
          >
            <div
              class="w-5 h-5 mt-0.5 rounded-full border-2 flex items-center justify-center shrink-0 transition-colors {containerMode === mode.value
                ? 'border-vault'
                : 'border-border'}"
            >
              {#if containerMode === mode.value}
                <div class="w-2.5 h-2.5 rounded-full bg-vault"></div>
              {/if}
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <svg aria-hidden="true" class="w-4 h-4 text-text-muted shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                  ><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={mode.icon} /></svg
                >
                <span class="text-sm font-medium text-text">{mode.label}</span>
              </div>
              <p class="text-xs text-text-muted mt-1 leading-relaxed">{mode.description}</p>
            </div>
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if hasVMs}
    <div>
      <span class="block text-sm font-medium text-text mb-2">VM Backup Mode <Tooltip text="Live Snapshot creates a backup while the VM is running — no downtime. Cold Backup shuts the VM down first for maximum consistency but causes downtime." /></span>
      <div class="grid grid-cols-1 gap-2">
        {#each vmModes as mode (mode.value)}
          <button
            type="button"
            onclick={() => (vmMode = mode.value)}
            class="flex items-start gap-3 p-3 rounded-lg border-2 transition-all text-left {vmMode === mode.value
              ? 'border-vault bg-vault/5'
              : 'border-border hover:border-border-hover bg-surface-3/50'}"
          >
            <div
              class="w-5 h-5 mt-0.5 rounded-full border-2 flex items-center justify-center shrink-0 transition-colors {vmMode === mode.value
                ? 'border-vault'
                : 'border-border'}"
            >
              {#if vmMode === mode.value}
                <div class="w-2.5 h-2.5 rounded-full bg-vault"></div>
              {/if}
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <svg aria-hidden="true" class="w-4 h-4 text-text-muted shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"
                  ><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={mode.icon} /></svg
                >
                <span class="text-sm font-medium text-text">{mode.label}</span>
              </div>
              <p class="text-xs text-text-muted mt-1 leading-relaxed">{mode.description}</p>
            </div>
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if !hasContainers && !hasVMs}
    <p class="text-sm text-text-muted text-center py-4">Select containers or VMs first to configure backup mode</p>
  {/if}
</div>
