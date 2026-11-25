<script>
  import { onMount } from 'svelte'
  import { navigate } from '../lib/router.svelte.js'
  import { api, isReplicaMode } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { relTime, relTimeUntil, formatSpeed, formatBytes, largestBackupsByJob, formatInt, formatDate, itemDisplayLabel } from '../lib/utils.js'
  import { getProgress, handleProgressMessage, restoreFromStatus, syncFromStatus } from '../lib/progress.svelte.js'
  import Skeleton from '../components/Skeleton.svelte'
  import ItemProgress from '../components/ItemProgress.svelte'
  import Toast from '../components/Toast.svelte'
  import Welcome from '../components/Welcome.svelte'
  import ComplianceBadge from '../components/ComplianceBadge.svelte'
  import StatusBanner from '../components/StatusBanner.svelte'
  import ActivityTimeline from '../components/ActivityTimeline.svelte'
  import PullToRefresh from '../components/PullToRefresh.svelte'
  import LoadError from '../components/LoadError.svelte'
  import { getAnomalyEnabled } from '../lib/settings.svelte.js'
  import { getAnomalies, setOpenList } from '../lib/anomalies.svelte.js'
  import { createDashboardLayout, SPAN_OPTIONS } from '../lib/dashboardLayout.svelte.js'
  import { attentionCaption, attentionHref, countRunOutcomes, deriveBackupStatus, healthText, healthTone, isDiscoveryIncomplete, mergeRunCounts, unhealthyStorage } from '../lib/backupStatus.js'
  import { classifyProtection, protectedPercent, protectionDot } from '../lib/protection.js'
  import { progressAnnouncement } from '../lib/progressAnnouncement.js'
  import { runStatusLabel } from '../lib/statusLabels.js'
  import InlineSpinner from '../components/InlineSpinner.svelte'
  import { ICONS, ITEM_TYPE_ICONS, PAGE_ICONS } from '../lib/icons.js'
  import { sizeTrendClass } from '../lib/outcome-colour.js'

  let loading = $state(true)
  let error = $state('')
  let health = $state(null)
  let jobs = $state([])
  let storage = $state([])
  let recentRuns = $state([])
  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  // Whether each discovery call succeeded. When one didn't, the job items in the
  // health summary stand in for that type (lib/protection.js).
  let discoveryAvailable = $state({ containers: false, vms: false, folders: false })
  let runningJob = $state(null)
  let runningAll = $state(false)
  let toast = $state({ message: '', type: 'info', key: 0 })
  let nextRuns = $state({})
  let healthSummary = $state(null)
  // A source the verdict relies on failed, so the page can't claim "healthy".
  let statusIncomplete = $state(false)
  let replicationSources = $state([])
  /** @type {Record<string, string>} */
  let settings = $state({})
  let liveSpeed = $state(null)
  let liveCumulativeBytes = $state(0)
  let liveStartTime = $state(null)
  // Shared progress state (persists across page navigations)
  const progress = getProgress()
  const anomalies = getAnomalies()

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  function fmtDur(s) {
    if (s == null || isNaN(s)) return ''
    if (s < 60) return `${Math.round(s)}s`
    const m = Math.floor(s / 60), ss = Math.round(s % 60)
    if (m < 60) return ss ? `${m}m ${ss}s` : `${m}m`
    const h = Math.floor(m / 60), mm = m % 60
    return mm ? `${h}h ${mm}m` : `${h}h`
  }

  // Debounce + in-flight guard for loadDashboard() triggered by burst-y
  // websocket events. Without this, rapid messages (e.g. a job that emits
  // many config_changed/run_started/run_completed in quick succession) can
  // queue up several overlapping fetches that each clobber the UI state.
  /** @type {ReturnType<typeof setTimeout> | null} */
  let dashboardReloadTimer = null
  let dashboardReloadInFlight = false
  let dashboardReloadPending = false

  function scheduleDashboardReload() {
    if (dashboardReloadTimer !== null) {
      clearTimeout(dashboardReloadTimer)
    }
    dashboardReloadTimer = setTimeout(() => {
      dashboardReloadTimer = null
      void runDashboardReload()
    }, 300)
  }

  async function runDashboardReload() {
    if (dashboardReloadInFlight) {
      // Coalesce: remember that another reload was requested while we were
      // mid-flight and run exactly one more after the current call returns.
      dashboardReloadPending = true
      return
    }
    dashboardReloadInFlight = true
    try {
      await loadDashboard()
    } finally {
      dashboardReloadInFlight = false
      if (dashboardReloadPending) {
        dashboardReloadPending = false
        scheduleDashboardReload()
      }
    }
  }

  onMount(() => {
    void runDashboardReload()
    // Re-pack tiles when any tile's measured height changes (data load, theme,
    // font) or the viewport resizes. Content height depends only on tile width
    // (fixed by its column span), never on the row position we assign — so
    // applying a position can't feed back into the measurement. No loop.
    compactObs = new ResizeObserver(() => scheduleCompact())
    for (const el of Object.values(tileEls)) compactObs.observe(el)
    const onResize = () => scheduleCompact()
    window.addEventListener('resize', onResize)
    // Restore progress overlay if a backup/restore is already running.
    api.getRunnerStatus().then(s => restoreFromStatus(s)).catch(() => {})
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'runner_status_snapshot') {
        syncFromStatus(msg.status)
      }
      const jobNameResolver = (id) => jobs.find(j => j.id === id)?.name
      handleProgressMessage(msg, jobNameResolver)
      if (msg.type === 'item_backup_done') {
        liveCumulativeBytes += msg.size_bytes || 0
        if (!liveStartTime) liveStartTime = Date.now()
        const elapsed = (Date.now() - liveStartTime) / 1000
        if (elapsed > 0) liveSpeed = formatSpeed(liveCumulativeBytes, elapsed)
      }
      if (msg.type === 'job_run_completed' || msg.type === 'job_run_started' || msg.type === 'import_completed') {
        liveSpeed = null
        liveCumulativeBytes = 0
        liveStartTime = null
        scheduleDashboardReload()
      }
      if (msg.type === 'config_changed' || msg.type === 'job_runs_acknowledged') {
        // Storage / job / replication CRUD changes the inputs to the
        // 3-2-1 compliance widget, the protection-status panel, and the
        // recovery plan. Re-fetch so derived UI stays current without a
        // page reload. Run acknowledgement also changes the health summary.
        scheduleDashboardReload()
      }
    })
    return () => {
      unsub()
      if (dashboardReloadTimer !== null) {
        clearTimeout(dashboardReloadTimer)
        dashboardReloadTimer = null
      }
      compactObs?.disconnect()
      window.removeEventListener('resize', onResize)
      if (compactRaf) window.cancelAnimationFrame(compactRaf)
    }
  })

  async function loadDashboard() {
    try {
      const [h, j, s, cRes, vRes, fRes, nextRunsData, hSummary, replSources, sett] = await Promise.all([
        api.health(),
        api.listJobs(),
        api.listStorage(),
        api.listContainers().catch(() => ({ items: [], available: false, failed: true })),
        api.listVMs().catch(() => ({ items: [], available: false, failed: true })),
        api.listFolders().catch(() => ({ items: [], available: false, failed: true })),
        api.getNextRuns().catch(() => ({})),
        api.getHealthSummary().catch(() => null),
        api.listReplicationSources().catch(() => []),
        api.getSettings().catch(() => ({})),
      ])
      health = h
      jobs = j || []
      storage = s || []
      containers = cRes.items || []
      vms = vRes.items || []
      folders = fRes.items || []
      discoveryAvailable = { containers: cRes.available !== false, vms: vRes.available !== false, folders: fRes.available !== false }
      nextRuns = nextRunsData || {}
      healthSummary = hSummary
      replicationSources = replSources || []
      settings = sett || {}

      // Feed the anomaly tile's count from the shared store (also kept live by
      // WS). Only when anomaly detection is enabled — otherwise the tile hides.
      if (getAnomalyEnabled()) loadAnomalyCount()

      let historyFailed = false
      const runPromises = jobs.slice(0, 10).map(async (job) => {
        try {
          const runs = await api.getJobHistory(job.id, 5)
          return (runs || []).map(r => ({ ...r, jobName: job.name }))
        } catch {
          historyFailed = true
          return []
        }
      })
      const allRuns = await Promise.all(runPromises)
      recentRuns = allRuns.flat().sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime()).slice(0, 10)
      // A replica never discovers items (api.js), so that isn't a gap there.
      statusIncomplete = historyFailed || (!isReplicaMode() && isDiscoveryIncomplete({ container: cRes, vm: vRes, folder: fRes }, hSummary))
      // Refresh the Largest-backups tile from a broad history window so it
      // reflects the true largest backup, not just the latest incremental.
      // Fire-and-forget so it never delays the rest of the dashboard.
      loadLargest()
      // Retention prune counts change whenever a backup runs or a policy edits,
      // both of which reload the dashboard — drop the cache so the $effect below
      // re-fetches instead of showing stale prunable counts until remount.
      retentionSummary = null
      retentionError = false
      error = ''
    } catch (e) {
      error = e.message || 'Failed to load dashboard'
    } finally {
      loading = false
    }
  }

  // A failed count is unknown, never a green 0 (#681).
  let anomaliesError = $state(false)
  let anomaliesRetrying = $state(false)
  async function loadAnomalyCount() {
    try {
      const r = await api.listAnomalies({ state: 'open', limit: 100 })
      setOpenList(r?.anomalies ?? [])
      anomaliesError = false
    } catch {
      anomaliesError = true
    }
  }
  async function retryAnomalies() {
    anomaliesRetrying = true
    try { await loadAnomalyCount() } finally { anomaliesRetrying = false }
  }

  async function cancelActiveRun() {
    const run = progress.activeRun
    if (!run) return
    try {
      await api.cancelJob(run.job_id)
      showToast(`Cancelling "${run.job_name}"…`, 'info')
    } catch (e) {
      showToast(e.message, 'error')
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

  async function runAll() {
    if (runningAll || enabledJobs.length === 0) return
    runningAll = true
    try {
      let failed = 0
      for (const j of enabledJobs) {
        await api.runJob(j.id).catch(() => { failed++ })
      }
      const started = enabledJobs.length - failed
      if (failed === 0) {
        showToast(`Started ${started} enabled job${started === 1 ? '' : 's'}`, 'success')
      } else if (started === 0) {
        showToast(`Could not start ${failed} job${failed === 1 ? '' : 's'}`, 'error')
      } else {
        showToast(`Started ${started} of ${enabledJobs.length} jobs · ${failed} failed`, 'warning')
      }
    } finally {
      runningAll = false
    }
  }

  const enabledJobs = $derived(jobs.filter(j => j.enabled))

  const containerBackupOn = $derived(settings.container_backup_enabled !== 'false')
  const vmBackupOn = $derived(settings.vm_backup_enabled !== 'false')
  const folderBackupOn = $derived(settings.folder_backup_enabled !== 'false')
  const flashBackupOn = $derived(settings.flash_backup_enabled !== 'false')
  const backupRuleOn = $derived(settings.backup_rule_enabled !== 'false')

  // Dismiss (×) on the 3-2-1 panel: hide immediately, persist, and revert on
  // failure. Re-enabled from Settings → Dashboard.
  async function dismissBackupRule() {
    const original = settings.backup_rule_enabled
    settings = { ...settings, backup_rule_enabled: 'false' }
    try {
      await api.updateSettings({ backup_rule_enabled: 'false' })
      showToast('3-2-1 Backup Rule hidden – re-enable in Settings → Dashboard')
    } catch (e) {
      settings = { ...settings, backup_rule_enabled: original }
      showToast(e.message || 'Could not hide the panel', 'error')
    }
  }

  // Persist the chosen 3-2-1 goal (optimistic, revert on failure). When unset,
  // the panel auto-detects the goal from the current setup.
  async function setBackupRuleGoal(goal) {
    const original = settings.backup_rule_goal
    settings = { ...settings, backup_rule_goal: goal }
    try {
      await api.updateSettings({ backup_rule_goal: goal })
    } catch (e) {
      settings = { ...settings, backup_rule_goal: original }
      showToast(e.message || 'Could not save backup goal', 'error')
    }
  }

  // One classification shared with Disaster Recovery, so both pages count the
  // same items the same way (#708): protected, out of date, pending first
  // backup, or unprotected. Without the health summary every item is unknown.
  const protection = $derived(classifyProtection({
    summary: healthSummary,
    discovered: { containers, vms, folders },
    available: discoveryAvailable,
    enabled: { containers: containerBackupOn, vms: vmBackupOn, folders: folderBackupOn, flash: flashBackupOn },
  }))
  const protectionCounts = $derived(protection.counts)
  const unprotectedCount = $derived(protectionCounts.unprotected)
  const outOfDateCount = $derived(protectionCounts.outOfDate)
  const pendingCount = $derived(protectionCounts.pending)
  // Any unprotected item (not yet in a backup, of any type) → show the CTA.
  const hasUnprotectedItems = $derived(unprotectedCount > 0)

  const totalItems = $derived(protectionCounts.total)
  const totalProtected = $derived(protectionCounts.protected)
  const protectionPct = $derived(protectedPercent(protectionCounts))
  // Collapse uses exact membership, not the rounded percentage: 199/200 rounds
  // to 100% but still has an item that must stay visible.
  const fullyProtected = $derived(protection.known && totalItems > 0 && totalProtected === totalItems)
  // Bar goes green only when every item is truly protected and up to date, and
  // stays neutral while protection is unknown.
  const protectionBar = $derived(!protection.known ? 'bg-surface-5' : fullyProtected ? 'bg-success' : protectionPct >= 50 ? 'bg-warning' : 'bg-danger')
  const protectionText = $derived(!protection.known ? 'text-text-dim' : fullyProtected ? 'text-success' : protectionPct >= 50 ? 'text-warning' : 'text-danger')

  // Collapse the Protection Status panel when everything is covered (issue #211
  // / E7). Below 100% the panel is always expanded so unprotected/pending items
  // stay visible; at 100% it collapses by default and the user's manual choice
  // persists in localStorage.
  const PROTECTION_EXPANDED_KEY = 'vault:dash:protectionExpanded'
  let protectionExpandedPref = $state(readProtectionPref())
  function readProtectionPref() {
    try {
      const v = localStorage.getItem(PROTECTION_EXPANDED_KEY)
      return v === null ? null : v === 'true'
    } catch { return null }
  }
  const protectionExpanded = $derived(!fullyProtected ? true : (protectionExpandedPref ?? false))
  function toggleProtection() {
    const next = !protectionExpanded
    protectionExpandedPref = next
    try { localStorage.setItem(PROTECTION_EXPANDED_KEY, String(next)) } catch { /* ignore */ }
  }

  const soonestNextRun = $derived.by(() => {
    const times = Object.values(nextRuns).map(t => new Date(t)).filter(d => !isNaN(d.getTime()))
    if (times.length === 0) return null
    return new Date(Math.min(...times.map(d => d.getTime()))).toISOString()
  })

  const soonestJob = $derived.by(() => {
    let best = null, bestT = Infinity
    for (const [jid, t] of Object.entries(nextRuns)) {
      const ms = new Date(t).getTime()
      if (!isNaN(ms) && ms < bestT) { bestT = ms; best = jid }
    }
    if (best == null) return null
    return jobs.find(j => String(j.id) === String(best)) || null
  })

  const avgSpeed = $derived.by(() => {
    const completed = recentRuns.filter(r => (r.status === 'completed' || r.status === 'success') && r.size_bytes && r.duration_seconds);
    if (!completed.length) return null;
    const totalBytes = completed.reduce((s, r) => s + r.size_bytes, 0);
    const totalSecs = completed.reduce((s, r) => s + r.duration_seconds, 0);
    return formatSpeed(totalBytes, totalSecs);
  })

  // Most recent completed/failed backup (not restore) for the Last-backup tile.
  const lastBackup = $derived(recentRuns.find(r => (r.run_type || 'backup') === 'backup' && r.status !== 'running') || null)

  // Recent backup success rate from the runs we already loaded (not a full 30d
  // window — that would need a history endpoint this page doesn't fetch).
  const successStats = $derived.by(() => {
    const runs = recentRuns.filter(r => (r.run_type || 'backup') === 'backup' && r.status !== 'running')
    if (!runs.length) return null
    const ok = runs.filter(r => r.status === 'completed' || r.status === 'success').length
    return { pct: Math.round((ok / runs.length) * 100), ok, total: runs.length }
  })

  // recentRuns only samples ten jobs, so what the server counted across every job
  // must still count: no panel may contradict the health tile.
  const runOutcomes = $derived(mergeRunCounts(countRunOutcomes(recentRuns), healthSummary))
  const recentFailures = $derived(runOutcomes.failures)
  const unhealthyDests = $derived(unhealthyStorage(storage))
  // Without the health summary nothing is known to be protected, so every item
  // would look unprotected. That's unknown, not a count of problems.
  const knownUnprotected = $derived(healthSummary ? unprotectedCount : 0)
  const serverKeyAtRisk = $derived(Boolean(healthSummary?.server_key_at_risk) && !isReplicaMode())
  const otherProblems = $derived(runOutcomes.warnings + runOutcomes.skipped + runOutcomes.cancelled + unhealthyDests.length + (healthSummary?.stale_jobs?.length ?? 0) + (serverKeyAtRisk ? 1 : 0))
  const attentionCount = $derived(recentFailures + knownUnprotected + outOfDateCount + otherProblems)
  // A replica discovers nothing, so its counts come from the summary's job items.
  const statusCounts = $derived({ pending: pendingCount, protectedCount: totalProtected + outOfDateCount, total: totalItems })

  const backupStatus = $derived(deriveBackupStatus({
    loading,
    error: !!error,
    replica: isReplicaMode(),
    hasStorage: storage.length > 0,
    hasJobs: jobs.length > 0,
    failures: recentFailures,
    warnings: runOutcomes.warnings,
    skipped: runOutcomes.skipped,
    cancelled: runOutcomes.cancelled,
    unhealthy: unhealthyDests,
    unprotected: unprotectedCount,
    outOfDate: outOfDateCount,
    ...statusCounts,
    summary: healthSummary,
    incomplete: statusIncomplete,
  }))

  // The progress card changes every second, so it is not a live region. Only a
  // start, a change of phase and the finish are announced (#686). A run already
  // under way on arrival is read in place, not announced.
  const progressNote = $derived(progressAnnouncement({
    activeRun: progress.activeRun, running: progress.running, phaseMessage: progress.phaseMessage, failed: progress.overallFailed,
  }))
  let progressAnnouncementText = $state('')
  let lastProgressKey
  let progressSeen = false
  $effect(() => {
    const key = progressNote?.key ?? null
    if (progressSeen && key !== null && key !== lastProgressKey) progressAnnouncementText = progressNote.text
    progressSeen = true
    lastProgressKey = key
  })

  // Storage capacity tiles derive from destinations that reported a probe.
  const storageCaps = $derived(storage.filter(s => s.capacity && s.capacity.total_bytes > 0))
  const storageCombined = $derived.by(() => {
    if (!storageCaps.length) return null
    const used = storageCaps.reduce((a, s) => a + (s.capacity.used_bytes || 0), 0)
    const total = storageCaps.reduce((a, s) => a + (s.capacity.total_bytes || 0), 0)
    return { used, total, pct: total > 0 ? Math.round((used / total) * 100) : 0, count: storageCaps.length }
  })

  // Largest backup per job. Sourced from a broad history window (not the
  // short recentRuns slice) so an older full backup is not hidden once
  // smaller incrementals become the most recent runs. Populated by
  // loadLargest() during the dashboard load.
  let largestBackups = $state([])
  // Guards against a slow reload's response overwriting a newer one: each
  // call claims a generation and only the latest applies its result.
  let largestLoadGeneration = 0
  async function loadLargest() {
    const generation = ++largestLoadGeneration
    try {
      const history = await api.getHistory(200)
      const nameByJob = new Map(jobs.map(j => [j.id, j.name]))
      if (generation === largestLoadGeneration) {
        largestBackups = largestBackupsByJob(history, nameByJob)
      }
    } catch { /* keep previous value on failure */ }
  }

  // ── Lazily-loaded data for optional tiles (default-off, so fetched only when
  // the user adds the tile). Each guards against duplicate in-flight loads. ──
  /** @type {{ points: Array<{start: string, total_bytes: number}> } | null} */
  let trendData = $state(null)
  let trendLoading = $state(false)
  // A failed optional load says so with a Retry instead of an empty state
  // (#681), and stops the effect below from re-requesting it in a loop.
  let trendError = $state(false)
  async function loadTrend() {
    if (trendLoading || trendData || trendError) return
    trendLoading = true
    try { trendData = await api.getHistoryTrend('30d') } catch { trendError = true } finally { trendLoading = false }
  }
  function retryTrend() { trendError = false; loadTrend() }

  /** @type {{ ratio: number, logical: number, physical: number } | null} */
  let dedupSummary = $state(null)
  let dedupLoading = $state(false)
  let dedupError = $state(false)
  async function loadDedup() {
    if (dedupLoading || dedupSummary || dedupError) return
    dedupLoading = true
    try {
      const dests = storage.filter(s => s.dedup_enabled)
      const stats = await Promise.all(dests.map(d => api.dedupStats(d.id).catch(() => null)))
      // Partial figures would understate the savings, so any failure is an error.
      if (stats.some(st => st === null)) { dedupError = true; return }
      let logical = 0, physical = 0
      for (const st of stats) { logical += st.logical_bytes || 0; physical += st.physical_bytes || 0 }
      dedupSummary = physical > 0 ? { ratio: logical / physical, logical, physical } : null
    } catch { dedupError = true } finally { dedupLoading = false }
  }
  function retryDedup() { dedupError = false; loadDedup() }

  /** @type {{ name: string, days: number, perDay: number } | null} */
  let forecastSummary = $state(null)
  let forecastLoading = $state(false)
  let forecastError = $state(false)
  async function loadForecast() {
    if (forecastLoading || forecastSummary || forecastError) return
    forecastLoading = true
    try {
      const dests = storage.filter(s => s.capacity && s.capacity.total_bytes > 0)
      const trajectories = await Promise.all(dests.map(d => api.getCapacityTrajectory(d.id).then(t => ({ d, t })).catch(() => null)))
      // A destination we couldn't read might be the one filling up first.
      if (trajectories.some(entry => entry === null)) { forecastError = true; return }
      let soonest = null
      for (const entry of trajectories) {
        if (!entry) continue
        const samples = (entry.t?.samples || []).filter(sm => sm.free_bytes != null)
        if (samples.length < 2) continue
        const first = samples[0], last = samples[samples.length - 1]
        const days = (new Date(last.sampled_at).getTime() - new Date(first.sampled_at).getTime()) / 86400000
        if (days <= 0) continue
        const perDay = (first.free_bytes - last.free_bytes) / days // bytes consumed/day
        if (perDay <= 0) continue // not filling
        const daysToFull = last.free_bytes / perDay
        if (!soonest || daysToFull < soonest.days) soonest = { name: entry.d.name, days: Math.round(daysToFull), perDay }
      }
      forecastSummary = soonest
    } catch { forecastError = true } finally { forecastLoading = false }
  }
  function retryForecast() { forecastError = false; loadForecast() }

  // Jobs with any Long-Term Retention policy set — these get pruned on their next
  // run, so they drive the Retention tile. keep_* come with the jobs list.
  const jobsWithRetention = $derived(jobs.filter(j =>
    (j.keep_latest || 0) + (j.keep_daily || 0) + (j.keep_weekly || 0) + (j.keep_monthly || 0) + (j.keep_yearly || 0) > 0
  ))

  // Earliest next run among policied jobs only — the actual next prune. (The
  // global soonestNextRun could be a job with no retention policy.)
  const soonestRetentionNextRun = $derived.by(() => {
    let best = Infinity
    for (const job of jobsWithRetention) {
      const t = nextRuns[job.id]
      if (!t) continue
      const ms = new Date(t).getTime()
      if (!isNaN(ms) && ms < best) best = ms
    }
    return best === Infinity ? null : new Date(best).toISOString()
  })

  /** @type {{ prunable: number, total: number } | null} */
  let retentionSummary = $state(null)
  let retentionLoading = $state(false)
  let retentionError = $state(false)
  async function loadRetention() {
    if (retentionLoading || retentionSummary || retentionError) return
    retentionLoading = true
    try {
      // Ask each policied job what its own policy would prune right now. The
      // server returns restore-point IDs (no byte sizes), so we surface counts —
      // there's no reclaimable-bytes source to honestly show.
      const results = await Promise.all(jobsWithRetention.map(j =>
        api.getRetentionPreview(j.id, {
          keep_latest: j.keep_latest, keep_daily: j.keep_daily, keep_weekly: j.keep_weekly,
          keep_monthly: j.keep_monthly, keep_yearly: j.keep_yearly,
        }).catch(() => null)))
      if (results.some(r => r === null)) { retentionError = true; return }
      let prunable = 0, total = 0
      for (const r of results) { prunable += r.would_delete?.length || 0; total += r.total_restore_points || 0 }
      retentionSummary = { prunable, total }
    } catch { retentionError = true } finally { retentionLoading = false }
  }
  function retryRetention() { retentionError = false; loadRetention() }

  // Offsite replication rollup for the Replication-status tile. replicationSources
  // is already loaded; no separate fetch. No lag field exists server-side, so we
  // surface sync state + last-synced only.
  const replicationSummary = $derived.by(() => {
    const srcs = replicationSources || []
    if (!srcs.length) return null
    // Health is judged over enabled sources only — a disabled target is off by
    // choice and must not block an "all synced" state. Mirror the Replication
    // page's status buckets (success / error|failed / partial).
    const enabled = srcs.filter(s => s.enabled)
    const synced = enabled.filter(s => s.last_sync_status === 'success').length
    const failed = enabled.filter(s => s.last_sync_status === 'error' || s.last_sync_status === 'failed').length
    const partial = enabled.filter(s => s.last_sync_status === 'partial').length
    // Scope "last synced" to enabled sources too, so a disabled source's stale
    // timestamp can't outrank the newest active replication.
    const times = enabled.map(s => s.last_sync_at).filter(Boolean).map(t => new Date(t).getTime()).filter(n => !isNaN(n))
    const lastSync = times.length ? new Date(Math.max(...times)).toISOString() : null
    return { total: srcs.length, enabled: enabled.length, synced, failed, partial, lastSync }
  })

  // Trigger the lazy loaders whenever a tile that needs their data is present.
  $effect(() => {
    if ((layout.order.includes('sizeTrend') || layout.order.includes('calendar')) && !trendData) loadTrend()
    if (layout.order.includes('savings') && !dedupSummary) loadDedup()
    if (layout.order.includes('forecast') && !forecastSummary) loadForecast()
    if (layout.order.includes('retention') && !retentionSummary && jobsWithRetention.length) loadRetention()
  })

  const trendChange = $derived.by(() => {
    const pts = trendData?.points?.filter(p => p.total_bytes > 0) || []
    if (pts.length < 2) return null
    const first = pts[0].total_bytes, last = pts[pts.length - 1].total_bytes
    return { first, last, pctChange: first > 0 ? Math.round(((last - first) / first) * 100) : 0 }
  })

  // SVG polyline points for the size-trend sparkline (normalized to 300×64).
  const trendPolyline = $derived.by(() => {
    const pts = (trendData?.points || []).map(p => p.total_bytes)
    if (pts.length < 2) return ''
    const max = Math.max(...pts, 1)
    return pts.map((v, i) => `${(i / (pts.length - 1) * 300).toFixed(1)},${(64 - (v / max) * 58).toFixed(1)}`).join(' ')
  })

  // Contribution-graph heatmap: the last 5 weeks aligned Sun→Sat so each grid
  // column is a whole week. Cells are coloured by that day's backup size.
  const calendarGrid = $derived.by(() => {
    if (!trendData?.points?.length) return null
    const ymd = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const byDay = {}
    let max = 1
    for (const p of trendData.points) {
      const key = (p.start || '').slice(0, 10)
      if (!key) continue
      byDay[key] = (byDay[key] || 0) + (p.total_bytes || 0)
      if (byDay[key] > max) max = byDay[key]
    }
    const DAY = 86400000
    const nowD = new Date(Date.now())
    const todayMs = new Date(nowD.getFullYear(), nowD.getMonth(), nowD.getDate()).getTime()
    const endMs = todayMs + (6 - new Date(todayMs).getDay()) * DAY // Saturday of this week
    const days = []
    for (let i = 34; i >= 0; i--) {
      const dMs = endMs - i * DAY
      const d = new Date(dMs)
      const key = ymd(d)
      const bytes = byDay[key] || 0
      days.push({ key, bytes, intensity: bytes / max, future: dMs > todayMs, label: d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) })
    }
    return { days, max }
  })

  function calCellStyle(d) {
    if (d.future) return 'background: var(--color-surface-3); opacity: 0.35;'
    if (!d.bytes) return 'background: var(--color-surface-4);'
    const pct = Math.round(35 + d.intensity * 65)
    return `background: color-mix(in srgb, var(--color-success) ${pct}%, transparent);`
  }

  // Summary stats shown beside the heatmap so the wide tile isn't mostly empty.
  const calendarStats = $derived.by(() => {
    if (!calendarGrid) return null
    const past = calendarGrid.days.filter(d => !d.future)
    const ran = past.filter(d => d.bytes > 0).length
    // Current streak: consecutive days with a backup, counting back from the
    // most recent past day.
    let streak = 0
    for (let i = past.length - 1; i >= 0; i--) {
      if (past[i].bytes > 0) streak++
      else break
    }
    const total = past.reduce((a, d) => a + d.bytes, 0)
    return { ran, days: past.length, streak, total }
  })

  const healthScore = $derived(healthSummary?.health_score ?? 0)
  const healthCounts = $derived({ failures: recentFailures, unprotected: knownUnprotected, outOfDate: outOfDateCount, pending: statusCounts.pending, warnings: runOutcomes.warnings, skipped: runOutcomes.skipped, cancelled: runOutcomes.cancelled, unhealthy: unhealthyDests.length, incomplete: statusIncomplete })
  const HEALTH_COLORS = { success: 'var(--color-success)', warning: 'var(--color-warning)', danger: 'var(--color-danger)', unknown: 'var(--color-surface-4)' }
  const healthColor = $derived(HEALTH_COLORS[healthTone(healthSummary, healthCounts)])
  const healthSummaryText = $derived(healthText(healthSummary, healthCounts))


  // ── Tile catalog + layout ────────────────────────────────────────────────
  // span = 12-col width. `bare` tiles render their own card (they reuse a
  // self-carding component/panel); the rest get the shared card shell.
  const CATALOG = {
    health:       { name: 'Job health',         span: 3, maxSpan: 6, icon: 'M4.318 6.318a4.5 4.5 0 000 6.364L12 20.364l7.682-7.682a4.5 4.5 0 00-6.364-6.364L12 7.636l-1.318-1.318a4.5 4.5 0 00-6.364 0z' },
    protected:    { name: 'Protected items',    span: 3, maxSpan: 6, icon: ICONS.shield },
    nextrun:      { name: 'Next run',           span: 3, maxSpan: 6, icon: ICONS.clock },
    lastbackup:   { name: 'Last backup',        span: 3, maxSpan: 6, icon: 'M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z' },
    threetwoone:  { name: '3-2-1 rule',         span: 12, bare: true, icon: 'M9 17V9m3 8v-5m3 5v-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z' },
    progress:     { name: 'Backup in progress', span: 6, bare: true, icon: ICONS.refresh },
    activity:     { name: 'Recent activity',    span: 6, bare: true, icon: 'M4 6h16M4 10h16M4 14h16M4 18h16' },
    jobs:         { name: 'Backup jobs',        span: 6, bare: true, icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2' },
    protection:   { name: 'Protection status',  span: 12, bare: true, icon: ICONS.shield },
    storageCombined:  { name: 'Storage — combined',  span: 4, maxSpan: 6, icon: PAGE_ICONS['/storage'] },
    storagePerTarget: { name: 'Storage — per target', span: 6, icon: PAGE_ICONS['/storage'] },
    recovery:     { name: 'Recovery readiness', span: 4, maxSpan: 6, icon: ICONS.shield },
    attention:    { name: 'Needs attention',    span: 4, maxSpan: 6, icon: ICONS.warning },
    successrate:  { name: 'Success rate',       span: 4, maxSpan: 6, icon: 'M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z' },
    anomalies:    { name: 'Anomalies',          span: 4, maxSpan: 6, icon: ICONS.warning },
    quickactions: { name: 'Quick actions',      span: 4, maxSpan: 6, icon: ICONS.bolt },
    sizeTrend:    { name: 'Backup size trend',  span: 6, icon: 'M13 7h8m0 0v8m0-8l-8 8-4-4-6 6' },
    calendar:     { name: 'Backup calendar',    span: 4, icon: 'M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z' },
    savings:      { name: 'Dedup & compression', span: 4, maxSpan: 6, icon: 'M19 14l-7 7m0 0l-7-7m7 7V3' },
    forecast:     { name: 'Storage forecast',   span: 4, maxSpan: 6, icon: ICONS.clock },
    largest:      { name: 'Largest backups',    span: 6, icon: 'M3 4h18M3 4v16M7 20V10m5 10V6m5 14v-8' },
    retention:    { name: 'Retention & cleanup', span: 4, maxSpan: 6, icon: ICONS.trash },
    replicationStatus: { name: 'Replication status', span: 4, maxSpan: 6, icon: ICONS.refresh },
  }

  const layout = createDashboardLayout(Object.keys(CATALOG))

  // A tile is hidden in normal view (but still listed in edit mode) when it
  // can't be computed from current data — so we never render a broken tile.
  function tileAvailable(id) {
    if (id === 'threetwoone') return jobs.length > 0 && backupRuleOn
    // Only while a run is active, including its short completion grace. Customize
    // bypasses this, so the tile keeps its saved place and can still be arranged.
    if (id === 'progress') return Boolean(progress.activeRun)
    if (id === 'storageCombined' || id === 'storagePerTarget') return storageCaps.length > 0
    if (id === 'anomalies') return getAnomalyEnabled()
    if (id === 'protection') return totalItems > 0
    if (id === 'savings') return storage.some(s => s.dedup_enabled)
    if (id === 'largest') return largestBackups.length > 0
    if (id === 'retention') return jobsWithRetention.length > 0
    if (id === 'replicationStatus') return replicationSources.length > 0
    // sizeTrend / calendar / forecast render their own loading/empty state, so
    // they stay visible while data loads or if the series is thin.
    return true
  }

  // During setup (no storage or no first job) only the at-a-glance KPI tiles are
  // meaningful; the heavier panels (jobs, protection, activity, analytics) would
  // all render empty, so we hold them back until the first job exists. The
  // Getting Started card above the grid carries the setup flow.
  const setupComplete = $derived(storage.length > 0 && jobs.length > 0)
  const SETUP_TILES = ['health', 'protected', 'nextrun', 'lastbackup']

  const tiles = $derived(layout.order.map((id, idx) => ({ id, idx, ...CATALOG[id], span: Math.min(layout.spans[id] ?? CATALOG[id].span, CATALOG[id].maxSpan ?? 12) })))
  const visibleTiles = $derived(
    layout.editMode
      ? tiles
      : tiles.filter(t => tileAvailable(t.id) && (setupComplete || SETUP_TILES.includes(t.id)))
  )
  const catalogList = $derived(
    Object.keys(CATALOG).map(id => ({ id, ...CATALOG[id], shown: layout.order.includes(id) }))
  )

  // Pointer-based drag reorder — works with a mouse AND a finger (HTML5 drag
  // doesn't fire on touch). The ↑/↓ buttons remain as the keyboard path.
  let dragIdx = $state(-1)   // order index being dragged
  let overIdx = $state(-1)   // order index currently under the pointer
  let dragActive = $state(false)
  let dragStart = null

  function tilePointerDown(e, idx, fromHandle = false) {
    if (!layout.editMode) return
    if (e.pointerType === 'mouse' && e.button !== 0) return
    // On touch, only the grab handle starts a drag — so touching the tile body
    // still scrolls the page. A mouse can drag from anywhere on the tile.
    if (e.pointerType !== 'mouse' && !fromHandle) return
    dragIdx = idx
    overIdx = idx
    dragActive = false
    dragStart = { x: e.clientX, y: e.clientY }
    window.addEventListener('pointermove', tilePointerMove, { passive: false })
    window.addEventListener('pointerup', tilePointerUp)
    window.addEventListener('pointercancel', tilePointerUp)
  }
  function tilePointerMove(e) {
    if (dragIdx < 0) return
    if (!dragActive) {
      if (Math.hypot(e.clientX - dragStart.x, e.clientY - dragStart.y) < 6) return
      dragActive = true // passed the movement threshold → it's a drag, not a tap
    }
    e.preventDefault() // stop the page scrolling under a touch drag
    const el = document.elementFromPoint(e.clientX, e.clientY)
    const tile = el && el.closest ? el.closest('.dash-tile') : null
    if (tile && tile.dataset.idx != null) overIdx = Number(tile.dataset.idx)
  }
  function tilePointerUp() {
    if (dragActive && dragIdx >= 0 && overIdx >= 0 && dragIdx !== overIdx) {
      layout.move(dragIdx, overIdx)
    }
    dragIdx = -1
    overIdx = -1
    dragActive = false
    dragStart = null
    window.removeEventListener('pointermove', tilePointerMove)
    window.removeEventListener('pointerup', tilePointerUp)
    window.removeEventListener('pointercancel', tilePointerUp)
  }

  // ── Vertical compaction ──────────────────────────────────────────────────
  // Pack tiles into the 12-col grid, filling vertical gaps while keeping reading
  // order — the gridstack/Datadog model, not masonry. Each tile keeps its
  // natural (measured) content height, so there is neither a gap between tiles
  // nor dead space inside them. Fine 4px row tracks with row-gap:0 and a per-tile
  // span that folds in the 12px gap give uniform spacing.
  // ponytail: O(cols·tiles) skyline pack; fine for ~20 tiles, revisit if it grows.
  const GRID_COLS = 12
  const CELL_PX = 4
  const GAP_PX = 12
  let tileEls = {}         // id -> wrapper element (content-height)
  let tilePos = $state({}) // id -> { col, row, span }
  let compactRaf = 0
  let compactObs = null

  function registerTile(el, id) {
    tileEls[id] = el
    compactObs?.observe(el)
    scheduleCompact()
    return {
      destroy() {
        compactObs?.unobserve(el)
        if (tileEls[id] === el) delete tileEls[id]
        scheduleCompact()
      },
    }
  }

  function scheduleCompact() {
    if (compactRaf || typeof window === 'undefined') return
    compactRaf = window.requestAnimationFrame(() => { compactRaf = 0; compact() })
  }

  function compact() {
    if (typeof window === 'undefined') return
    // Below 720px the grid stacks (CSS handles it); explicit positions are off.
    if (window.innerWidth <= 720) { if (Object.keys(tilePos).length) tilePos = {}; return }
    const cols = new Array(GRID_COLS).fill(0) // filled height (row units) per column
    const pos = {}
    for (const t of visibleTiles) {
      const el = tileEls[t.id]
      if (!el) continue
      const w = Math.min(GRID_COLS, t.span)
      const span = Math.max(1, Math.ceil((el.getBoundingClientRect().height + GAP_PX) / CELL_PX))
      let bestX = 0, bestY = Infinity
      for (let x = 0; x + w <= GRID_COLS; x++) {
        let y = 0
        for (let c = x; c < x + w; c++) if (cols[c] > y) y = cols[c]
        if (y < bestY) { bestY = y; bestX = x }
      }
      for (let c = bestX; c < bestX + w; c++) cols[c] = bestY + span
      pos[t.id] = { col: bestX + 1, row: bestY + 1, span }
    }
    tilePos = pos
  }

  // Re-pack when the set of tiles or their widths change; the ResizeObserver
  // (wired in onMount) covers content-height changes within stable tiles.
  $effect(() => { void visibleTiles; scheduleCompact() })
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

{#snippet pendingBadge()}
  <span class="ml-auto inline-flex items-center gap-1 text-2xs text-info shrink-0 whitespace-nowrap" title="In a backup job but not captured in a restore point yet — it will be backed up on the next scheduled run">
    <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.clock}/></svg>
    pending first backup
  </span>
{/snippet}

<!-- ═══ Tile bodies ═══ -->

<!-- Shared compact metric header: icon chip + uppercase label. Keeps every
     tile visually consistent and adds the icon richness the flat look lacked. -->
{#snippet mHead(icon, label)}
  <div class="flex items-center gap-2 mb-2.5">
    <span class="w-6 h-6 rounded-md bg-vault/10 text-vault-text flex items-center justify-center shrink-0">
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8"><path stroke-linecap="round" stroke-linejoin="round" d={icon}/></svg>
    </span>
    <span class="text-2xs font-semibold uppercase tracking-wider text-text-muted truncate">{label}</span>
  </div>
{/snippet}

{#snippet metricCardEmpty(icon, label)}
  <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
    {@render mHead(icon, label)}
    <p class="text-xs text-text-dim mt-auto">Not available yet</p>
  </div>
{/snippet}

{#snippet tHealth()}
  <a href="#/history" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors" title="Scored from items in backup jobs and recent runs. View backup history">
    {@render mHead(CATALOG.health.icon, 'Job health')}
    <div class="flex items-center gap-3 mt-auto">
      <div class="relative w-12 h-12 shrink-0">
        <svg aria-hidden="true" viewBox="0 0 36 36" class="w-full h-full -rotate-90">
          <circle cx="18" cy="18" r="15" fill="none" stroke="var(--color-surface-4)" stroke-width="4" />
          <circle cx="18" cy="18" r="15" fill="none" stroke={healthColor} stroke-width="4" stroke-linecap="round"
            stroke-dasharray={2 * Math.PI * 15} stroke-dashoffset={2 * Math.PI * 15 * (1 - healthScore / 100)} class="transition-all duration-700" />
        </svg>
        <div class="absolute inset-0 flex items-center justify-center text-sm font-bold text-text tabular-nums">{healthSummary ? healthScore : '–'}</div>
      </div>
      <div class="min-w-0 break-words">
        <p class="text-xs font-medium text-text">{healthSummaryText}</p>
        {#if avgSpeed}<p class="text-2xs text-text-dim mt-0.5 tabular-nums">avg {avgSpeed}</p>{/if}
      </div>
    </div>
  </a>
{/snippet}

{#snippet tProtected()}
  <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
    {@render mHead(CATALOG.protected.icon, 'Protected')}
    <p class="text-metric leading-none font-bold text-text tabular-nums">{protection.known ? formatInt(totalProtected) : '–'}<span class="text-base text-text-dim font-semibold">/{formatInt(totalItems)}</span></p>
    <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden mt-2.5">
      <div class="h-full {protectionBar} transition-all duration-500" style="width: {protection.known ? protectionPct : 0}%"></div>
    </div>
    {#if !protection.known}
      <p class="text-2xs text-text-dim mt-1.5 font-medium">Protection status unavailable</p>
    {:else if fullyProtected}
      <p class="text-2xs text-success mt-1.5 font-medium">All items protected</p>
    {:else}
      <!-- Protected means a recent enough Restore Point (#708). Each other state
           is named, so a short count never reads as "all covered". -->
      <p class="text-2xs mt-1.5 font-medium flex flex-wrap gap-x-2 tabular-nums">
        {#if hasUnprotectedItems}<a href="#/jobs" class="text-vault-text hover:underline">{formatInt(unprotectedCount)} unprotected →</a>{/if}
        {#if outOfDateCount > 0}<a href="#/recovery" class="text-warning hover:underline">{formatInt(outOfDateCount)} out of date →</a>{/if}
        {#if pendingCount > 0}<span class="text-text-dim">{formatInt(pendingCount)} pending first backup</span>{/if}
      </p>
    {/if}
  </div>
{/snippet}

{#snippet tNextRun()}
  <a href="#/jobs" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.nextrun.icon, 'Next run')}
    {#if soonestNextRun}
      <p class="text-xl font-bold text-text tabular-nums leading-none">{relTimeUntil(soonestNextRun)}</p>
      {#if soonestJob}<p class="text-2xs text-text-dim mt-1 truncate">{soonestJob.name}</p>{/if}
    {:else}
      <p class="text-xl font-bold text-text-dim leading-none">No schedule</p>
    {/if}
    <p class="text-2xs text-vault-text font-medium mt-auto pt-1.5 tabular-nums">{jobs.length} job{jobs.length === 1 ? '' : 's'} · {enabledJobs.length} enabled</p>
  </a>
{/snippet}

{#snippet tLastBackup()}
  <a href="#/history" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.lastbackup.icon, 'Last backup')}
    {#if lastBackup}
      {@const ok = lastBackup.status === 'completed' || lastBackup.status === 'success'}
      {@const running = lastBackup.status === 'running'}
      {@const partial = lastBackup.status === 'partial'}
      <!-- A skipped or cancelled run didn't fail: it's neutral, and its label
           comes from the shared helper like every other page's (#694). -->
      {@const neutral = lastBackup.status === 'skipped' || lastBackup.status === 'cancelled'}
      <p class="text-base font-bold flex items-center gap-1.5 {ok ? 'text-success' : running ? 'text-info' : partial ? 'text-warning' : neutral ? 'text-text-muted' : 'text-danger'}">
        <span class="w-2 h-2 rounded-full shrink-0 {ok ? 'bg-success' : running ? 'bg-info' : partial ? 'bg-warning' : neutral ? 'bg-surface-5' : 'bg-danger'}"></span>
        {runStatusLabel(lastBackup.status)}
      </p>
      <p class="text-2xs text-text-muted mt-1 truncate">{lastBackup.jobName} · {relTime(lastBackup.started_at)}</p>
      {#if lastBackup.size_bytes || lastBackup.duration_seconds}
        <p class="text-2xs text-text-muted mt-auto pt-1 tabular-nums">{formatBytes(lastBackup.size_bytes || 0)}{lastBackup.duration_seconds ? ` · ${fmtDur(lastBackup.duration_seconds)}` : ''}</p>
      {/if}
    {:else}
      <p class="text-base font-bold text-text-dim">No runs yet</p>
    {/if}
  </a>
{/snippet}

{#snippet tThreeTwoOne()}
  {#if jobs.length > 0 && backupRuleOn}
    <ComplianceBadge {storage} {jobs} {replicationSources} ondismiss={isReplicaMode() ? undefined : dismissBackupRule} goalSetting={settings.backup_rule_goal || ''} onGoalChange={isReplicaMode() ? undefined : setBackupRuleGoal} protectedCount={healthSummary?.protected_items ?? null} />
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl p-4 text-sm text-text-dim">
      {#if jobs.length === 0}
        3-2-1 rule unavailable — add a job to compute it.
      {:else}
        3-2-1 check is turned off. <a href="#/settings/general" class="text-vault-text hover:underline font-medium">Turn it on in Settings › Dashboard</a>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet tProgress()}
  {#if progress.activeRun}
    {@const progressItems = Object.entries(progress.itemProgress)}
    {@const activeItemPct = progressItems.reduce((maxPct, [, info]) => info.status === 'running' ? Math.max(maxPct, info.percent || 0) : maxPct, 0)}
    {@const overallPct = progress.overallTotal > 0 ? Math.min(100, Math.round((((progress.overallDone + progress.overallFailed) + (activeItemPct / 100)) / progress.overallTotal) * 100)) : activeItemPct}
    {@const elapsedStr = progress.elapsedSec >= 3600 ? `${Math.floor(progress.elapsedSec / 3600)}h ${Math.floor((progress.elapsedSec % 3600) / 60)}m` : progress.elapsedSec >= 60 ? `${Math.floor(progress.elapsedSec / 60)}m ${progress.elapsedSec % 60}s` : `${progress.elapsedSec}s`}
    {@const activeRunLabel = progress.activeRun.run_type === 'restore' ? 'Restore in progress' : 'Backup in progress'}
    <!-- Not a live region: the timer and counts change every second (#686). The
         page's progress announcer speaks for start, phase and finish. -->
    <div class="bg-surface-2 border border-vault/30 rounded-xl p-3.5">
      <div class="flex items-center gap-2 mb-2.5">
        <div class="w-2.5 h-2.5 rounded-full bg-vault animate-pulse shrink-0"></div>
        <span class="text-2xs font-semibold uppercase tracking-wider text-text-muted">{activeRunLabel}</span>
        <span class="ml-auto text-2xs px-2 py-0.5 rounded-full bg-vault/15 text-vault-text font-medium truncate max-w-[45%]">{progress.activeRun.job_name}</span>
        {#if progress.activeRun.run_type !== 'restore'}
          <button onclick={cancelActiveRun} disabled={progress.cancelling}
            class="text-2xs px-2 py-0.5 rounded-full border border-danger/40 text-danger hover:bg-danger/10 transition-colors disabled:opacity-50 shrink-0"
            title="Cancel this backup" aria-label="Cancel running backup">
            {progress.cancelling ? 'Cancelling…' : 'Cancel'}
          </button>
        {/if}
      </div>
      <div class="flex items-center justify-between text-xs text-text-muted mb-1.5">
        <span>Overall progress</span><span class="font-mono text-text-dim tabular-nums">{overallPct}%</span>
      </div>
      <div class="w-full h-2 bg-surface-4 rounded-full overflow-hidden">
        <div class="h-full rounded-full transition-all duration-300 {overallPct < 100 ? 'shimmer-bar' : 'bg-vault'}" style="width: {overallPct}%"></div>
      </div>
      <p class="text-2xs text-text-dim mt-2 tabular-nums">
        {progress.overallDone}/{progress.overallTotal} items · {elapsedStr}{#if progress.overallFailed > 0} · <span class="text-danger">{progress.overallFailed} failed</span>{/if}{#if liveSpeed} · <span class="text-info">{liveSpeed}</span>{/if}
      </p>
      {#if progress.currentItem}
        <p class="text-2xs text-text-dim mt-1 truncate">
          {progress.activeRun.run_type === 'restore' ? 'Restoring' : 'Backing up'}: <span class="text-text font-medium">{progress.currentItem.name}</span>{#if progress.currentItem.item_type} <span class="text-text-muted">({progress.currentItem.item_type})</span>{/if}
        </p>
        <ItemProgress item={progress.currentItem} runType={progress.activeRun.run_type} />
      {/if}
      {#if progress.phaseMessage}<p class="text-2xs text-warning animate-pulse mt-1">{progress.phaseMessage}</p>{/if}
    </div>
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
      {@render mHead(CATALOG.progress.icon, 'Backup in progress')}
      <div class="flex items-center gap-2 mt-auto text-text-dim">
        <svg aria-hidden="true" class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={ICONS.shield}/></svg>
        <div><p class="text-sm font-medium text-text">No backup running</p>{#if soonestNextRun}<p class="text-2xs text-text-dim tabular-nums">Next: {relTimeUntil(soonestNextRun)}</p>{/if}</div>
      </div>
    </div>
  {/if}
{/snippet}

{#snippet tActivity()}
  <ActivityTimeline runs={recentRuns} maxItems={6} />
{/snippet}

{#snippet tJobs()}
  <div class="bg-surface-2 border border-border rounded-xl">
    <div class="px-4 py-3 border-b border-border flex items-center gap-2">
      <span class="w-6 h-6 rounded-md bg-vault/10 text-vault-text flex items-center justify-center shrink-0"><svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8"><path stroke-linecap="round" stroke-linejoin="round" d={CATALOG.jobs.icon}/></svg></span>
      <span class="text-2xs font-semibold uppercase tracking-wider text-text-muted">Backup jobs</span>
      <a href="#/jobs" class="ml-auto text-xs text-vault-text hover:underline font-medium">View all →</a>
    </div>
    {#if jobs.length === 0}
      <div class="px-5 py-8 text-center text-sm text-text-muted">No backup jobs configured</div>
    {:else}
      <div class="divide-y divide-border">
        {#each jobs.slice(0, 5) as job (job.id)}
          <div class="px-5 py-3 flex items-center justify-between gap-2">
            <div class="min-w-0">
              <p class="text-sm font-medium text-text truncate">{job.name}</p>
              <p class="text-xs text-text-dim">{job.enabled ? 'Enabled' : 'Disabled'} · {job.compression || 'none'}</p>
            </div>
            {#if !isReplicaMode()}
              <div class="flex items-center gap-2 shrink-0">
                <a href="#/restore?job={job.id}" class="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg font-medium transition-colors bg-surface-3 text-text-muted hover:bg-surface-4 hover:text-text" title="Restore from {job.name}" aria-label="Restore: {job.name}">
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.refresh}/></svg>
                  Restore
                </a>
                <button onclick={() => runNow(job)} disabled={runningJob === job.id} aria-label="{runningJob === job.id ? 'Running...' : 'Run Now'}: {job.name}" class="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg font-medium transition-colors bg-vault/10 text-vault-text hover:bg-vault/20 disabled:opacity-50">
                  {#if runningJob === job.id}
                    <InlineSpinner size="xs" />
                    Running...
                  {:else}
                    <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.play}/></svg>
                    Run Now
                  {/if}
                </button>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/snippet}

{#snippet protItemRow(it)}
  {@const displayLabel = itemDisplayLabel({ item_type: it.group === 'flash' ? 'flash' : it.type, item_name: it.name, ...it.source })}
  {@const backedUp = it.state === 'protected' || it.state === 'out_of_date'}
  <div class="flex items-center gap-2.5 px-3 py-2 rounded-lg {it.state === 'protected' ? 'bg-success/5' : it.state === 'out_of_date' ? 'bg-warning/5' : 'bg-surface-3'} group" data-protection-state={it.state}>
    <div class="w-2 h-2 rounded-full shrink-0 {protectionDot(it.state)}"></div>
    <!-- The name wraps rather than truncating, and the USB badge drops below it
         when the row is narrow (#715). -->
    <div class="min-w-0 flex-1 flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <span class="text-sm text-text break-words min-w-0">{displayLabel}</span>
      {#if it.group === 'flash'}<span class="text-2xs px-1.5 py-0.5 rounded bg-hue-amber-bg/15 text-hue-amber font-medium whitespace-nowrap">USB boot drive</span>{/if}
    </div>
    {#if backedUp && !isReplicaMode()}
      <a href="#/restore?type={it.type}&name={encodeURIComponent(it.name)}" class="opacity-40 hover:opacity-100 focus-visible:opacity-100 p-1 text-vault-text hover:bg-vault/10 rounded transition-all shrink-0" title="Restore {it.name}" aria-label="Restore: {it.name}">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.refresh}/></svg>
      </a>
    {/if}
    {#if it.state === 'protected'}
      <span class="inline-flex items-center gap-1 text-2xs text-success shrink-0 whitespace-nowrap">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d={ICONS.shield}/></svg>
        <span class="sr-only">Protected</span>
      </span>
    {:else if it.state === 'out_of_date'}
      <span class="inline-flex items-center gap-1 text-2xs text-warning shrink-0 whitespace-nowrap tabular-nums" title={it.lastBackupAt ? `Last backup ${formatDate(it.lastBackupAt)}` : 'Out of date'}>
        <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.warning}/></svg>
        {it.lastBackupAt ? `Last backup ${relTime(it.lastBackupAt)}` : 'Out of date'}
      </span>
    {:else if it.state === 'pending'}
      {@render pendingBadge()}
    {:else if it.state === 'unknown'}
      <span class="text-2xs text-text-dim shrink-0">status unknown</span>
    {:else}
      <span class="text-2xs text-text-dim shrink-0">unprotected</span>
    {/if}
  </div>
{/snippet}

{#snippet protGroup(label, iconClass, iconPath, items)}
  {#if items.length > 0}
    <div>
      <div class="flex items-center gap-2 mb-3">
        <svg aria-hidden="true" class="w-4 h-4 {iconClass}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={iconPath}/></svg>
        <h3 class="text-sm font-medium text-text">{label}</h3>
        <span class="text-xs text-text-dim ml-auto tabular-nums">{protection.known ? formatInt(items.filter(it => it.state === 'protected').length) : '–'}/{formatInt(items.length)}</span>
      </div>
      <!-- Items flow into as many columns as fit, so a long list doesn't leave
           the rest of the page empty beside it (#715). -->
      <div class="prot-item-grid grid gap-1.5">{#each items as it (it.key)}{@render protItemRow(it)}{/each}</div>
    </div>
  {/if}
{/snippet}

{#snippet tProtection()}
  {#if totalItems > 0}
    <div class="bg-surface-2 border border-border rounded-xl">
      <div class="px-4 py-3 flex items-center justify-between gap-2 flex-wrap {protectionExpanded ? 'border-b border-border' : ''}">
        <div class="flex items-center gap-2 min-w-0 flex-wrap">
          <span class="w-6 h-6 rounded-md bg-vault/10 text-vault-text flex items-center justify-center shrink-0"><svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8"><path stroke-linecap="round" stroke-linejoin="round" d={CATALOG.protection.icon}/></svg></span>
          <span class="text-2xs font-semibold uppercase tracking-wider text-text-muted">Protection</span>
          {#if protection.known}
            <span class="text-xs px-2 py-0.5 rounded-full font-medium tabular-nums shrink-0 {fullyProtected ? 'bg-success/15 text-success' : protectionPct >= 50 ? 'bg-warning/15 text-warning' : 'bg-danger/15 text-danger'}">
              {formatInt(totalProtected)}/{formatInt(totalItems)} protected · {protectionPct}%
            </span>
            {#if outOfDateCount > 0}<span class="text-xs px-2 py-0.5 rounded-full font-medium tabular-nums shrink-0 bg-warning/15 text-warning">{formatInt(outOfDateCount)} out of date</span>{/if}
          {:else}
            <span class="text-xs px-2 py-0.5 rounded-full font-medium shrink-0 bg-surface-3 text-text-muted">Status unknown</span>
          {/if}
        </div>
        {#if hasUnprotectedItems && !isReplicaMode()}
          <a href="#/jobs" class="text-xs text-vault-text hover:underline transition-colors font-medium">+ Add to Backup</a>
        {:else if fullyProtected}
          <button onclick={toggleProtection} aria-expanded={protectionExpanded} class="flex items-center gap-1 text-xs font-medium text-text-muted hover:text-text transition-colors">
            {protectionExpanded ? 'Hide items' : 'Show items'}
            <svg aria-hidden="true" class="w-4 h-4 transition-transform {protectionExpanded ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronDown}/></svg>
          </button>
        {/if}
      </div>
      {#if protectionExpanded}
        <div class="p-5">
          <div class="w-full h-2 bg-surface-4 rounded-full overflow-hidden mb-5">
            <div class="h-full rounded-full transition-all duration-500 {protectionBar}" style="width: {protection.known ? protectionPct : 0}%"></div>
          </div>
          <div class="space-y-6">
            {@render protGroup('Containers', 'text-hue-blue', ITEM_TYPE_ICONS.container, protection.groups.container)}
            {@render protGroup('Virtual Machines', 'text-hue-purple', ITEM_TYPE_ICONS.vm, protection.groups.vm)}
            {@render protGroup('Folders', 'text-hue-amber', ICONS.folder, protection.groups.folder)}
            {@render protGroup('Flash Drive', 'text-hue-amber', ITEM_TYPE_ICONS.flash, protection.groups.flash)}
          </div>
        </div>
      {/if}
    </div>
  {:else}
    {@render metricCardEmpty(CATALOG.protection.icon, 'Protection status')}
  {/if}
{/snippet}

{#snippet tStorageCombined()}
  {#if storageCombined}
    <a href="#/storage" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
      {@render mHead(CATALOG.storageCombined.icon, 'Storage used')}
      <p class="text-metric leading-none font-bold text-text tabular-nums">{formatBytes(storageCombined.used)}</p>
      <p class="text-2xs text-text-muted mt-1 tabular-nums">of {formatBytes(storageCombined.total)} · {storageCombined.count} target{storageCombined.count === 1 ? '' : 's'}</p>
      <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden mt-2.5"><div class="h-full bg-vault" style="width: {storageCombined.pct}%"></div></div>
    </a>
  {:else}{@render metricCardEmpty(CATALOG.storageCombined.icon, 'Storage used')}{/if}
{/snippet}

{#snippet tStoragePerTarget()}
  <a href="#/storage" class="block bg-surface-2 border border-border rounded-xl p-3.5 cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.storagePerTarget.icon, 'Storage per target')}
    {#if storageCaps.length}
      <div class="flex flex-col gap-2.5">
        {#each storageCaps as s (s.id)}
          {@const pct = s.capacity.total_bytes > 0 ? Math.round((s.capacity.used_bytes || 0) / s.capacity.total_bytes * 100) : 0}
          <div>
            <div class="flex justify-between text-xs mb-1"><span class="text-text truncate">{s.name}</span><span class="text-text-dim shrink-0 ml-2 tabular-nums">{formatBytes(s.capacity.used_bytes || 0)}</span></div>
            <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden"><div class="h-full bg-vault" style="width: {pct}%"></div></div>
          </div>
        {/each}
      </div>
    {:else}<p class="text-xs text-text-dim mt-auto">No capacity data yet — probe a destination on the Storage page.</p>{/if}
  </a>
{/snippet}

{#snippet tRecovery()}
  <a href="#/recovery" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.recovery.icon, 'Recovery readiness')}
    <p class="text-metric leading-none font-bold tabular-nums {protectionText}">{protection.known ? `${protectionPct}%` : '–'}</p>
    <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden mt-2.5"><div class="h-full {protectionBar}" style="width: {protection.known ? protectionPct : 0}%"></div></div>
    <p class="text-2xs text-text-muted mt-1.5 tabular-nums">{#if protection.known}{formatInt(totalProtected)} of {formatInt(totalItems)} protected{#if outOfDateCount > 0} · {formatInt(outOfDateCount)} out of date{/if}{:else}Protection status unavailable{/if}</p>
  </a>
{/snippet}

{#snippet tAttention()}
  <a href="#{attentionHref({ failures: recentFailures, unprotected: knownUnprotected, outOfDate: outOfDateCount, replica: isReplicaMode() })}" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.attention.icon, 'Needs attention')}
    <p class="text-metric leading-none font-bold tabular-nums {attentionCount > 0 ? 'text-danger' : healthSummary ? 'text-success' : 'text-text-dim'}">{formatInt(attentionCount)}</p>
    <p class="text-2xs text-text-muted mt-auto pt-1.5">{attentionCaption({ failures: recentFailures, unprotected: knownUnprotected, outOfDate: outOfDateCount, other: otherProblems, fullyProtected, known: !!healthSummary })}</p>
  </a>
{/snippet}

{#snippet tSuccessRate()}
  <a href="#/history" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.successrate.icon, 'Success rate')}
    {#if successStats}
      <p class="text-metric leading-none font-bold text-text tabular-nums">{successStats.pct}%</p>
      <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden mt-2.5"><div class="h-full {successStats.pct >= 90 ? 'bg-success' : successStats.pct >= 50 ? 'bg-warning' : 'bg-danger'}" style="width: {successStats.pct}%"></div></div>
      <p class="text-2xs text-text-muted mt-1.5 tabular-nums">{successStats.ok} of {successStats.total} recent runs</p>
    {:else}
      <p class="text-metric leading-none font-bold text-text-dim">—</p>
      <p class="text-2xs text-text-dim mt-auto pt-1.5">No completed runs yet</p>
    {/if}
  </a>
{/snippet}

{#snippet tAnomalies()}
  {#if getAnomalyEnabled() && anomaliesError}
    <!-- Unknown is not green: a failed count is "unavailable", never 0 (#681). -->
    <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
      {@render mHead(CATALOG.anomalies.icon, 'Anomalies')}
      <p class="text-metric leading-none font-bold text-text-dim">–</p>
      <div class="mt-auto pt-1.5"><LoadError compact title="Anomalies unavailable" onretry={retryAnomalies} retrying={anomaliesRetrying} /></div>
    </div>
  {:else if getAnomalyEnabled()}
    <a href="#/anomalies" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
      {@render mHead(CATALOG.anomalies.icon, 'Anomalies')}
      <p class="text-metric leading-none font-bold tabular-nums {anomalies.openList.length === 0 ? 'text-success' : 'text-warning'}">{anomalies.openList.length}</p>
      <p class="text-2xs text-text-dim mt-auto pt-1.5">{anomalies.openList.length === 0 ? 'No unusual runs detected' : 'open — review on Anomalies'}</p>
    </a>
  {:else}{@render metricCardEmpty(CATALOG.anomalies.icon, 'Anomalies')}{/if}
{/snippet}

{#snippet tQuickActions()}
  <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
    {@render mHead(CATALOG.quickactions.icon, 'Quick actions')}
    {#if !isReplicaMode()}
      <div class="flex flex-col gap-1.5 mt-auto">
        <button onclick={runAll} disabled={runningAll || enabledJobs.length === 0} class="text-xs font-medium px-3 py-2 rounded-lg bg-vault text-on-vault hover:bg-vault-dark disabled:opacity-50 transition-colors text-left">
          {runningAll ? 'Starting…' : 'Run all backups'}
        </button>
        <div class="flex gap-1.5">
          <a href="#/jobs" class="flex-1 text-center text-xs font-medium px-3 py-2 rounded-lg bg-vault/10 text-vault-text hover:bg-vault/20 transition-colors">New job</a>
          <a href="#/restore" class="flex-1 text-center text-xs font-medium px-3 py-2 rounded-lg bg-vault/10 text-vault-text hover:bg-vault/20 transition-colors">Restore</a>
        </div>
      </div>
    {:else}
      <p class="text-xs text-text-dim mt-auto">Read-only replica</p>
    {/if}
  </div>
{/snippet}

{#snippet tSizeTrend()}
  {#if trendError}
    <div class="bg-surface-2 border border-border rounded-xl p-3.5">
      {@render mHead(CATALOG.sizeTrend.icon, 'Backup size trend')}
      <LoadError compact title="Couldn't load the size trend" onretry={retryTrend} />
    </div>
  {:else}
  <a href="#/history" class="block bg-surface-2 border border-border rounded-xl p-3.5 cursor-pointer hover:border-vault/40 transition-colors">
    <div class="flex items-center gap-2 mb-2.5">
      <span class="w-6 h-6 rounded-md bg-vault/10 text-vault-text flex items-center justify-center shrink-0"><svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8"><path stroke-linecap="round" stroke-linejoin="round" d={CATALOG.sizeTrend.icon}/></svg></span>
      <span class="text-2xs font-semibold uppercase tracking-wider text-text-muted truncate">Backup size trend</span>
      {#if trendChange}
        <span class="ml-auto text-2xs font-semibold px-2 py-0.5 rounded-full tabular-nums bg-surface-3 {sizeTrendClass(trendChange.pctChange > 0 ? 'up' : 'down')}">{trendChange.pctChange >= 0 ? '+' : ''}{formatInt(trendChange.pctChange)}% · 30d</span>
      {/if}
    </div>
    {#if trendPolyline}
      <svg viewBox="0 0 300 64" preserveAspectRatio="none" class="w-full h-20" aria-hidden="true">
        <polyline points="{trendPolyline} 300,64 0,64" fill="var(--color-vault)" opacity="0.10" vector-effect="non-scaling-stroke" />
        <polyline points={trendPolyline} fill="none" stroke="var(--color-vault)" stroke-width="2" vector-effect="non-scaling-stroke" />
      </svg>
      {#if trendChange}<p class="text-2xs text-text-dim mt-2 tabular-nums">{formatBytes(trendChange.last)}/day now · was {formatBytes(trendChange.first)}</p>{/if}
    {:else}
      <p class="text-xs text-text-dim py-5 text-center">{trendLoading ? 'Loading trend…' : 'Not enough history yet'}</p>
    {/if}
  </a>
  {/if}
{/snippet}

{#snippet tCalendar()}
  <div class="bg-surface-2 border border-border rounded-xl p-3.5">
    <div class="flex items-center gap-2">
      <div class="flex-1 min-w-0">{@render mHead(CATALOG.calendar.icon, 'Backup calendar')}</div>
      <a href="#/history" class="text-xs text-vault-text hover:underline font-medium mb-2.5 shrink-0">View history →</a>
    </div>
    {#if trendError}
      <LoadError compact title="Couldn't load the backup calendar" onretry={retryTrend} />
    {:else if calendarGrid}
      <div class="flex items-start justify-between gap-4 flex-wrap">
        <!-- The coloured grid is a picture of the table below, which carries the
             per-day detail for screen readers (#690). -->
        <div class="flex gap-2" aria-hidden="true">
          <div class="grid gap-1 text-2xs leading-none text-text-dim shrink-0 items-center" style="grid-template-rows: repeat(7, 15px);">
            <span></span><span>Mon</span><span></span><span>Wed</span><span></span><span>Fri</span><span></span>
          </div>
          <div class="grid gap-1" style="grid-auto-flow: column; grid-template-rows: repeat(7, 15px); grid-auto-columns: 15px;">
            {#each calendarGrid.days as d (d.key)}
              <div class="rounded-[3px]" style={calCellStyle(d)} title="{d.label} — {d.future ? 'upcoming' : d.bytes ? formatBytes(d.bytes) + ' backed up' : 'no backup'}"></div>
            {/each}
          </div>
        </div>
        <table class="sr-only">
          <caption>Backups per day, last 5 weeks</caption>
          <thead><tr><th scope="col">Day</th><th scope="col">Backed up</th></tr></thead>
          <tbody>
            {#each calendarGrid.days.filter(d => !d.future) as d (d.key)}
              <tr><th scope="row">{d.label}</th><td>{d.bytes ? formatBytes(d.bytes) : 'No backup'}</td></tr>
            {/each}
          </tbody>
        </table>
        {#if calendarStats}
          <div class="flex gap-5 sm:gap-6 shrink-0">
            <div>
              <p class="text-xl font-bold text-text tabular-nums leading-none">{calendarStats.ran}<span class="text-xs text-text-dim font-semibold">/{calendarStats.days}</span></p>
              <p class="text-2xs text-text-dim mt-1">days backed up</p>
            </div>
            <div>
              <p class="text-xl font-bold text-text tabular-nums leading-none">{calendarStats.streak}</p>
              <p class="text-2xs text-text-dim mt-1">day streak</p>
            </div>
          </div>
        {/if}
      </div>
      <div class="flex items-center gap-1.5 mt-3 text-2xs text-text-dim">
        <span aria-hidden="true">Less</span>
        <span aria-hidden="true" class="w-2.5 h-2.5 rounded-[3px] bg-surface-4"></span>
        <span aria-hidden="true" class="w-2.5 h-2.5 rounded-[3px]" style="background: color-mix(in srgb, var(--color-success) 40%, transparent);"></span>
        <span aria-hidden="true" class="w-2.5 h-2.5 rounded-[3px]" style="background: color-mix(in srgb, var(--color-success) 70%, transparent);"></span>
        <span aria-hidden="true" class="w-2.5 h-2.5 rounded-[3px] bg-success"></span>
        <span aria-hidden="true">More</span>
        <span class="ml-auto tabular-nums">{formatBytes(calendarStats?.total || 0)} in 30d</span>
      </div>
    {:else}
      <p class="text-xs text-text-dim py-6 text-center">{trendLoading ? 'Loading…' : 'No history yet'}</p>
    {/if}
  </div>
{/snippet}

{#snippet tSavings()}
  {#if dedupSummary}
    <a href="#/storage" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
      {@render mHead(CATALOG.savings.icon, 'Dedup & compression')}
      <p class="text-metric leading-none font-bold text-success tabular-nums">{dedupSummary.ratio.toFixed(1)}×</p>
      <p class="text-2xs text-text-muted mt-auto pt-1.5 tabular-nums">{formatBytes(dedupSummary.logical)} → {formatBytes(dedupSummary.physical)} stored</p>
    </a>
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
      {@render mHead(CATALOG.savings.icon, 'Dedup & compression')}
      {#if dedupError}
        <div class="mt-auto"><LoadError compact title="Couldn't load dedup savings" onretry={retryDedup} /></div>
      {:else}
        <p class="text-2xs text-text-dim mt-auto">{dedupLoading ? 'Loading…' : 'No deduplicated data yet'}</p>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet tForecast()}
  {#if forecastSummary}
    <a href="#/storage" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
      {@render mHead(CATALOG.forecast.icon, 'Storage forecast')}
      <p class="text-metric leading-none font-bold text-text tabular-nums">~{forecastSummary.days}<span class="text-base text-text-dim font-semibold"> days</span></p>
      <p class="text-2xs text-text-muted mt-1.5">until {forecastSummary.name} is full</p>
      <p class="text-2xs text-warning mt-auto pt-1 font-medium tabular-nums">+{formatBytes(forecastSummary.perDay)}/day</p>
    </a>
  {:else}
    <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
      {@render mHead(CATALOG.forecast.icon, 'Storage forecast')}
      {#if forecastError}
        <div class="mt-auto"><LoadError compact title="Couldn't load the storage forecast" onretry={retryForecast} /></div>
      {:else}
        <p class="text-2xs text-text-dim mt-auto">{forecastLoading ? 'Loading…' : 'Not filling / not enough samples'}</p>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet tLargest()}
  <div class="bg-surface-2 border border-border rounded-xl p-3.5">
    {@render mHead(CATALOG.largest.icon, 'Largest backups')}
    {#if largestBackups.length}
      {@const max = largestBackups[0].size}
      <div class="flex flex-col gap-2.5">
        {#each largestBackups as b (b.name)}
          <div>
            <div class="flex justify-between text-xs mb-1"><span class="text-text truncate">{b.name}</span><span class="text-text-dim shrink-0 ml-2 tabular-nums">{formatBytes(b.size)}</span></div>
            <div class="h-1.5 bg-surface-4 rounded-full overflow-hidden"><div class="h-full bg-vault" style="width: {Math.round(b.size / max * 100)}%"></div></div>
          </div>
        {/each}
      </div>
    {:else}
      <p class="text-xs text-text-dim py-4 text-center">No sized backups yet</p>
    {/if}
  </div>
{/snippet}

{#snippet tRetention()}
  {#if retentionError}
    <div class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col">
      {@render mHead(CATALOG.retention.icon, 'Retention & cleanup')}
      <div class="mt-auto"><LoadError compact title="Couldn't load retention" onretry={retryRetention} /></div>
    </div>
  {:else}
  <a href="#/jobs" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
    {@render mHead(CATALOG.retention.icon, 'Retention & cleanup')}
    {#if retentionSummary}
      <p class="text-metric leading-none font-bold text-text tabular-nums">{retentionSummary.prunable}<span class="text-base text-text-dim font-semibold"> prunable</span></p>
      <p class="text-2xs text-text-muted mt-1.5 tabular-nums">of {retentionSummary.total} restore point{retentionSummary.total === 1 ? '' : 's'}{#if soonestRetentionNextRun} · next prune {relTimeUntil(soonestRetentionNextRun)}{/if}</p>
    {:else}
      <p class="text-2xs text-text-dim mt-auto">{retentionLoading ? 'Loading…' : 'No retention policy set'}</p>
    {/if}
  </a>
  {/if}
{/snippet}

{#snippet tReplicationStatus()}
  {#if replicationSummary}
    {@const rs = replicationSummary}
    {@const noneActive = rs.enabled === 0}
    {@const ok = !noneActive && rs.failed === 0 && rs.partial === 0 && rs.synced === rs.enabled}
    {@const caption = rs.failed > 0 ? `${rs.failed} failed` : rs.partial > 0 ? `${rs.partial} partial` : ok ? 'All offsite copies synced' : 'sync pending'}
    <a href="#/replication" class="bg-surface-2 border border-border rounded-xl p-3.5 min-h-[104px] flex flex-col cursor-pointer hover:border-vault/40 transition-colors">
      {@render mHead(CATALOG.replicationStatus.icon, 'Replication status')}
      {#if noneActive}
        <p class="text-base font-bold text-text-dim mt-auto">All sources disabled</p>
        <p class="text-2xs text-text-muted mt-1.5 tabular-nums">{rs.total} configured</p>
      {:else}
        <!-- numerator and denominator both scoped to enabled sources, so the
             count and the caption never disagree (e.g. "2/2 · All synced"). -->
        <p class="text-metric leading-none font-bold tabular-nums {rs.failed > 0 ? 'text-danger' : ok ? 'text-success' : 'text-warning'}">{rs.synced}<span class="text-base text-text-dim font-semibold">/{rs.enabled}</span></p>
        <p class="text-2xs text-text-muted mt-1.5 tabular-nums">{caption}{#if rs.lastSync} · last {relTime(rs.lastSync)}{/if}</p>
      {/if}
    </a>
  {:else}{@render metricCardEmpty(CATALOG.replicationStatus.icon, 'Replication status')}{/if}
{/snippet}

{#snippet tileBody(id)}
  {#if id === 'health'}{@render tHealth()}
  {:else if id === 'protected'}{@render tProtected()}
  {:else if id === 'nextrun'}{@render tNextRun()}
  {:else if id === 'lastbackup'}{@render tLastBackup()}
  {:else if id === 'threetwoone'}{@render tThreeTwoOne()}
  {:else if id === 'progress'}{@render tProgress()}
  {:else if id === 'activity'}{@render tActivity()}
  {:else if id === 'jobs'}{@render tJobs()}
  {:else if id === 'protection'}{@render tProtection()}
  {:else if id === 'storageCombined'}{@render tStorageCombined()}
  {:else if id === 'storagePerTarget'}{@render tStoragePerTarget()}
  {:else if id === 'recovery'}{@render tRecovery()}
  {:else if id === 'attention'}{@render tAttention()}
  {:else if id === 'successrate'}{@render tSuccessRate()}
  {:else if id === 'anomalies'}{@render tAnomalies()}
  {:else if id === 'quickactions'}{@render tQuickActions()}
  {:else if id === 'sizeTrend'}{@render tSizeTrend()}
  {:else if id === 'calendar'}{@render tCalendar()}
  {:else if id === 'savings'}{@render tSavings()}
  {:else if id === 'forecast'}{@render tForecast()}
  {:else if id === 'largest'}{@render tLargest()}
  {:else if id === 'retention'}{@render tRetention()}
  {:else if id === 'replicationStatus'}{@render tReplicationStatus()}
  {/if}
{/snippet}

<PullToRefresh onrefresh={runDashboardReload}>
<div>
  <div class="dash-page-head flex items-start justify-between gap-4 mb-6 flex-wrap">
    <div>
      <h1 class="text-2xl font-bold text-text">Dashboard</h1>
      <div class="flex items-center gap-2 mt-1 flex-wrap">
        <p class="text-sm text-text-muted">{layout.editMode ? 'Arrange your dashboard — drag, add, or remove tiles' : 'Overview of your backup system'}</p>
        {#if health}
          <span class="inline-flex items-center gap-1.5 text-xs text-text-dim">
            <span class="w-1.5 h-1.5 rounded-full {health.status === 'ok' ? 'bg-success' : 'bg-danger'}"></span>
            {health.status === 'ok' ? 'Online' : 'Offline'}<!-- The desktop sidebar already shows the version (#715); embedded has no sidebar.
            --><span data-dash-version class="lg:[html:not(.vault-embedded)_&]:hidden"> · v{health.version || '?'}</span>
          </span>
        {/if}
      </div>
    </div>
    {#if !loading && !error && storage.length > 0 && jobs.length > 0}
      <!-- On phones Customize sits below the status banner, so the verdict is the
           first thing under the heading (#699). -->
      <div class="dash-head-actions {layout.editMode ? 'flex' : 'hidden sm:flex'} gap-2 w-full sm:w-auto">
        {#if layout.editMode}
          <button onclick={layout.reset} class="flex-1 sm:flex-none min-h-[44px] px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-2 border border-border hover:bg-surface-3 rounded-lg transition-colors">Reset</button>
          <button onclick={layout.toggleEdit} class="flex-1 sm:flex-none min-h-[44px] inline-flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold text-on-status bg-success hover:bg-success/90 rounded-lg transition-colors">
            <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.check}/></svg>Done
          </button>
        {:else}
          <button onclick={layout.toggleEdit} class="flex-1 sm:flex-none min-h-[44px] inline-flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-2 border border-border hover:bg-surface-3 rounded-lg transition-colors">
            <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7M18.5 2.5a2.121 2.121 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>Customize
          </button>
        {/if}
      </div>
    {/if}
  </div>

  {#if isReplicaMode()}
    <div class="flex items-center gap-2.5 bg-surface-3 border border-border rounded-xl px-4 py-2.5 mb-4 text-sm text-text-muted">
      <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.lock}/></svg>
      <span>Read-only replica — write actions are disabled on this instance.</span>
    </div>
  {/if}

  <StatusBanner status={backupStatus} />
  <p class="sr-only" role="status" aria-live="polite" aria-atomic="true" data-progress-announcer>{progressAnnouncementText}</p>

  {#if !loading && !error && storage.length > 0 && jobs.length > 0 && !layout.editMode}
    <div class="sm:hidden flex justify-end -mt-2 mb-3">
      <button onclick={layout.toggleEdit} class="min-h-[44px] inline-flex items-center gap-1.5 px-3 py-2 text-sm font-medium text-text-muted hover:text-text hover:bg-surface-3 rounded-lg transition-colors">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7M18.5 2.5a2.121 2.121 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>Customize
      </button>
    </div>
  {/if}

  {#if loading}
    <Skeleton variant="stats" />
    <Skeleton variant="card" count={3} />
  {:else if error}
    <LoadError title="Couldn't load the dashboard" message={error} onretry={runDashboardReload} />
  {:else if storage.length === 0 && jobs.length === 0}
    <Welcome onstart={() => navigate('/storage')} onrecover={isReplicaMode() ? undefined : () => navigate('/recover')} />
  {:else}
    <!-- Getting Started Guide (shown when no storage or no jobs) -->
    {#if storage.length === 0 || jobs.length === 0}
      <div class="bg-surface-2 border border-vault/30 rounded-xl p-6 mb-8">
        <div class="flex items-start gap-4">
          <div class="w-10 h-10 rounded-lg bg-vault/10 flex items-center justify-center shrink-0 mt-0.5">
            <svg aria-hidden="true" class="w-5 h-5 text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.bolt}/></svg>
          </div>
          <div class="flex-1 min-w-0">
            <h2 class="text-base font-semibold text-text">Getting Started</h2>
            <p class="text-sm text-text-muted mt-1">Set up your backup system in a few easy steps.</p>
            <div class="mt-4 flex flex-col sm:flex-row gap-3">
              <button onclick={() => navigate('/storage')} class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {storage.length > 0 ? 'border-success/30 bg-success/5' : 'border-vault/40 bg-vault/5 hover:bg-vault/10'}">
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {storage.length > 0 ? 'bg-success text-on-status' : 'bg-vault text-on-vault'}">
                  {#if storage.length > 0}<svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d={ICONS.check}/></svg>{:else}1{/if}
                </div>
                <div>
                  <p class="text-sm font-medium {storage.length > 0 ? 'text-success' : 'text-text'}">Configure Storage</p>
                  <p class="text-xs {storage.length > 0 ? 'text-success' : 'text-text-dim'}">{storage.length > 0 ? `${storage.length} destination${storage.length !== 1 ? 's' : ''} configured` : 'Set up where backups are stored'}</p>
                </div>
              </button>
              <button onclick={() => navigate('/jobs')} disabled={storage.length === 0} class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {jobs.length > 0 ? 'border-success/30 bg-success/5' : storage.length > 0 ? 'border-vault/40 bg-vault/5 hover:bg-vault/10' : 'border-border bg-surface-3 opacity-50 cursor-not-allowed'}">
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {jobs.length > 0 ? 'bg-success text-on-status' : storage.length > 0 ? 'bg-vault text-on-vault' : 'bg-surface-4 text-text-muted'}">
                  {#if jobs.length > 0}<svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d={ICONS.check}/></svg>{:else}2{/if}
                </div>
                <div>
                  <p class="text-sm font-medium {jobs.length > 0 ? 'text-success' : storage.length > 0 ? 'text-text' : 'text-text-dim'}">Create Backup Job</p>
                  <p class="text-xs {jobs.length > 0 ? 'text-success' : 'text-text-dim'}">{jobs.length > 0 ? `${jobs.length} job${jobs.length !== 1 ? 's' : ''} configured` : 'Choose what to back up and when'}</p>
                </div>
              </button>
              <div class="flex items-center gap-3 px-4 py-3 rounded-lg border transition-colors text-left {jobs.length > 0 && storage.length > 0 ? 'border-vault/40 bg-vault/5' : 'border-border bg-surface-3'}">
                <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold shrink-0 {jobs.length > 0 && storage.length > 0 ? 'bg-vault text-on-vault' : 'bg-surface-4 text-text-muted'}">3</div>
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

    <!-- Edit-mode banner -->
    {#if layout.editMode}
      <div class="flex items-center gap-2.5 bg-info/10 border border-info/30 rounded-xl px-4 py-3 mb-4 text-sm text-info">
        <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 8h16M4 8a2 2 0 01-2-2V5a2 2 0 012-2h16a2 2 0 012 2v1a2 2 0 01-2 2M4 8v10a2 2 0 002 2h12a2 2 0 002-2V8M9 12h6"/></svg>
        <span><strong>Customize mode.</strong> Drag a tile by its handle to reorder (or use ↑ ↓) · remove with × · add more from the panel.</span>
      </div>
    {/if}

    <!-- Tile grid + add-tiles panel -->
    <div class="dash-layout-row flex gap-4 items-start">
      <div class="dash-tile-grid flex-1 min-w-0 {layout.editMode ? 'is-edit' : ''}" role="list">
        {#each visibleTiles as t (t.id)}
          <div
            role="listitem"
            class="dash-tile relative min-w-0 {dragActive && dragIdx === t.idx ? 'is-dragging' : ''} {dragActive && overIdx === t.idx && dragIdx !== t.idx ? 'is-dragover' : ''}"
            style="--w:{t.span}; --col:{tilePos[t.id]?.col ?? 'auto'}; --row:{tilePos[t.id]?.row ?? 'auto'}; --span:{tilePos[t.id]?.span ?? 1};"
            data-span={t.span}
            data-idx={t.idx}
            use:registerTile={t.id}
          >
            {#if layout.editMode}
              <!-- Mouse can drag anywhere on the tile; this overlay also blocks
                   the tile's own click actions while customizing. (Touch scrolls
                   here and drags from the handle below instead.) -->
              <div class="absolute inset-0 z-[1] rounded-xl" style="cursor: grab;" onpointerdown={(e) => tilePointerDown(e, t.idx, false)} aria-hidden="true"></div>
              <!-- The label keeps its full name: when the tile is narrow the controls
                   wrap to a second line instead of squeezing it to a letter (#715). -->
              <div class="relative z-[2] flex flex-wrap items-center gap-x-2 gap-y-1 mb-1.5 px-1">
                <div class="flex flex-1 items-center gap-2 min-w-[7rem] cursor-grab py-1 -my-1 touch-none" style="touch-action: none;" onpointerdown={(e) => tilePointerDown(e, t.idx, true)} aria-hidden="true" title="Drag to reorder">
                  <svg aria-hidden="true" class="w-4 h-4 text-text-dim shrink-0" fill="currentColor" viewBox="0 0 24 24"><circle cx="8" cy="6" r="1.4"/><circle cx="8" cy="12" r="1.4"/><circle cx="8" cy="18" r="1.4"/><circle cx="16" cy="6" r="1.4"/><circle cx="16" cy="12" r="1.4"/><circle cx="16" cy="18" r="1.4"/></svg>
                  <span class="text-2xs font-medium text-text-muted break-words" data-tile-label>{t.name}</span>
                </div>
                <div class="ml-auto flex items-center gap-1 shrink-0">
                  <span class="resize-controls flex items-center gap-1">
                    <button onclick={() => layout.resize(t.id, t.span, -1, CATALOG[t.id].maxSpan ?? 12)} disabled={t.span <= SPAN_OPTIONS[0]} class="p-1 rounded text-text-muted hover:text-text hover:bg-surface-3 disabled:opacity-30 disabled:cursor-not-allowed" aria-label="Make {t.name} narrower" title="Narrower">
                      <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.chevronLeft}/></svg>
                    </button>
                    <button onclick={() => layout.resize(t.id, t.span, 1, CATALOG[t.id].maxSpan ?? 12)} disabled={t.span >= Math.min(SPAN_OPTIONS[SPAN_OPTIONS.length - 1], CATALOG[t.id].maxSpan ?? 12)} class="p-1 rounded text-text-muted hover:text-text hover:bg-surface-3 disabled:opacity-30 disabled:cursor-not-allowed" aria-label="Make {t.name} wider" title="Wider">
                      <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.chevronRight}/></svg>
                    </button>
                    <span class="w-px h-4 bg-border mx-0.5"></span>
                  </span>
                  <button onclick={() => layout.moveBy(t.id, -1)} disabled={t.idx === 0} class="p-1 rounded text-text-muted hover:text-text hover:bg-surface-3 disabled:opacity-30 disabled:cursor-not-allowed" aria-label="Move {t.name} up" title="Move up">
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.chevronUp}/></svg>
                  </button>
                  <button onclick={() => layout.moveBy(t.id, 1)} disabled={t.idx === layout.order.length - 1} class="p-1 rounded text-text-muted hover:text-text hover:bg-surface-3 disabled:opacity-30 disabled:cursor-not-allowed" aria-label="Move {t.name} down" title="Move down">
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.chevronDown}/></svg>
                  </button>
                  <button onclick={() => layout.remove(t.id)} class="p-1 rounded text-danger hover:bg-danger/10" aria-label="Remove {t.name}" title="Remove tile">
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d={ICONS.close}/></svg>
                  </button>
                </div>
              </div>
            {/if}
            <!-- Inert while customizing: the toolbar owns the keyboard here, so Tab
                 can't reach a link or control the overlay blocks for the mouse. -->
            <div inert={layout.editMode}>{@render tileBody(t.id)}</div>
          </div>
        {/each}
      </div>

      {#if layout.editMode}
        <aside aria-label="Add tiles" class="dash-addpanel shrink-0 bg-surface-2 border border-border rounded-xl p-4">
          <div class="text-sm font-bold text-text">Add tiles</div>
          <div class="text-xs text-text-muted mb-3">Click to add to your dashboard</div>
          <div class="dash-cat-list flex flex-col gap-2">
            {#each catalogList as c (c.id)}
              <button onclick={() => layout.add(c.id)} disabled={c.shown}
                class="flex items-center justify-between gap-2 w-full px-3 py-2 rounded-lg border text-left transition-colors {c.shown ? 'border-border bg-surface-3 cursor-default' : 'border-border bg-surface hover:border-vault/40'}">
                <div class="flex items-center gap-2.5 min-w-0">
                  <span class="w-7 h-7 rounded-lg bg-vault/10 text-vault-text flex items-center justify-center shrink-0">
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8"><path stroke-linecap="round" stroke-linejoin="round" d={c.icon}/></svg>
                  </span>
                  <span class="text-xs font-medium text-text truncate">{c.name}</span>
                </div>
                <span class="shrink-0 text-2xs font-semibold px-2 py-0.5 rounded-full {c.shown ? 'bg-success/15 text-success' : 'bg-vault/15 text-vault-text'}">{c.shown ? 'Added' : '+ Add'}</span>
              </button>
            {/each}
          </div>
        </aside>
      {/if}
    </div>
  {/if}
</div>
</PullToRefresh>

<style>
  :global(.dash-tile-grid) {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-auto-rows: 4px;  /* fine row unit — tiles span a measured number of them */
    column-gap: 12px;
    row-gap: 0;           /* vertical 12px gap is folded into each tile's row span */
    align-content: start;
    align-items: start;   /* tiles stay content-height so measurement never loops */
  }
  /* Position comes from the compaction pass (--col/--row/--span); before it runs
     (and on the server) tiles auto-flow at their column width. */
  :global(.dash-tile) {
    grid-column: var(--col, auto) / span var(--w, 1);
    grid-row: var(--row, auto) / span var(--span, 1);
  }
  :global(.dash-tile.is-dragging) { opacity: 0.4; }
  .prot-item-grid { grid-template-columns: repeat(auto-fill, minmax(min(100%, 15rem), 1fr)); }
  :global(.dash-tile.is-dragover) { outline: 2px solid var(--color-info); outline-offset: 2px; border-radius: 14px; }
  :global(.dash-tile-grid.is-edit .dash-tile) { cursor: grab; }

  /* Tablet: stack the add-tiles panel under the grid. */
  @media (max-width: 1024px) {
    :global(.dash-layout-row) { flex-direction: column; }
    :global(.dash-addpanel) { width: 100%; }
    :global(.dash-cat-list) { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }

  /* Phone: drop compaction, stack in normal flow — 2-up small tiles, panels
     full width. Explicit positions are cleared by compact() below 720px; these
     rules override the var-driven grid placement regardless. */
  @media (max-width: 720px) {
    :global(.dash-tile-grid) { grid-template-columns: repeat(2, minmax(0, 1fr)); grid-auto-rows: auto; column-gap: 10px; row-gap: 10px; }
    :global(.dash-tile-grid > .dash-tile) { grid-column: span 2 !important; grid-row: auto !important; }
    :global(.dash-tile-grid > .dash-tile[data-span="3"]),
    :global(.dash-tile-grid > .dash-tile[data-span="4"]) { grid-column: span 1 !important; }
    :global(.dash-cat-list) { grid-template-columns: minmax(0, 1fr); }
    /* Below 720px the grid is forced to 1–2 columns, so a tile's stored span
       can't visibly change — hide the resize arrows (reorder + remove stay). */
    :global(.resize-controls) { display: none; }
  }

  /* Very small phones only: single column. Above this (e.g. 375/390px) small
     KPI tiles stay 2-up for a denser, ops-dashboard feel. */
  @media (max-width: 359px) {
    :global(.dash-tile-grid) { grid-template-columns: minmax(0, 1fr); }
    :global(.dash-tile-grid > .dash-tile) { grid-column: span 1 !important; grid-row: auto !important; }
  }
</style>
