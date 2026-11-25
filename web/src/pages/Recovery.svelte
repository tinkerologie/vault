<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { formatBytes, formatDate, formatInt, relTime } from '../lib/utils.js'
  import { classifyProtection, protectedPercent } from '../lib/protection.js'
  import { SERVER_KEY_HREF } from '../lib/backupStatus.js'
  import Spinner from '../components/Spinner.svelte'
  import LoadError from '../components/LoadError.svelte'

  let loading = $state(true)
  let retrying = $state(false)
  let plan = $state(null)
  let error = $state('')
  // The health summary decides each item's protection, as on the Dashboard
  // (#708). Null when it failed: readiness is then unknown, never 100%.
  let summary = $state(null)
  let summaryFailed = $state(false)
  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  let available = $state({ containers: false, vms: false, folders: false })
  let expandedSteps = new SvelteSet()
  /** @type {Record<string, string>} */
  let settings = $state({})
  let keyAcknowledging = $state(false)
  let keyAckError = $state('')

  async function load() {
    try {
      const [p, cRes, vRes, fRes, sett, hSummary] = await Promise.all([
        api.getRecoveryPlan(),
        api.listContainers().catch(() => ({ items: [], available: false })),
        api.listVMs().catch(() => ({ items: [], available: false })),
        api.listFolders().catch(() => ({ items: [], available: false })),
        api.getSettings().catch(() => ({})),
        api.getHealthSummary().catch(() => null),
      ])
      plan = p
      containers = cRes.items || []
      vms = vRes.items || []
      folders = fRes.items || []
      available = { containers: cRes.available !== false, vms: vRes.available !== false, folders: fRes.available !== false }
      settings = sett || {}
      summary = hSummary
      summaryFailed = !hSummary
      error = ''
    } catch (e) {
      error = e.message || 'Failed to load the recovery plan'
    } finally {
      loading = false
    }
  }

  async function retry() {
    retrying = true
    try { await load() } finally { retrying = false }
  }

  onMount(load)

  const enabled = $derived({
    containers: settings.container_backup_enabled !== 'false',
    vms: settings.vm_backup_enabled !== 'false',
    folders: settings.folder_backup_enabled !== 'false',
    flash: settings.flash_backup_enabled !== 'false',
  })
  const protection = $derived(classifyProtection({ summary, discovered: { containers, vms, folders }, available, enabled }))
  const counts = $derived(protection.counts)
  const readinessPct = $derived(protectedPercent(counts))
  const allProtected = $derived(protection.known && counts.total > 0 && counts.protected === counts.total)
  const unprotectedItems = $derived(protection.items.filter(it => it.state === 'unprotected'))
  const pendingItems = $derived(protection.items.filter(it => it.state === 'pending'))
  const ringColor = $derived(!protection.known || counts.total === 0 ? 'var(--color-border)' : allProtected ? 'var(--color-success)' : readinessPct >= 50 ? 'var(--color-warning)' : 'var(--color-danger)')
  const serverKeyAtRisk = $derived(Boolean(summary?.server_key_at_risk))
  const needsAttention = $derived(protection.known && (counts.unprotected + counts.pending + counts.outOfDate > 0))

  const plural = (n, one, many = `${one}s`) => `${formatInt(n)} ${n === 1 ? one : many}`

  function readinessText() {
    if (!protection.known) return "Vault couldn't load protection status, so readiness is unknown."
    if (counts.total === 0) return 'No items to protect yet. Add containers, VMs, or folders to a backup job to get started.'
    if (allProtected) return 'All items are protected and up to date.'
    return `${formatInt(counts.protected)} of ${plural(counts.total, 'item')} protected and up to date.`
  }

  // vault.key is the only key to deduplicated backups (#714). Downloading it in
  // Settings clears this; so does confirming a copy is kept somewhere else.
  async function acknowledgeServerKey() {
    keyAcknowledging = true
    keyAckError = ''
    try {
      await api.updateSettings({ server_key_saved_at: new Date().toISOString() })
      summary = await api.getHealthSummary()
    } catch (e) {
      keyAckError = e.message || 'Could not save'
    } finally {
      keyAcknowledging = false
    }
  }

  function toggleStep(step) {
    if (expandedSteps.has(step)) expandedSteps.delete(step)
    else expandedSteps.add(step)
  }

  function stepItemState(item) {
    if (!item.has_restore_point) return 'pending'
    return item.out_of_date ? 'out_of_date' : 'protected'
  }

  const STEP_STATUS = {
    ready: { label: 'Ready', cls: 'text-success', path: 'M5 13l4 4L19 7' },
    warning: { label: 'Needs attention', cls: 'text-warning', path: 'M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z' },
    not_available: { label: 'Not available', cls: 'text-danger', path: 'M6 18L18 6M6 6l12 12' },
  }
  const stepStatus = (status) => STEP_STATUS[status] ?? STEP_STATUS.not_available
  const TYPE_TAGS = { container: 'Container', vm: 'VM', folder: 'Folder' }
