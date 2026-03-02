<script>
  import { onMount } from 'svelte'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { formatDate, relTime, formatBytes, statusBadge, relTimeUntil } from '../lib/utils.js'
  import { getProgress, handleProgressMessage } from '../lib/progress.svelte.js'
  import Skeleton from '../components/Skeleton.svelte'
  import Toast from '../components/Toast.svelte'
  import Welcome from '../components/Welcome.svelte'
  import HealthGauge from '../components/HealthGauge.svelte'
  import ActivityTimeline from '../components/ActivityTimeline.svelte'
  import PullToRefresh from '../components/PullToRefresh.svelte'

  let loading = $state(true)
  let error = $state('')
  let health = $state(null)
  let jobs = $state([])
  let storage = $state([])
  let recentRuns = $state([])
  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  let protectedItems = $state(new Set())
  let runningJob = $state(null)
  let toast = $state({ message: '', type: 'info', key: 0 })
  let nextRuns = $state({})
  let healthSummary = $state(null)
  // Shared progress state (persists across page navigations)
  const progress = getProgress()

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  /** Human-readable item type label */
  function itemTypeLabel(type) {
    const map = { container: 'Container', vm: 'VM', folder: 'Folder', plugin: 'Plugin' }
    return map[type] || type
  }

  /** Icon color for item type */
  function itemTypeColor(type) {
    const map = { container: 'text-blue-400', vm: 'text-purple-400', folder: 'text-amber-400', plugin: 'text-emerald-400' }
    return map[type] || 'text-text-muted'
  }

  onMount(() => {
    loadDashboard()
    const unsub = onWsMessage((msg) => {
      const jobNameResolver = (id) => jobs.find(j => j.id === id)?.name
      const handled = handleProgressMessage(msg, jobNameResolver)
      if (msg.type === 'job_run_completed') {
        loadDashboard()
      }
    })
    return unsub
  })

  async function loadDashboard() {
    try {
      const [h, j, s, cRes, vRes, fRes, nextRunsData, hSummary] = await Promise.all([
        api.health(),
        api.listJobs(),
        api.listStorage(),
        api.listContainers().catch(() => ({ items: [], available: false })),
        api.listVMs().catch(() => ({ items: [], available: false })),
        api.listFolders().catch(() => ({ items: [], available: false })),
        api.getNextRuns().catch(() => ({})),
        api.getHealthSummary().catch(() => null),
      ])
      health = h
      jobs = j || []
      storage = s || []
      containers = cRes.items || []
      vms = vRes.items || []
      folders = fRes.items || []
      nextRuns = nextRunsData || {}
      healthSummary = hSummary

      const enabledJobs = jobs.filter(j => j.enabled)
      if (enabledJobs.length > 0) {
        const jobDetails = await Promise.all(
          enabledJobs.map(j => api.getJob(j.id).catch(() => null))
        )
        const pSet = new Set()
        for (const detail of jobDetails) {
          if (!detail?.items) continue
          for (const item of detail.items) {
            pSet.add(`${item.item_type}:${item.item_name}`)
          }
        }
        protectedItems = pSet
      }

      const runPromises = jobs.slice(0, 10).map(async (job) => {
        try {
          const runs = await api.getJobHistory(job.id, 5)
          return (runs || []).map(r => ({ ...r, jobName: job.name }))
        } catch { return [] }
      })
      const allRuns = await Promise.all(runPromises)
      recentRuns = allRuns.flat().sort((a, b) => new Date(b.started_at) - new Date(a.started_at)).slice(0, 10)
      error = ''
    } catch (e) {
      error = e.message || 'Failed to load dashboard'
    } finally {
      loading = false
    }
  }

  async function runNow(job) {
    runningJob = job.id
    try {
      await api.runJob(job.id)
      showToast(`"${job.name}" started`, 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      runningJob = null
    }
  }

  const enabledJobs = $derived(jobs.filter(j => j.enabled))
  const totalSize = $derived(recentRuns.reduce((sum, r) => sum + (r.size_bytes || 0), 0))

  const protectedContainers = $derived(containers.filter(c => protectedItems.has(`container:${c.name}`)))
  const unprotectedContainers = $derived(containers.filter(c => !protectedItems.has(`container:${c.name}`)))
  const protectedVMs = $derived(vms.filter(v => protectedItems.has(`vm:${v.name}`)))
  const unprotectedVMs = $derived(vms.filter(v => !protectedItems.has(`vm:${v.name}`)))
  const protectedFolders = $derived(folders.filter(f => protectedItems.has(`folder:${f.name}`)))

  const totalItems = $derived(containers.length + vms.length + folders.length)
  const totalProtected = $derived(protectedContainers.length + protectedVMs.length + protectedFolders.length)
  const protectionPct = $derived(totalItems > 0 ? Math.round((totalProtected / totalItems) * 100) : 0)

  const soonestNextRun = $derived(() => {
    const times = Object.values(nextRuns).map(t => new Date(t)).filter(d => !isNaN(d.getTime()))
    if (times.length === 0) return null
    return new Date(Math.min(...times.map(d => d.getTime()))).toISOString()
  })

  const healthSummaryText = $derived(() => {
    if (!healthSummary) return ''
    const s = healthSummary
    if (s.health_score >= 80) return 'All backups healthy'
    const issues = []
    if (s.total_items - s.protected_items > 0) issues.push(`${s.total_items - s.protected_items} items unprotected`)
    if (s.recent_failed > 0) issues.push(`${s.recent_failed} recent failures`)
    if (issues.length === 0) return 'Backups operational'
    if (s.health_score < 50) return 'Attention needed — ' + issues.join(', ')
    return issues.join(', ')
  })
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<PullToRefresh onrefresh={loadDashboard}>
<div>
  <div class="mb-8">
    <h1 class="text-2xl font-bold text-text">Dashboard</h1>
    <p class="text-sm text-text-muted mt-1">Overview of your backup system</p>
  </div>

  {#if loading}
    <Skeleton variant="stats" />
    <Skeleton variant="card" count={3} />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{error}</span>
    </div>
  {:else if !loading && storage.length === 0 && jobs.length === 0}
    <Welcome onstart={() => navigate('/storage')} />
  {:else}
    <!-- Getting Started Guide (shown when no storage or no jobs) -->
    {#if storage.length === 0 || jobs.length === 0}
      <div class="bg-surface-2 border border-vault/30 rounded-xl p-6 mb-8">
        <div class="flex items-start gap-4">
          <div class="w-10 h-10 rounded-lg bg-vault/10 flex items-center justify-center shrink-0 mt-0.5">
            <svg class="w-5 h-5 text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
          </div>
          <div class="flex-1 min-w-0">
            <h2 class="text-base font-semibold text-text">Getting Started</h2>
            <p class="text-sm text-text-muted mt-1">Set up your backup system in a few easy steps.</p>
            <div class="mt-4 flex flex-col sm:flex-row gap-3">
              <!-- Step 1: Storage -->
              <button
                onclick={() => navigate('/storage')}
                class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {storage.length > 0 ? 'border-success/30 bg-success/5' : 'border-vault/40 bg-vault/5 hover:bg-vault/10'}"
              >
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {storage.length > 0 ? 'bg-success text-white' : 'bg-vault text-white'}">
                  {#if storage.length > 0}
                    <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                  {:else}
                    1
                  {/if}
                </div>
                <div>
                  <p class="text-sm font-medium {storage.length > 0 ? 'text-success' : 'text-text'}">Configure Storage</p>
                  <p class="text-xs {storage.length > 0 ? 'text-success/70' : 'text-text-dim'}">
                    {storage.length > 0 ? `${storage.length} destination${storage.length !== 1 ? 's' : ''} configured` : 'Set up where backups are stored'}
                  </p>
                </div>
              </button>

              <!-- Step 2: Jobs -->
              <button
                onclick={() => navigate('/jobs')}
                disabled={storage.length === 0}
                class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {jobs.length > 0 ? 'border-success/30 bg-success/5' : storage.length > 0 ? 'border-vault/40 bg-vault/5 hover:bg-vault/10' : 'border-border bg-surface-3 opacity-50 cursor-not-allowed'}"
              >
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {jobs.length > 0 ? 'bg-success text-white' : storage.length > 0 ? 'bg-vault text-white' : 'bg-surface-4 text-text-dim'}">
                  {#if jobs.length > 0}
                    <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                  {:else}
                    2
                  {/if}
                </div>
                <div>
                  <p class="text-sm font-medium {jobs.length > 0 ? 'text-success' : storage.length > 0 ? 'text-text' : 'text-text-dim'}">Create Backup Job</p>
                  <p class="text-xs {jobs.length > 0 ? 'text-success/70' : 'text-text-dim'}">
                    {jobs.length > 0 ? `${jobs.length} job${jobs.length !== 1 ? 's' : ''} configured` : 'Choose what to back up and when'}
                  </p>
                </div>
              </button>

              <!-- Step 3: Run -->
              <div class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {jobs.length > 0 && storage.length > 0 ? 'border-vault/40 bg-vault/5' : 'border-border bg-surface-3 opacity-50'}">
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {jobs.length > 0 && storage.length > 0 ? 'bg-vault text-white' : 'bg-surface-4 text-text-dim'}">
                  3
                </div>
                <div>
                  <p class="text-sm font-medium {jobs.length > 0 && storage.length > 0 ? 'text-text' : 'text-text-dim'}">Run Backups</p>
                  <p class="text-xs text-text-dim">Jobs run on schedule or on demand</p>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    {/if}

    <!-- Health Gauge -->
    {#if healthSummary && jobs.length > 0}
      <HealthGauge score={healthSummary.health_score} summary={healthSummaryText()} />
    {/if}

    <!-- Stats Grid -->
    <div class="flex gap-4 overflow-x-auto pb-2 snap-x snap-mandatory lg:grid lg:grid-cols-5 lg:overflow-visible lg:pb-0 mb-8">
      <div class="bg-surface-2 border border-border rounded-xl p-5 snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-text-muted">Server</p>
            <p class="text-2xl font-bold mt-1 {health?.status === 'ok' ? 'text-success' : 'text-danger'}">
              {health?.status === 'ok' ? 'Online' : 'Offline'}
            </p>
          </div>
          <div class="w-10 h-10 rounded-lg bg-success/10 flex items-center justify-center">
            <svg class="w-5 h-5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
          </div>
        </div>
        <p class="text-xs text-text-dim mt-2">v{health?.version || '?'}</p>
      </div>

      <button onclick={() => navigate('/jobs')} class="bg-surface-2 border border-border rounded-xl p-5 text-left hover:border-vault/30 hover:shadow-sm transition-all cursor-pointer snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-text-muted">Jobs</p>
            <p class="text-2xl font-bold mt-1 text-text">{jobs.length}</p>
          </div>
          <div class="w-10 h-10 rounded-lg bg-vault/10 flex items-center justify-center">
            <svg class="w-5 h-5 text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"/></svg>
          </div>
        </div>
        <p class="text-xs text-text-dim mt-2">{enabledJobs.length} enabled</p>
        {#if soonestNextRun()}
          <p class="text-xs text-vault font-medium mt-1">Next: {relTimeUntil(soonestNextRun())}</p>
        {/if}
      </button>

      <div class="bg-surface-2 border border-border rounded-xl p-5 snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-text-muted">Containers</p>
            <p class="text-2xl font-bold mt-1 text-text">{containers.length}</p>
          </div>
          <div class="w-10 h-10 rounded-lg bg-blue-500/10 flex items-center justify-center">
            <svg class="w-5 h-5 text-blue-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4"/></svg>
          </div>
        </div>
        <p class="text-xs text-text-dim mt-2">Docker</p>
      </div>

      <div class="bg-surface-2 border border-border rounded-xl p-5 snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-text-muted">VMs</p>
            <p class="text-2xl font-bold mt-1 text-text">{vms.length}</p>
          </div>
          <div class="w-10 h-10 rounded-lg bg-purple-500/10 flex items-center justify-center">
            <svg class="w-5 h-5 text-purple-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/></svg>
          </div>
        </div>
        <p class="text-xs text-text-dim mt-2">libvirt</p>
      </div>

      <button onclick={() => navigate('/storage')} class="bg-surface-2 border border-border rounded-xl p-5 text-left hover:border-vault/30 hover:shadow-sm transition-all cursor-pointer snap-start min-w-[140px] flex-shrink-0 lg:min-w-0 lg:flex-shrink">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-text-muted">Storage</p>
            <p class="text-2xl font-bold mt-1 text-text">{storage.length}</p>
          </div>
          <div class="w-10 h-10 rounded-lg bg-info/10 flex items-center justify-center">
            <svg class="w-5 h-5 text-info" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"/></svg>
          </div>
        </div>
        <p class="text-xs text-text-dim mt-2">{storage.map(s => s.type).filter((v,i,a) => a.indexOf(v) === i).join(', ') || '—'}</p>
      </button>
    </div>

    <!-- Active Backup/Restore Progress -->
    {#if progress.activeRun}
      {@const overallPct = progress.overallTotal > 0 ? Math.round(((progress.overallDone + progress.overallFailed) / progress.overallTotal) * 100) : 0}
      {@const elapsedStr = progress.elapsedSec >= 3600 ? `${Math.floor(progress.elapsedSec / 3600)}h ${Math.floor((progress.elapsedSec % 3600) / 60)}m` : progress.elapsedSec >= 60 ? `${Math.floor(progress.elapsedSec / 60)}m ${progress.elapsedSec % 60}s` : `${progress.elapsedSec}s`}
      {@const progressItems = Object.entries(progress.itemProgress)}
      <div class="bg-surface-2 border border-vault/30 rounded-xl mb-8 overflow-hidden">
        <div class="px-5 py-4 border-b border-border flex items-center justify-between">
          <div class="flex items-center gap-3">
            <div class="w-2.5 h-2.5 rounded-full bg-vault animate-pulse"></div>
            <h2 class="text-base font-semibold text-text">Backup in Progress</h2>
            <span class="text-xs px-2.5 py-1 rounded-full bg-vault/15 text-vault font-medium">
              {progress.activeRun.job_name}
            </span>
          </div>
          <div class="flex items-center gap-4 text-xs text-text-dim">
            <span>{progress.overallDone}/{progress.overallTotal} items</span>
            {#if progress.overallFailed > 0}
              <span class="text-danger">{progress.overallFailed} failed</span>
            {/if}
            <span>{elapsedStr}</span>
          </div>
        </div>

        <!-- Overall progress bar -->
        <div class="px-5 pt-4">
          <div class="flex items-center justify-between mb-1.5">
            <span class="text-xs text-text-muted font-medium">Overall Progress</span>
            <span class="text-xs text-text-dim font-mono">{overallPct}%</span>
          </div>
          <div class="w-full h-2.5 bg-surface-4 rounded-full overflow-hidden">
            <div class="h-full rounded-full bg-vault transition-all duration-300 ease-out" style="width: {overallPct}%"></div>
          </div>
        </div>

        <!-- Per-item progress list -->
        <div class="p-5 space-y-3 max-h-80 overflow-y-auto">
          {#each progressItems as [name, info]}
            <div class="flex items-center gap-3">
              <!-- Status icon -->
              <div class="w-5 h-5 flex items-center justify-center shrink-0">
                {#if info.status === 'done'}
                  <svg class="w-4 h-4 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7"/></svg>
                {:else if info.status === 'failed'}
                  <svg class="w-4 h-4 text-danger" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
                {:else}
                  <svg class="w-4 h-4 text-vault animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                {/if}
              </div>

              <!-- Name + type -->
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium text-text truncate">{name}</span>
                  {#if info.item_type}
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-surface-4 {itemTypeColor(info.item_type)} font-medium shrink-0">{itemTypeLabel(info.item_type)}</span>
                  {/if}
                </div>
                <!-- Progress bar per item -->
                <div class="flex items-center gap-2 mt-1">
                  <div class="flex-1 h-1.5 bg-surface-4 rounded-full overflow-hidden">
                    <div
                      class="h-full rounded-full transition-all duration-300 ease-out {info.status === 'done' ? 'bg-success' : info.status === 'failed' ? 'bg-danger' : 'bg-vault'}"
                      style="width: {info.percent}%"
                    ></div>
                  </div>
                  <span class="text-[10px] text-text-dim font-mono w-8 text-right shrink-0">{info.percent}%</span>
                </div>
                <!-- Status message -->
                <p class="text-xs text-text-dim mt-0.5 truncate">{info.message}</p>
              </div>
            </div>
          {/each}
          {#if progressItems.length === 0}
            <p class="text-sm text-text-muted text-center py-2">Preparing backup...</p>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Protection Status -->
    {#if totalItems > 0}
      <div class="bg-surface-2 border border-border rounded-xl mb-8">
        <div class="px-5 py-4 border-b border-border flex items-center justify-between">
          <div class="flex items-center gap-3">
            <h2 class="text-base font-semibold text-text">Protection Status</h2>
            <span class="text-xs px-2.5 py-1 rounded-full font-medium {protectionPct === 100 ? 'bg-success/15 text-success' : protectionPct > 50 ? 'bg-warning/15 text-warning' : 'bg-danger/15 text-danger'}">
              {totalProtected}/{totalItems} protected ({protectionPct}%)
            </span>
          </div>
          {#if unprotectedContainers.length + unprotectedVMs.length > 0}
            <button onclick={() => navigate('/jobs')} class="text-xs text-vault hover:text-vault-dark transition-colors font-medium">
              + Add to Backup
            </button>
          {/if}
        </div>
        <div class="p-5">
          <!-- Progress bar -->
          <div class="w-full h-2 bg-surface-4 rounded-full overflow-hidden mb-5">
            <div class="h-full rounded-full transition-all duration-500 {protectionPct === 100 ? 'bg-success' : protectionPct > 50 ? 'bg-warning' : 'bg-danger'}" style="width: {protectionPct}%"></div>
          </div>

          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
            <!-- Containers -->
            {#if containers.length > 0}
              <div>
                <div class="flex items-center gap-2 mb-3">
                  <svg class="w-4 h-4 text-blue-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4"/></svg>
                  <h3 class="text-sm font-medium text-text">Containers</h3>
                  <span class="text-xs text-text-dim ml-auto">{protectedContainers.length}/{containers.length}</span>
                </div>
                <div class="space-y-1.5 max-h-48 overflow-y-auto">
                  {#each containers as c}
                    {@const isProtected = protectedItems.has(`container:${c.name}`)}
                    <div class="flex items-center gap-2.5 px-3 py-2 rounded-lg {isProtected ? 'bg-success/5' : 'bg-surface-3'} group">
                      <div class="w-2 h-2 rounded-full shrink-0 {isProtected ? 'bg-success' : 'bg-surface-5'}"></div>
                      <span class="text-sm text-text truncate">{c.name}</span>
                      {#if isProtected}
                        <button onclick={() => navigate(`/restore?type=container&name=${encodeURIComponent(c.name)}`)} class="ml-auto opacity-0 group-hover:opacity-100 p-1 text-vault hover:bg-vault/10 rounded transition-all" title="Restore {c.name}">
                          <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                        </button>
                        <svg class="w-3.5 h-3.5 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
                      {:else}
                        <span class="text-[10px] text-text-dim ml-auto">unprotected</span>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <!-- VMs -->
            {#if vms.length > 0}
              <div>
                <div class="flex items-center gap-2 mb-3">
                  <svg class="w-4 h-4 text-purple-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/></svg>
                  <h3 class="text-sm font-medium text-text">Virtual Machines</h3>
                  <span class="text-xs text-text-dim ml-auto">{protectedVMs.length}/{vms.length}</span>
                </div>
                <div class="space-y-1.5 max-h-48 overflow-y-auto">
                  {#each vms as v}
                    {@const isProtected = protectedItems.has(`vm:${v.name}`)}
                    <div class="flex items-center gap-2.5 px-3 py-2 rounded-lg {isProtected ? 'bg-success/5' : 'bg-surface-3'} group">
                      <div class="w-2 h-2 rounded-full shrink-0 {isProtected ? 'bg-success' : 'bg-surface-5'}"></div>
                      <span class="text-sm text-text truncate">{v.name}</span>
                      {#if isProtected}
                        <button onclick={() => navigate(`/restore?type=vm&name=${encodeURIComponent(v.name)}`)} class="ml-auto opacity-0 group-hover:opacity-100 p-1 text-vault hover:bg-vault/10 rounded transition-all" title="Restore {v.name}">
                          <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                        </button>
                        <svg class="w-3.5 h-3.5 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
                      {:else}
                        <span class="text-[10px] text-text-dim ml-auto">unprotected</span>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <!-- Folders -->
            {#if folders.length > 0}
              <div>
                <div class="flex items-center gap-2 mb-3">
                  <svg class="w-4 h-4 text-amber-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
                  <h3 class="text-sm font-medium text-text">Folders</h3>
                  <span class="text-xs text-text-dim ml-auto">{protectedFolders.length}/{folders.length}</span>
                </div>
                <div class="space-y-1.5 max-h-48 overflow-y-auto">
                  {#each folders as f}
                    {@const isProtected = protectedItems.has(`folder:${f.name}`)}
                    <div class="flex items-center gap-2.5 px-3 py-2 rounded-lg {isProtected ? 'bg-success/5' : 'bg-surface-3'} group">
                      <div class="w-2 h-2 rounded-full shrink-0 {isProtected ? 'bg-success' : 'bg-surface-5'}"></div>
                      <span class="text-sm text-text truncate">{f.name}</span>
                      {#if f.preset}
                        <span class="text-[9px] px-1.5 py-0.5 rounded bg-amber-500/15 text-amber-400 font-medium shrink-0">{f.preset}</span>
                      {/if}
                      {#if isProtected}
                        <button onclick={() => navigate(`/restore?type=folder&name=${encodeURIComponent(f.name)}`)} class="ml-auto opacity-0 group-hover:opacity-100 p-1 text-vault hover:bg-vault/10 rounded transition-all" title="Restore {f.name}">
                          <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
                        </button>
                        <svg class="w-3.5 h-3.5 text-success shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
                      {:else}
                        <span class="text-[10px] text-text-dim ml-auto">unprotected</span>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}

    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <!-- Activity Timeline -->
      <ActivityTimeline runs={recentRuns} maxItems={8} />

      <!-- Active Jobs with Run Now -->
      <div class="bg-surface-2 border border-border rounded-xl">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Backup Jobs</h2>
        </div>
        {#if jobs.length === 0}
          <div class="px-5 py-8 text-center text-sm text-text-muted">No backup jobs configured</div>
        {:else}
          <div class="divide-y divide-border">
            {#each jobs.slice(0, 5) as job}
              <div class="px-5 py-3 flex items-center justify-between">
                <div>
                  <p class="text-sm font-medium text-text">{job.name}</p>
                  <p class="text-xs text-text-dim">{job.enabled ? 'Enabled' : 'Disabled'} · {job.compression || 'none'}</p>
                </div>
                <button
                  onclick={() => runNow(job)}
                  disabled={runningJob === job.id}
                  class="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg font-medium transition-colors bg-vault/10 text-vault hover:bg-vault/20 disabled:opacity-50"
                >
                  {#if runningJob === job.id}
                    <svg class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                    Running...
                  {:else}
                    <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"/></svg>
                    Run Now
                  {/if}
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>
</PullToRefresh>
