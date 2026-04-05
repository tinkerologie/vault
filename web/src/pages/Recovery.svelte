<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { formatBytes, formatDate } from '../lib/utils.js'
  import Spinner from '../components/Spinner.svelte'

  let loading = $state(true)
  let plan = $state(null)
  let error = $state('')
  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  let protectedItems = $state(new SvelteSet())
  let expandedSteps = $state(new SvelteSet())
  /** @type {Record<string, string>} */
  let settings = $state({})

  onMount(async () => {
    try {
      const [p, cRes, vRes, fRes, jobs, sett] = await Promise.all([
        api.getRecoveryPlan(),
        api.listContainers().catch(() => ({ items: [] })),
        api.listVMs().catch(() => ({ items: [] })),
        api.listFolders().catch(() => ({ items: [] })),
        api.listJobs(),
        api.getSettings().catch(() => ({})),
      ])
      plan = p
      containers = cRes.items || []
      vms = vRes.items || []
      folders = fRes.items || []
      settings = sett || {}

      // Compute protected items from enabled jobs.
      const enabledJobs = (jobs || []).filter(j => j.enabled)
      const jobDetails = await Promise.all(
        enabledJobs.map(j => api.getJob(j.id).catch(() => null))
      )
      const pSet = new SvelteSet()
      for (const detail of jobDetails) {
        if (!detail?.items) continue
        for (const item of detail.items) {
          pSet.add(`${item.item_type}:${item.item_name}`)
        }
      }
      protectedItems = pSet
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  })

  let containerBackupOn = $derived(settings.container_backup_enabled !== 'false')
  let vmBackupOn = $derived(settings.vm_backup_enabled !== 'false')
  let folderBackupOn = $derived(settings.folder_backup_enabled !== 'false')
  let flashBackupOn = $derived(settings.flash_backup_enabled !== 'false')

  let unprotectedContainers = $derived(containerBackupOn ? containers.filter(c => !protectedItems.has(`container:${c.name}`)) : [])
  let unprotectedVMs = $derived(vmBackupOn ? vms.filter(v => !protectedItems.has(`vm:${v.name}`)) : [])
  let trackedFolders = $derived(folderBackupOn ? folders.filter(f => f.settings?.preset !== 'flash') : [])
  let trackedFlash = $derived(flashBackupOn ? folders.filter(f => f.settings?.preset === 'flash') : [])
  let unprotectedFolders = $derived(trackedFolders.filter(f => !protectedItems.has(`folder:${f.name}`)))
  let unprotectedFlash = $derived(trackedFlash.filter(f => !protectedItems.has(`folder:${f.name}`)))
  let totalUnprotected = $derived(unprotectedContainers.length + unprotectedVMs.length + unprotectedFolders.length + unprotectedFlash.length)
  let trackedContainerCount = $derived(containerBackupOn ? containers.length : 0)
  let trackedVMCount = $derived(vmBackupOn ? vms.length : 0)
  let totalItems = $derived(trackedContainerCount + trackedVMCount + trackedFolders.length + trackedFlash.length)
  let readinessPct = $derived(totalItems > 0 ? Math.round(((totalItems - totalUnprotected) / totalItems) * 100) : 100)

  function toggleStep(step) {
    if (expandedSteps.has(step)) expandedSteps.delete(step)
    else expandedSteps.add(step)
  }

  function statusColor(status) {
    return status === 'ready' ? 'text-success' : status === 'warning' ? 'text-warning' : 'text-danger'
  }

  function statusIcon(status) {
    if (status === 'ready') return 'check'
    if (status === 'warning') return 'warning'
    return 'cross'
  }
</script>

<div>
  <div class="mb-8">
    <h1 class="text-2xl font-bold text-text">Recovery Guide</h1>
    <p class="text-sm text-text-muted mt-1">Your disaster recovery plan — what to do if your server dies.</p>
  </div>

  {#if loading}
    <Spinner text="Loading recovery plan..." />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4">
      <p class="text-sm">{error}</p>
    </div>
  {:else if plan}
    <!-- Readiness Hero -->
    <div class="bg-surface-2 border border-border rounded-xl p-6 mb-8">
      <div class="flex items-center gap-6">
        <div class="relative w-20 h-20 shrink-0">
          <svg aria-hidden="true" viewBox="0 0 100 100" class="w-full h-full -rotate-90">
            <circle cx="50" cy="50" r="40" fill="none" stroke="var(--color-border)" stroke-width="8" />
            <circle cx="50" cy="50" r="40" fill="none"
              stroke={readinessPct >= 80 ? 'var(--color-success)' : readinessPct >= 50 ? 'var(--color-warning)' : 'var(--color-danger)'}
              stroke-width="8" stroke-linecap="round"
              stroke-dasharray={2 * Math.PI * 40} stroke-dashoffset={2 * Math.PI * 40 * (1 - readinessPct / 100)}
              class="transition-all duration-1000" />
          </svg>
          <div class="absolute inset-0 flex items-center justify-center">
            <span class="text-lg font-bold text-text">{readinessPct}%</span>
          </div>
        </div>
        <div>
          <h2 class="text-lg font-semibold text-text">Recovery Readiness</h2>
          <p class="text-sm text-text-muted mt-1">
            {readinessPct === 100 ? 'All items are protected and backed up.' :
             readinessPct >= 80 ? 'Most items protected. Review warnings below.' :
             'Several items need attention.'}
          </p>
          <p class="text-xs text-text-dim mt-1">
            {plan.server_info?.total_protected_items || 0} items protected · Vault v{plan.server_info?.vault_version || '?'}
          </p>
        </div>
      </div>
    </div>

    <!-- Warnings -->
    {#if (plan.warnings?.length > 0) || totalUnprotected > 0}
      <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-8">
        <h3 class="text-sm font-semibold text-warning mb-2">Warnings</h3>
        <ul class="space-y-1">
          {#if totalUnprotected > 0}
            <li class="text-xs text-text-muted">• {totalUnprotected} item{totalUnprotected !== 1 ? 's' : ''} not included in any backup job</li>
          {/if}
          {#each (plan.warnings || []).slice(0, 10) as w, i (i)}
            <li class="text-xs text-text-muted">• {w}</li>
          {/each}
        </ul>
        {#if totalUnprotected > 0}
          <button onclick={() => navigate('/jobs')} class="mt-3 text-xs text-vault hover:text-vault-dark font-medium">
            Configure backup jobs →
          </button>
        {/if}
      </div>
    {/if}

    <!-- Recovery Steps -->
    <div class="space-y-4">
      {#each (plan.steps || []) as step (step.step)}
        <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
          <div class="px-5 py-4 flex items-center gap-4 cursor-pointer" role="button" tabindex="0" onclick={() => toggleStep(step.step)} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleStep(step.step) } }}>
            <div class="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold shrink-0 {step.status === 'ready' ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'}">
              {step.step}
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <h3 class="text-sm font-semibold text-text">{step.title}</h3>
                <span class="text-xs {statusColor(step.status)}">{#if statusIcon(step.status) === 'check'}<svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>{:else if statusIcon(step.status) === 'warning'}<svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/></svg>{:else}<svg aria-hidden="true" class="w-3 h-3 inline-block" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>{/if}</span>
              </div>
              <p class="text-xs text-text-muted mt-0.5">{step.description}</p>
            </div>
            {#if step.total_size}
              <span class="text-xs text-text-dim shrink-0">{formatBytes(step.total_size)}</span>
            {/if}
            <svg aria-hidden="true" class="w-4 h-4 text-text-muted transition-transform shrink-0 {expandedSteps.has(step.step) ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
          </div>
          {#if expandedSteps.has(step.step) && step.items?.length > 0}
            <div class="px-5 pb-4 border-t border-border pt-3">
              <div class="space-y-2">
                {#each step.items as item (item.name)}
                  <div class="flex items-center justify-between px-3 py-2 bg-surface-3 rounded-lg">
                    <div class="flex items-center gap-2 min-w-0">
                      <div class="w-2 h-2 rounded-full shrink-0 {item.has_restore_point ? 'bg-success' : 'bg-warning'}"></div>
                      <span class="text-sm text-text truncate">{item.name}</span>
                    </div>
                    <div class="flex items-center gap-3 text-xs text-text-dim shrink-0">
                      {#if item.last_backup}
                        <span>{formatDate(item.last_backup)}</span>
                      {:else}
                        <span class="text-warning">No backup</span>
                      {/if}
                      {#if item.size_bytes}
                        <span>{formatBytes(item.size_bytes)}</span>
                      {/if}
                      <span class="text-text-muted">{item.storage_name || '—'}</span>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- Unprotected Items -->
    {#if unprotectedContainers.length > 0 || unprotectedVMs.length > 0}
      <div class="bg-surface-2 border border-border rounded-xl mt-8">
        <div class="px-5 py-4 border-b border-border">
          <h3 class="text-base font-semibold text-text">Unprotected Items</h3>
          <p class="text-xs text-text-muted mt-0.5">These items are not included in any backup job.</p>
        </div>
        <div class="p-5 space-y-2">
          {#each unprotectedContainers as c (c.name)}
            <div class="flex items-center gap-2 px-3 py-2 bg-danger/5 rounded-lg">
              <div class="w-2 h-2 rounded-full bg-danger shrink-0"></div>
              <span class="text-sm text-text">{c.name}</span>
              <span class="text-[10px] text-text-dim ml-auto">container</span>
            </div>
          {/each}
          {#each unprotectedVMs as v (v.name)}
            <div class="flex items-center gap-2 px-3 py-2 bg-danger/5 rounded-lg">
              <div class="w-2 h-2 rounded-full bg-danger shrink-0"></div>
              <span class="text-sm text-text">{v.name}</span>
              <span class="text-[10px] text-text-dim ml-auto">vm</span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</div>