</script>

<div>
  <div class="mb-8">
    <h1 class="text-2xl font-bold text-text">Disaster Recovery</h1>
    <p class="text-sm text-text-muted mt-1">Your disaster recovery plan – what to do if your server dies.</p>
  </div>

  <div class="bg-surface-2 border border-border rounded-xl p-4 mb-6 flex flex-col sm:flex-row sm:items-center gap-3">
    <div class="flex-1">
      <h2 class="text-sm font-medium text-text">Recovering after a server rebuild?</h2>
      <p class="text-xs text-text-muted">
        The Recover Vault wizard reconnects your backup storage and restores your settings step by step.
      </p>
    </div>
    <a href="#/recover" class="btn btn-primary shrink-0 text-center">Recover Vault</a>
  </div>

  {#if loading}
    <Spinner text="Loading recovery plan..." />
  {:else if error}
    <LoadError title="Couldn't load the recovery plan" message={error} onretry={retry} {retrying} />
  {:else if plan}
    <!-- Readiness Hero -->
    <div class="bg-surface-2 border border-border rounded-xl p-6 mb-8" data-readiness>
      <div class="flex items-center gap-6">
        <div class="relative w-20 h-20 shrink-0">
          <svg aria-hidden="true" viewBox="0 0 100 100" class="w-full h-full -rotate-90">
            <circle cx="50" cy="50" r="40" fill="none" stroke="var(--color-border)" stroke-width="8" />
            <circle cx="50" cy="50" r="40" fill="none"
              stroke={ringColor}
              stroke-width="8" stroke-linecap="round"
              stroke-dasharray={2 * Math.PI * 40} stroke-dashoffset={2 * Math.PI * 40 * (1 - (protection.known ? readinessPct : 0) / 100)}
              class="transition-all duration-1000" />
          </svg>
          <div class="absolute inset-0 flex items-center justify-center">
            <span class="text-lg font-bold text-text tabular-nums">{!protection.known || counts.total === 0 ? '—' : `${readinessPct}%`}</span>
          </div>
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-text">Recovery Readiness</h2>
          <p class="text-sm text-text-muted mt-1">{readinessText()}</p>
          {#if needsAttention}
            <p class="text-sm mt-1 flex flex-wrap gap-x-3 tabular-nums">
              {#if counts.outOfDate > 0}<span class="text-warning">{formatInt(counts.outOfDate)} out of date</span>{/if}
              {#if counts.pending > 0}<span class="text-info">{formatInt(counts.pending)} pending first backup</span>{/if}
              {#if counts.unprotected > 0}<span class="text-danger">{formatInt(counts.unprotected)} unprotected</span>{/if}
            </p>
          {/if}
          <p class="text-xs text-text-dim mt-1 tabular-nums">
            {plural(counts.total, 'item')} tracked · Vault v{plan.server_info?.vault_version || '?'}
          </p>
        </div>
      </div>
      {#if summaryFailed}
        <div class="mt-4"><LoadError compact title="Couldn't load protection status" onretry={retry} {retrying} /></div>
      {/if}
    </div>

    <!-- Checklist: what to fix before disaster strikes -->
    {#if serverKeyAtRisk || needsAttention || plan.warnings?.length > 0}
      <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-8" data-recovery-checklist>
        <h2 class="text-sm font-semibold text-warning mb-2">Before you need it</h2>
        <ul class="space-y-2">
          {#if serverKeyAtRisk}
            <li class="text-sm text-text" data-server-key-caution>
              <p class="font-medium">Save a copy of vault.key, or set a backup password</p>
              <p class="text-xs text-text-muted mt-0.5">No backup password is set, so vault.key is the only way to open your deduplicated backups, and it lives on the flash drive this server would lose.</p>
              <div class="flex flex-wrap items-center gap-3 mt-1.5">
                <a href="#{SERVER_KEY_HREF}" class="text-xs text-vault-text hover:underline font-medium">Download vault.key in Settings →</a>
                <button type="button" onclick={acknowledgeServerKey} disabled={keyAcknowledging} class="text-xs font-medium text-text-muted hover:text-text underline disabled:opacity-50">
                  {keyAcknowledging ? 'Saving…' : 'I keep a copy elsewhere'}
                </button>
                {#if keyAckError}<span class="text-xs text-danger" role="alert">{keyAckError}</span>{/if}
              </div>
            </li>
          {/if}
          {#if protection.known && counts.unprotected > 0}
            <li class="text-xs text-text-muted">• {plural(counts.unprotected, 'item is', 'items are')} not in any backup job</li>
          {/if}
          {#if protection.known && counts.pending > 0}
            <li class="text-xs text-text-muted">• {plural(counts.pending, 'item is', 'items are')} waiting for a first backup</li>
          {/if}
          {#if protection.known && counts.outOfDate > 0}
            <li class="text-xs text-text-muted">• {plural(counts.outOfDate, 'item is', 'items are')} out of date: the newest backup is older than the job's schedule allows</li>
          {/if}
          {#each (plan.warnings || []).slice(0, 10) as w, i (i)}
            <li class="text-xs text-text-muted">• {w}</li>
          {/each}
        </ul>
        {#if protection.known && counts.unprotected > 0}
          <a href="#/jobs" class="action-link mt-3 inline-block text-xs text-vault-text hover:underline font-medium">
            Configure backup jobs →
          </a>
        {/if}
      </div>
    {/if}

    <!-- Recovery Steps -->
    <div class="space-y-4">
      {#each (plan.steps || []) as step (step.step)}
        {@const status = stepStatus(step.status)}
        {@const expandable = step.items?.length > 0}
        {@const expanded = expandedSteps.has(step.step)}
        <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
          <!-- A native button inside the heading keeps the step in heading
               navigation and exposes its expanded state (#692). Its ::after
               stretches over the row, so the whole row still toggles. -->
          <div class="relative px-5 py-4 flex items-center gap-4">
            <div class="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold shrink-0 tabular-nums {step.status === 'ready' ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'}" aria-hidden="true">
              {step.step}
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2 flex-wrap">
                <h3 class="text-sm font-semibold text-text">
                  {#if expandable}
                    <button type="button" onclick={() => toggleStep(step.step)} aria-expanded={expanded} aria-controls="recovery-step-{step.step}"
                      class="text-left after:absolute after:inset-0 after:content-['']">
                      {step.title}
                    </button>
                  {:else}
                    {step.title}
                  {/if}
                </h3>
                <span class="inline-flex items-center gap-1 text-xs {status.cls}">
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d={status.path}/></svg>
                  {status.label}
                </span>
              </div>
              <p class="text-xs text-text-muted mt-0.5">{step.description}</p>
            </div>
            {#if step.total_size}
              <span class="text-xs text-text-dim shrink-0 tabular-nums">{formatBytes(step.total_size)}</span>
            {/if}
            {#if expandable}
              <svg aria-hidden="true" class="w-4 h-4 text-text-muted transition-transform shrink-0 {expanded ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
            {/if}
          </div>
          {#if expandable && expanded}
            <div id="recovery-step-{step.step}" class="px-5 pb-4 border-t border-border pt-3">
              <ul class="space-y-2">
                {#each step.items as item, i (i)}
                  {@const state = stepItemState(item)}
                  <!-- Meta wraps under the name on narrow screens, so a long
                       name is never squeezed to nothing (#697). -->
                  <li class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-2 bg-surface-3 rounded-lg" data-protection-state={state}>
                    <div class="flex items-center gap-2 min-w-0 flex-[1_1_12rem]">
                      <div class="w-2 h-2 rounded-full shrink-0 {state === 'protected' ? 'bg-success' : state === 'out_of_date' ? 'bg-warning' : 'bg-info'}" aria-hidden="true"></div>
                      <span class="text-sm text-text break-words min-w-0">{item.name}</span>
                    </div>
                    <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-text-dim tabular-nums">
                      {#if state === 'pending'}
                        <span class="text-info">Pending first backup</span>
                      {:else if state === 'out_of_date'}
                        <span class="text-warning" title={formatDate(item.last_backup)}>Out of date · last backup {relTime(item.last_backup)}</span>
                      {:else if item.last_backup}
                        <span>{formatDate(item.last_backup)}</span>
                      {/if}
                      {#if item.size_bytes}
                        <span>{formatBytes(item.size_bytes)}</span>
                      {/if}
                      <span class="text-text-muted break-all">{item.storage_name || '–'}</span>
                    </div>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    {#snippet itemList(title, description, items, dotClass, rowClass)}
      {#if items.length > 0}
        <div class="bg-surface-2 border border-border rounded-xl mt-8">
          <div class="px-5 py-4 border-b border-border">
            <h3 class="text-base font-semibold text-text">{title} <span class="text-sm font-normal text-text-dim tabular-nums">({formatInt(items.length)})</span></h3>
            <p class="text-xs text-text-muted mt-0.5">{description}</p>
          </div>
          <ul class="p-5 space-y-2">
            {#each items as it (it.key)}
              <li class="flex items-center gap-2 px-3 py-2 rounded-lg {rowClass}">
                <div class="w-2 h-2 rounded-full shrink-0 {dotClass}" aria-hidden="true"></div>
                <span class="text-sm text-text break-words min-w-0">{it.name}</span>
                <span class="text-xs text-text-muted ml-auto shrink-0">{it.group === 'flash' ? 'Flash drive' : TYPE_TAGS[it.type] ?? it.type}</span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
    {/snippet}

    <!-- Pending and unprotected are different problems, listed apart (#694). -->
    {@render itemList('Pending first backup', 'In a backup job, but not in any restore point yet. They are backed up on the next run.', pendingItems, 'bg-info', 'bg-surface-3')}
    {@render itemList('Unprotected Items', 'These items are not included in any backup job.', unprotectedItems, 'bg-danger', 'bg-danger/5')}
  {/if}
</div>
