<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { buildApiRequest } from '../lib/runtime-config.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { describeSchedule, relTimeUntil } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Skeleton from '../components/Skeleton.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import AnomalyBadge from '../components/AnomalyBadge.svelte'
  import { getAnomalies, onBaselineUpdated } from '../lib/anomalies.svelte.js'
  import { getAnomalyEnabled } from '../lib/settings.svelte.js'
  import ItemPicker from '../components/ItemPicker.svelte'
  import ScheduleBuilder from '../components/ScheduleBuilder.svelte'
  import BackupModeSelector from '../components/BackupModeSelector.svelte'
  import ScriptBrowser from '../components/ScriptBrowser.svelte'
  import TypePicker from '../components/TypePicker.svelte'
  import Tooltip from '../components/Tooltip.svelte'
  import RetryDelaysEditor from '../components/RetryDelaysEditor.svelte'

  let loading = $state(true)
  let jobs = $state([])
  let storageList = $state([])
  let showModal = $state(false)
  let editing = $state(null)
  let saving = $state(false)
  let runningJob = $state(null)
  let confirmDelete = $state({ show: false, id: 0, name: '', deleteFiles: false })
  let toast = $state({ message: '', type: 'info', key: 0 })
  let nextRuns = $state({})
  let editingNameId = $state(null)
  let editName = $state('')

  // Anomaly / baseline per-job data
  const anomalyState = getAnomalies()
  /** @type {Record<number, {sample_count: number}|null>} */
  let baselines = $state({})

  // Monotonic counter to discard out-of-order baseline fetch responses.
  let baselineLoadId = 0

  // Stale-item remediation (#119): items whose underlying container/VM/folder
  // no longer exists on the system (persisted missing_since on the job item).
  let staleItems = $state({})          // jobId -> array of stale job items (persisted missing_since)
  let staleLoadId = 0
  let remediating = $state(null)       // the job currently shown in the remediation dialog (or null)
  let remediateBusy = $state(false)

  // Count open anomalies per job from shared state
  /** @type {(jobId: number) => number} */
  function jobAnomalyCount(jobId) {
    return anomalyState.openList.filter(a => a.scope_kind === 'job' && a.scope_id === jobId).length
  }

  /** @type {(jobId: number) => string} */
  function jobWorstSeverity(jobId) {
    const anomalies = anomalyState.openList.filter(a => a.scope_kind === 'job' && a.scope_id === jobId)
    for (const sev of ['critical', 'warning', 'info']) {
      if (anomalies.some(a => a.severity === sev)) return sev
    }
    return 'info'
  }

  function baselineSamples(jobId) {
    return baselines[jobId]?.sample_count ?? 0
  }

  function jobStaleCount(jobId) {
    return (staleItems[jobId] || []).length
  }

  // Find / filter / sort toolbar (Direction 3). All client-side over the
  // already-loaded jobs list, so no extra requests.
  let search = $state('')
  let statusFilter = $state('all') // 'all' | 'enabled' | 'disabled'
  let storageFilter = $state(0) // 0 = all destinations, else storage_dest_id
  let sortBy = $state('name') // 'name' | 'next' | 'created'

  let filtersActive = $derived(
    search.trim() !== '' || statusFilter !== 'all' || storageFilter !== 0
  )

  let filteredJobs = $derived.by(() => {
    const q = search.trim().toLowerCase()
    const out = jobs.filter(j => {
      if (statusFilter === 'enabled' && !j.enabled) return false
      if (statusFilter === 'disabled' && j.enabled) return false
      if (storageFilter !== 0 && j.storage_dest_id !== storageFilter) return false
      if (q && !(`${j.name} ${j.description || ''}`.toLowerCase().includes(q))) return false
      return true
    })
    const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
    out.sort((a, b) => {
      if (sortBy === 'created') return new Date(b.created_at) - new Date(a.created_at)
      if (sortBy === 'next') {
        const na = nextRuns[String(a.id)], nb = nextRuns[String(b.id)]
        if (!na && !nb) return byName(a, b)
        if (!na) return 1 // jobs without a next run sort last
        if (!nb) return -1
        return new Date(na) - new Date(nb)
      }
      return byName(a, b)
    })
    return out
  })

  function clearFilters() {
    search = ''
    statusFilter = 'all'
    storageFilter = 0
  }

  // Bulk selection. Selection and the select-all toggle operate on the
  // currently filtered set, so you can (e.g.) filter to Disabled and bulk
  // enable just those.
  let selectedJobs = $state(new SvelteSet())
  let bulkRunning = $state(false)

  let allSelected = $derived(
    filteredJobs.length > 0 && filteredJobs.every(j => selectedJobs.has(j.id))
  )
  // How many of the currently visible (filtered) jobs are selected — keeps the
  // "Select all (n/total)" numerator consistent with its filtered denominator.
  let selectedVisibleCount = $derived(filteredJobs.filter(j => selectedJobs.has(j.id)).length)

  function toggleSelectJob(id) {
    const next = new SvelteSet(selectedJobs)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedJobs = next
  }

  function toggleSelectAll() {
    if (allSelected) {
      selectedJobs = new SvelteSet()
    } else {
      selectedJobs = new SvelteSet(filteredJobs.map(j => j.id))
    }
  }

  async function bulkEnable(enable) {
    bulkRunning = true
    try {
      const targets = jobs.filter(j => selectedJobs.has(j.id) && j.enabled !== enable)
      await Promise.all(targets.map(j => api.updateJob(j.id, { ...j, enabled: enable })))
      showToast(`${targets.length} job${targets.length !== 1 ? 's' : ''} ${enable ? 'enabled' : 'disabled'}`, 'success')
      selectedJobs = new SvelteSet()
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      bulkRunning = false
    }
  }

  async function bulkRun() {
    bulkRunning = true
    try {
      const ids = [...selectedJobs]
      await Promise.all(ids.map(id => api.runJob(id)))
      showToast(`${ids.length} job${ids.length !== 1 ? 's' : ''} queued`, 'success')
      selectedJobs = new SvelteSet()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      bulkRunning = false
    }
  }

  async function bulkDelete() {
    bulkRunning = true
    try {
      const ids = [...selectedJobs]
      await Promise.all(ids.map(id => api.deleteJob(id, false)))
      showToast(`${ids.length} job${ids.length !== 1 ? 's' : ''} deleted`, 'success')
      selectedJobs = new SvelteSet()
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      bulkRunning = false
    }
  }

  // Wizard step
  let step = $state(1)
  const totalSteps = 6

  // Form state
  let form = $state(defaultForm())

  // LTR retention preview state (Feature C). Recomputed via a debounced
  // effect whenever any keep_* field changes on an editing job. Inactive
  // for new-job mode (no restore points to preview against yet).
  let retentionPreview = $state(null)
  let retentionPreviewLoading = $state(false)
  let retentionPreviewError = $state('')
  let retentionPreviewTimer = null

  let ltrActive = $derived(
    (form.keep_latest || 0) + (form.keep_daily || 0) + (form.keep_weekly || 0) +
    (form.keep_monthly || 0) + (form.keep_yearly || 0) > 0
  )

  // Feature A: friendly verify schedule. The backend still stores a cron
  // string in form.verify_schedule (empty = disabled). The checkbox below
  // toggles between empty (off) and a sensible default (Sunday 04:00).
  let verifyEnabled = $derived((form.verify_schedule || '').trim() !== '')
  function toggleVerifyEnabled(e) {
    if (e.currentTarget.checked) {
      form.verify_schedule = form.verify_schedule || '0 4 * * 0'
    } else {
      form.verify_schedule = ''
    }
  }

  $effect(() => {
    // Re-trigger on any keep_* change.
    const policy = {
      keep_latest: form.keep_latest || 0,
      keep_daily: form.keep_daily || 0,
      keep_weekly: form.keep_weekly || 0,
      keep_monthly: form.keep_monthly || 0,
      keep_yearly: form.keep_yearly || 0,
    }
    if (!editing || !ltrActive) {
      retentionPreview = null
      retentionPreviewError = ''
      return
    }
    if (retentionPreviewTimer) clearTimeout(retentionPreviewTimer)
    retentionPreviewTimer = setTimeout(async () => {
      retentionPreviewLoading = true
      retentionPreviewError = ''
      try {
        retentionPreview = await api.getRetentionPreview(editing.id, policy)
      } catch (e) {
        retentionPreviewError = e?.message || 'request failed'
        retentionPreview = null
      } finally {
        retentionPreviewLoading = false
      }
    }, 300)
  })

  function defaultForm() {
    return {
      name: '',
      description: '',
      enabled: true,
      schedule: '0 2 * * *',
      backup_type_chain: 'full',
      retention_count: 5,
      retention_days: 30,
      keep_latest: 0,
      keep_daily: 0,
      keep_weekly: 0,
      keep_monthly: 0,
      keep_yearly: 0,
      verify_schedule: '',
      verify_mode: 'quick',
      compression: 'zstd',
      container_mode: 'one_by_one',
      vm_mode: 'snapshot',
      pre_script: '',
      post_script: '',
      notify_on: 'failure',
      verify_backup: true,
      defer_remote_upload: false,
      encryption: 'none',
      storage_dest_id: 0,
      retry_max_override: '',
      retry_delays_override: '',
      anomaly_sensitivity: '',
      max_parallel_uploads: 3,
      items: [],
      selectedTypes: [],
    }
  }

  // Retry-override JSON validation used to live here; the RetryDelaysEditor
  // component now handles parsing/serialising and only ever emits a valid
  // JSON array string (or '' for "use the global default"), so no client-side
  // validator is needed any more.

  const vmRestoreVerifyModes = [
    {
      value: 'running',
      label: 'Running State Only',
      description: 'Finish restore once libvirt reports the guest is running again.',
    },
    {
      value: 'guest_agent',
      label: 'QEMU Guest Agent',
      description: 'Wait for the guest agent to answer after the VM starts. Best when qemu-guest-agent is installed.',
    },
    {
      value: 'tcp',
      label: 'TCP Service',
      description: 'Wait for a TCP service such as SSH or Home Assistant to accept connections.',
    },
  ]

  function deriveTypesFromItems(items) {
    // eslint-disable-next-line svelte/prefer-svelte-reactivity -- local Set used only for de-duplication, not reactive state
    const types = new Set()
    for (const item of items) {
      if (item.item_type === 'container') types.add('containers')
      else if (item.item_type === 'vm') types.add('vms')
      else if (item.item_type === 'folder') {
        const s = parseItemSettings(item)
        if (s.preset === 'flash') types.add('flash')
        else types.add('folders')
      }
      else if (item.item_type === 'plugin') types.add('plugins')
      else if (item.item_type === 'zfs') types.add('zfs')
    }
    return [...types]
  }

  function parseItemSettings(item) {
    if (!item?.settings) return {}
    if (typeof item.settings === 'object') return item.settings
    try {
      return JSON.parse(item.settings)
    } catch {
      return {}
    }
  }

  function getVMRestoreVerifySettings(item) {
    const settings = parseItemSettings(item)
    return {
      mode: settings.restore_verify_mode || 'running',
      timeout: settings.restore_verify_timeout_seconds || 120,
      host: settings.restore_verify_tcp_host || '',
      port: settings.restore_verify_tcp_port ? String(settings.restore_verify_tcp_port) : '',
    }
  }

  function updateVMRestoreVerifySetting(itemName, key, rawValue) {
    form = {
      ...form,
      items: form.items.map((item) => {
        if (item.item_type !== 'vm' || item.item_name !== itemName) return item

        const settings = { ...parseItemSettings(item) }
        const value = rawValue == null ? '' : rawValue

        if (key === 'restore_verify_mode') {
          if (!value || value === 'running') {
            delete settings.restore_verify_mode
            delete settings.restore_verify_timeout_seconds
            delete settings.restore_verify_tcp_host
            delete settings.restore_verify_tcp_port
          } else {
            settings.restore_verify_mode = value
          }
        } else if (key === 'restore_verify_timeout_seconds') {
          if (value === '') delete settings.restore_verify_timeout_seconds
          else settings.restore_verify_timeout_seconds = Number.parseInt(value, 10)
        } else if (key === 'restore_verify_tcp_port') {
          if (value === '') delete settings.restore_verify_tcp_port
          else settings.restore_verify_tcp_port = Number.parseInt(value, 10)
        } else if (key === 'restore_verify_tcp_host') {
          if (!String(value).trim()) delete settings.restore_verify_tcp_host
          else settings.restore_verify_tcp_host = String(value).trim()
        }

        return { ...item, settings: JSON.stringify(settings) }
      }),
    }
  }

  function getVMRestoreVerifyError(item) {
    const verify = getVMRestoreVerifySettings(item)
    if (!['running', 'guest_agent', 'tcp'].includes(verify.mode)) {
      return `${item.item_name}: unsupported VM restore verification mode`
    }
    if (verify.mode !== 'running') {
      const timeout = Number.parseInt(String(verify.timeout), 10)
      if (!Number.isInteger(timeout) || timeout < 1) {
        return `${item.item_name}: verification timeout must be at least 1 second`
      }
    }
    if (verify.mode === 'tcp') {
      const port = Number.parseInt(String(verify.port), 10)
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        return `${item.item_name}: TCP readiness requires a port between 1 and 65535`
      }
    }
    return ''
  }

  function getContainerExclusionPaths(item) {
    const settings = parseItemSettings(item)
    return Array.isArray(settings.exclude_paths) ? settings.exclude_paths : []
  }

  function updateContainerExclusionPaths(itemName, paths) {
    form = {
      ...form,
      items: form.items.map((item) => {
        if (item.item_type !== 'container' || item.item_name !== itemName) return item

        const settings = { ...parseItemSettings(item) }
        if (paths.length === 0) {
          delete settings.exclude_paths
        } else {
          settings.exclude_paths = paths
        }

        return { ...item, settings: JSON.stringify(settings) }
      }),
    }
  }

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(() => {
    loadData()
    const unsubWs = onWsMessage((msg) => {
      if (msg.type === 'job_run_started' || msg.type === 'job_run_completed' || msg.type === 'import_completed' || msg.type === 'stale_items_detected') {
        loadData()
      } else if (msg.type === 'job_cleanup_complete') {
        // Outcome of the background backup-file sweep after delete-with-files (#111).
        showToast(`Backup files for "${msg.job_name}" deleted`, 'success')
      } else if (msg.type === 'job_cleanup_failed') {
        showToast(`Failed to delete backup files for "${msg.job_name}" – files may remain on storage (see Activity Log)`, 'error')
      }
    })
    const unsubBaseline = onBaselineUpdated((data) => {
      if (data?.job_id && data?.baseline) {
        baselines = { ...baselines, [data.job_id]: data.baseline }
      }
    })
    return () => { unsubWs(); unsubBaseline() }
  })

  async function loadData() {
    loading = true
    try {
      const [j, s, nr] = await Promise.all([api.listJobs(), api.listStorage(), api.getNextRuns()])
      jobs = j || []
      storageList = s || []
      nextRuns = nr || {}
      // Fetch baselines for all jobs in parallel; always returns 200 (sample_count=0 when still learning).
      void loadBaselines(jobs)
      // Fetch persisted stale items per job (no live inventory scan).
      void loadStaleItems(jobs)
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
    }
  }

  async function loadBaselines(jobList) {
    const reqId = ++baselineLoadId
    const results = await Promise.all(
      jobList.map(async (j) => {
        try {
          const b = await api.getJobBaseline(j.id)
          return [j.id, b]
        } catch {
          return [j.id, null]
        }
      })
    )
    if (reqId !== baselineLoadId) return // stale response – discard
    const map = {}
    for (const [id, b] of results) map[id] = b
    baselines = map
  }

  // Mirror loadBaselines: uses api.getJob (persisted items, no live Docker/
  // libvirt scan) and keeps only items the backend has already flagged with a
  // missing_since timestamp.
  async function loadStaleItems(jobList) {
    const reqId = ++staleLoadId
    const results = await Promise.all(
      jobList.map(async (j) => {
        try {
          const d = await api.getJob(j.id)
          const stale = (d.items || []).filter((it) => it.missing_since)
          return [j.id, stale]
        } catch {
          return [j.id, []]
        }
      })
    )
    if (reqId !== staleLoadId) return // stale response – discard
    const map = {}
    for (const [id, s] of results) map[id] = s
    staleItems = map
  }

  function openRemediate(job) {
    remediating = job
  }

  async function refreshStaleForJob(jobId) {
    try {
      const d = await api.getJob(jobId)
      staleItems = { ...staleItems, [jobId]: (d.items || []).filter((it) => it.missing_since) }
    } catch { /* ignore */ }
  }

  async function removeStaleItem(job, item) {
    remediateBusy = true
    try {
      await api.deleteJobItem(job.id, item.id)
      await refreshStaleForJob(job.id)
      showToast(`Removed ${item.item_name} from ${job.name}`, 'success')
      if (jobStaleCount(job.id) === 0) remediating = null
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      remediateBusy = false
    }
  }

  async function removeAllStale(job) {
    remediateBusy = true
    try {
      const res = await api.removeStaleItems(job.id)
      await refreshStaleForJob(job.id)
      showToast(`Removed ${res?.count ?? 0} missing item(s) from ${job.name}`, 'success')
      remediating = null
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      remediateBusy = false
    }
  }

  function openCreate() {
    editing = null
    form = defaultForm()
    if (storageList.length > 0) form.storage_dest_id = storageList[0].id
    step = 1
    showModal = true
  }

  async function openEdit(id) {
    try {
      const data = await api.getJob(id)
      editing = data.job
      form = {
        name: data.job.name || '',
        description: data.job.description || '',
        enabled: data.job.enabled ?? true,
        schedule: data.job.schedule || '0 2 * * *',
        backup_type_chain: data.job.backup_type_chain || 'full',
        retention_count: data.job.retention_count || 5,
        retention_days: data.job.retention_days || 30,
        keep_latest: data.job.keep_latest || 0,
        keep_daily: data.job.keep_daily || 0,
        keep_weekly: data.job.keep_weekly || 0,
        keep_monthly: data.job.keep_monthly || 0,
        keep_yearly: data.job.keep_yearly || 0,
        verify_schedule: data.job.verify_schedule || '',
        verify_mode: data.job.verify_mode || 'quick',
        compression: data.job.compression || 'zstd',
        container_mode: data.job.container_mode || 'one_by_one',
        // Read the saved VM backup mode so editing a job preserves the user's
        // choice (snapshot vs cold). Falls back to 'snapshot' for jobs created
        // before this column existed.
        vm_mode: data.job.vm_mode || 'snapshot',
        pre_script: data.job.pre_script || '',
        post_script: data.job.post_script || '',
        notify_on: data.job.notify_on || 'failure',
        verify_backup: data.job.verify_backup ?? true,
        defer_remote_upload: data.job.defer_remote_upload ?? false,
        encryption: data.job.encryption || 'none',
        storage_dest_id: data.job.storage_dest_id || 0,
        // Backend uses null to mean "fall back to the global default". The
        // form keeps them as strings so the inputs bind cleanly; we round
        // trip them back to null on save when blank.
        retry_max_override: data.job.retry_max_override == null ? '' : String(data.job.retry_max_override),
        retry_delays_override: data.job.retry_delays_override == null ? '' : String(data.job.retry_delays_override),
        anomaly_sensitivity: data.job.anomaly_sensitivity || '',
        max_parallel_uploads: data.job.max_parallel_uploads || 3,
        items: data.items || [],
        selectedTypes: deriveTypesFromItems(data.items || []),
      }
      step = 1
      showModal = true
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function saveJob(andRun = false) {
    saving = true
    try {
      const payload = { ...form }
      delete payload.selectedTypes
      // Normalise retry overrides: blank → null so the backend falls back
      // to the global default; otherwise coerce the max to an int and pass
      // the delays JSON through verbatim (already validated above).
      const retryMaxStr = String(payload.retry_max_override ?? '').trim()
      payload.retry_max_override = retryMaxStr === '' ? null : Number.parseInt(retryMaxStr, 10)
      const retryDelaysStr = String(payload.retry_delays_override ?? '').trim()
      payload.retry_delays_override = retryDelaysStr === '' ? null : retryDelaysStr
      let jobId
      if (editing) {
        await api.updateJob(editing.id, payload)
        jobId = editing.id
        showToast('Job updated successfully', 'success')
      } else {
        const result = await api.createJob(payload)
        jobId = result?.id
        showToast('Job created successfully', 'success')
      }
      if (andRun && jobId) {
        try {
          await api.runJob(jobId)
          showToast('Job queued for execution', 'success')
        } catch (e) {
          showToast(`Job saved but failed to start: ${e.message}`, 'warning')
        }
      }
      showModal = false
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  async function deleteJob(id, name) {
    confirmDelete = { show: true, id, name, deleteFiles: false }
  }

  async function doDeleteJob() {
    const { id, deleteFiles } = confirmDelete
    confirmDelete = { show: false, id: 0, name: '', deleteFiles: false }
    try {
      await api.deleteJob(id, deleteFiles)
      showToast(deleteFiles ? 'Job and backup files deleted' : 'Job deleted (backup files kept)', 'success')
      await loadData()
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function toggleEnabled(job) {
    try {
      await api.updateJob(job.id, { ...job, enabled: !job.enabled })
      await loadData()
      showToast(`Job ${!job.enabled ? 'enabled' : 'disabled'}`, 'success')
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function duplicateJob(job) {
    try {
      const data = await api.getJob(job.id)
      const fullJob = data.job
      form = {
        ...defaultForm(),
        name: `${fullJob.name} (copy)`,
        description: fullJob.description || '',
        schedule: fullJob.schedule || '0 2 * * *',
        storage_dest_id: fullJob.storage_dest_id || 0,
        compression: fullJob.compression || 'zstd',
        encryption: fullJob.encryption || 'none',
        container_mode: fullJob.container_mode || 'one_by_one',
        backup_type_chain: fullJob.backup_type_chain || 'full',
        retention_count: fullJob.retention_count || 5,
        retention_days: fullJob.retention_days || 30,
        keep_latest: fullJob.keep_latest || 0,
        keep_daily: fullJob.keep_daily || 0,
        keep_weekly: fullJob.keep_weekly || 0,
        keep_monthly: fullJob.keep_monthly || 0,
        keep_yearly: fullJob.keep_yearly || 0,
        verify_schedule: fullJob.verify_schedule || '',
        verify_mode: fullJob.verify_mode || 'quick',
        pre_script: fullJob.pre_script || '',
        post_script: fullJob.post_script || '',
        notify_on: fullJob.notify_on || 'failure',
        verify_backup: fullJob.verify_backup ?? true,
        defer_remote_upload: fullJob.defer_remote_upload ?? false,
        retry_max_override: fullJob.retry_max_override == null ? '' : String(fullJob.retry_max_override),
        retry_delays_override: fullJob.retry_delays_override == null ? '' : String(fullJob.retry_delays_override),
        anomaly_sensitivity: fullJob.anomaly_sensitivity || '',
        max_parallel_uploads: fullJob.max_parallel_uploads || 3,
        enabled: false,
        items: (data.items || []).map(i => ({
          item_type: i.item_type,
          item_name: i.item_name,
          item_id: i.item_id,
          settings: i.settings,
          sort_order: i.sort_order,
        })),
      }
      editing = null
      step = 3
      showModal = true
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  function startNameEdit(job) {
    editingNameId = job.id
    editName = job.name
  }

  async function saveJobName(job) {
    const trimmed = editName.trim()
    editingNameId = null
    if (!trimmed || trimmed === job.name) return
    try {
      await api.updateJob(job.id, { ...job, name: trimmed })
      await loadData()
      showToast('Job renamed', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function runNow(job) {
    runningJob = job.id
    try {
      await api.runJob(job.id)
      showToast(`"${job.name}" queued for execution`, 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      runningJob = null
    }
  }

  function getStorageName(id) {
    return storageList.find(s => s.id === id)?.name || 'Unknown'
  }

  // Wizard validation
  let canNext = $derived.by(() => {
    if (step === 1) return form.selectedTypes.length > 0
    if (step === 2) return form.items.length > 0
    if (step === 3) return form.storage_dest_id > 0
    if (step === 4) return form.name.trim().length > 0
    if (step === 5) return vmRestoreVerifyErrors.length === 0
    return true
  })

  let stepHint = $derived.by(() => {
    if (step === 1 && form.selectedTypes.length === 0) return 'Select at least one backup type'
    if (step === 2 && form.items.length === 0) return 'Select at least one item to back up'
    if (step === 3 && form.storage_dest_id === 0) return 'Select a storage destination'
    if (step === 4 && !form.name.trim()) return 'Enter a job name to continue'
    if (step === 5 && vmRestoreVerifyErrors.length > 0) return vmRestoreVerifyErrors[0]
    return ''
  })

  // Auto-suggest job name based on selected types
  let suggestedName = $derived.by(() => {
    if (form.selectedTypes.length === 0) return ''
    const labels = { containers: 'Containers', vms: 'VMs', folders: 'Folders', flash: 'Flash', plugins: 'Plugins', zfs: 'ZFS' }
    return form.selectedTypes.map(t => labels[t] || t).join(' + ') + ' Backup'
  })

  let hasContainers = $derived(form.items.some(i => i.item_type === 'container'))
  let hasVMs = $derived(form.items.some(i => i.item_type === 'vm'))
  let hasFolders = $derived(form.items.some(i => i.item_type === 'folder' && parseItemSettings(i).preset !== 'flash'))
  let hasFlash = $derived(form.items.some(i => i.item_type === 'folder' && parseItemSettings(i).preset === 'flash'))
  let hasPlugins = $derived(form.items.some(i => i.item_type === 'plugin'))
  let hasZFS = $derived(form.items.some(i => i.item_type === 'zfs'))
  let selectedVMItems = $derived(form.items.filter(i => i.item_type === 'vm'))
  let selectedContainerItems = $derived(form.items.filter(i => i.item_type === 'container'))
  let vmRestoreVerifyErrors = $derived(selectedVMItems.map(getVMRestoreVerifyError).filter(Boolean))

  let containerPresets = $state({})
  let presetsAbortController = null

  async function fetchContainerPresets(items) {
    if (presetsAbortController) {
      presetsAbortController.abort()
    }
    presetsAbortController = new AbortController()
    const signal = presetsAbortController.signal

    const newPresets = {}
    for (const item of items) {
      const settings = parseItemSettings(item)
      const image = settings.image || ''
      const containerName = item.item_name || ''
      if (!image && !containerName) continue
      try {
        const params = new URLSearchParams()
        if (image) params.set('image', image)
        if (containerName) params.set('container', containerName)
        const { url, options } = buildApiRequest('GET', `/presets/exclusions?${params.toString()}`)
        const res = await fetch(url, { ...options, signal })
        if (!res.ok) continue
        const data = await res.json()
        if (data.paths && data.paths.length > 0) {
          newPresets[item.item_name] = data.paths
        }
      } catch {
        // Silently ignore preset fetch failures and aborts.
      }
    }
    if (signal.aborted) return
    containerPresets = newPresets
  }

  $effect(() => {
    if (selectedContainerItems.length > 0) {
      fetchContainerPresets(selectedContainerItems)
    }
  })

  // Per-container bind mounts keyed by container name, used to render the
  // include/exclude toggles. Auto-skipped mounts (per the backup engine's
  // heuristics) are surfaced as disabled with their reason.
  let containerMounts = $state({})
  let mountsAbortController = null

  async function fetchContainerMounts(names) {
    if (mountsAbortController) {
      mountsAbortController.abort()
    }
    mountsAbortController = new AbortController()
    const signal = mountsAbortController.signal

    // Fetch every selected container's mounts concurrently. A failed or
    // unavailable fetch records an empty list (not undefined) so the UI shows
    // "no bind mounts" rather than spinning on "Loading…" forever.
    const entries = await Promise.all(
      names.filter(Boolean).map(async (name) => {
        try {
          const { url, options } = buildApiRequest('GET', `/containers/${encodeURIComponent(name)}/mounts`)
          const res = await fetch(url, { ...options, signal })
          if (!res.ok) return [name, []]
          const data = await res.json()
          return [name, data.available && Array.isArray(data.mounts) ? data.mounts : []]
        } catch {
          return [name, []]
        }
      })
    )
    // A newer fetch aborted this one – discard its results.
    if (signal.aborted) return
    containerMounts = Object.fromEntries(entries)
  }

  // Stable key of selected container names so mounts are only re-fetched when
  // the selection changes – not on every exclusion/toggle edit to form.items.
  let selectedContainerNamesKey = $derived(
    selectedContainerItems.map((i) => i.item_name).filter(Boolean).join('\n')
  )

  $effect(() => {
    const key = selectedContainerNamesKey
    if (!key) return
    fetchContainerMounts(key.split('\n'))
  })

  function getExcludedMounts(item) {
    const settings = parseItemSettings(item)
    return Array.isArray(settings.excluded_mounts) ? settings.excluded_mounts : []
  }

  function updateExcludedMounts(itemName, mounts) {
    form = {
      ...form,
      items: form.items.map((item) => {
        if (item.item_type !== 'container' || item.item_name !== itemName) return item

        const settings = { ...parseItemSettings(item) }
        if (mounts.length === 0) {
          delete settings.excluded_mounts
        } else {
          settings.excluded_mounts = mounts
        }

        return { ...item, settings: JSON.stringify(settings) }
      }),
    }
  }

  function getItemByName(itemName) {
    return form.items.find((i) => i.item_type === 'container' && i.item_name === itemName)
  }

  // Normalise a path for whole-mount comparison: trim and drop a trailing slash.
  function cleanMountPath(p) {
    const t = (p || '').trim()
    return t === '/' ? '/' : t.replace(/\/+$/, '')
  }

  // A mount is shown as excluded (unchecked) when its whole destination is
  // excluded via either mechanism: the checkbox-driven excluded_mounts, or an
  // exact-path entry in the free-text exclude_paths (e.g. a pre-existing
  // /rootfs exclusion). Subpath/glob patterns leave the mount checked – the
  // mount is still backed up, just filtered.
  function isMountWholeExcluded(item, destination) {
    const dest = cleanMountPath(destination)
    if (getExcludedMounts(item).some((d) => cleanMountPath(d) === dest)) return true
    return getContainerExclusionPaths(item).some((p) => cleanMountPath(p) === dest)
  }

  function toggleExcludedMount(itemName, destination, included) {
    const item = getItemByName(itemName)
    const dest = cleanMountPath(destination)
    if (included) {
      // Include the mount: drop it from excluded_mounts and remove any exact
      // whole-mount entry from exclude_paths so re-checking actually re-includes.
      updateExcludedMounts(itemName, getExcludedMounts(item).filter((d) => cleanMountPath(d) !== dest))
      updateContainerExclusionPaths(itemName, getContainerExclusionPaths(item).filter((p) => cleanMountPath(p) !== dest))
    } else {
      updateExcludedMounts(itemName, [...new Set([...getExcludedMounts(item), destination])])
    }
  }

  // Clear defer_remote_upload when the user switches to (or stays on) a local
  // destination – deferring an upload that never leaves the box is a no-op.
  $effect(() => {
    const selected = storageList.find(s => s.id === form.storage_dest_id)
    const isLocal = !selected || selected.type === 'local'
    if (isLocal && form.defer_remote_upload) {
      form.defer_remote_upload = false
    }
  })

  // describeSchedule and relTimeUntil imported from utils.js
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Backup Jobs</h1>
      <p class="text-sm text-text-muted mt-1">Manage your backup job configurations</p>
    </div>
    {#if !loading && jobs.length > 0}
      <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
        New Job
      </button>
    {/if}
  </div>

  {#if !loading && jobs.length > 0}
    <!-- Find / filter / sort toolbar -->
    <div class="flex flex-wrap items-center gap-2 mb-3">
      <div class="relative flex-1 min-w-[12rem]">
        <svg aria-hidden="true" class="w-4 h-4 absolute left-2.5 top-1/2 -translate-y-1/2 text-text-dim pointer-events-none" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/></svg>
        <input type="search" bind:value={search} placeholder="Search jobs by name or description"
          aria-label="Search jobs"
          class="w-full pl-8 pr-3 py-1.5 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim focus:outline-none focus:ring-1 focus:ring-vault focus:border-vault" />
      </div>
      <!-- Status segmented control -->
      <div class="flex items-center rounded-lg border border-border bg-surface-3 p-0.5 text-xs">
        {#each [['all', 'All'], ['enabled', 'Enabled'], ['disabled', 'Disabled']] as [val, label] (val)}
          <button type="button" onclick={() => statusFilter = val}
            class="px-2.5 py-1 rounded-md font-medium transition-colors cursor-pointer {statusFilter === val ? 'bg-vault text-white' : 'text-text-muted hover:text-text'}">
            {label}
          </button>
        {/each}
      </div>
      <select bind:value={storageFilter} aria-label="Filter by storage destination"
        class="px-2.5 py-1.5 bg-surface-3 border border-border rounded-lg text-xs text-text focus:outline-none focus:ring-1 focus:ring-vault focus:border-vault cursor-pointer">
        <option value={0}>All storage</option>
        {#each storageList as s (s.id)}
          <option value={s.id}>{s.name}</option>
        {/each}
      </select>
      <select bind:value={sortBy} aria-label="Sort jobs"
        class="px-2.5 py-1.5 bg-surface-3 border border-border rounded-lg text-xs text-text focus:outline-none focus:ring-1 focus:ring-vault focus:border-vault cursor-pointer">
        <option value="name">Name (A–Z)</option>
        <option value="next">Next run</option>
        <option value="created">Recently created</option>
      </select>
    </div>
    <div class="flex items-center gap-3 mb-3">
      <label class="flex items-center gap-2 text-xs text-text-muted cursor-pointer select-none">
        <input type="checkbox" checked={allSelected} onchange={toggleSelectAll}
          class="accent-vault w-3.5 h-3.5" />
        Select all ({selectedVisibleCount}/{filteredJobs.length})
      </label>
      <span class="text-xs text-text-dim">
        Showing {filteredJobs.length} of {jobs.length}
      </span>
      {#if filtersActive}
        <button type="button" onclick={clearFilters}
          class="text-xs text-vault hover:underline cursor-pointer">Clear filters</button>
      {/if}
    </div>
  {/if}

  {#if loading}
    <Skeleton variant="card" count={3} />
  {:else if jobs.length === 0}
    <EmptyState title="No backup jobs" subtitle="Step 1: Set up a job" description="Create your first backup job to get started." actionLabel="Create Job" onaction={() => openCreate()}>
      {#snippet iconSlot()}
        <svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4"/></svg>
      {/snippet}
    </EmptyState>
  {:else if filteredJobs.length === 0}
    <div class="text-center py-12">
      <div class="mb-3 opacity-30 flex justify-center"><svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/></svg></div>
      <p class="text-sm text-text-muted">No jobs match these filters.</p>
      <button type="button" onclick={clearFilters} class="mt-2 text-sm text-vault hover:underline cursor-pointer">Clear filters</button>
    </div>
  {:else}
    <div class="space-y-3 stagger">
      {#each filteredJobs as job (job.id)}
        <div class="bg-surface-2 border border-border rounded-xl p-5 hover:border-vault/30 hover:shadow-sm transition-all {selectedJobs.has(job.id) ? 'ring-1 ring-vault/40' : ''}">
          <div class="flex items-start justify-between">
            <div class="flex-1 min-w-0">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                <!-- Bulk checkbox -->
                <input
                  type="checkbox"
                  checked={selectedJobs.has(job.id)}
                  onchange={() => toggleSelectJob(job.id)}
                  onclick={(e) => e.stopPropagation()}
                  class="accent-vault w-3.5 h-3.5 shrink-0 cursor-pointer"
                />
                <!-- Toggle switch -->
                <button
                  onclick={(e) => { e.stopPropagation(); toggleEnabled(job) }}
                  class="relative w-9 h-5 rounded-full transition-colors shrink-0 {job.enabled ? 'bg-vault' : 'bg-surface-4'}"
                  title={job.enabled ? 'Disable job' : 'Enable job'}
                  role="switch"
                  aria-checked={job.enabled}
                >
                  <span class="absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform {job.enabled ? 'translate-x-4' : ''}"></span>
                </button>
                <!-- Inline editable name -->
                {#if editingNameId === job.id}
                  <!-- svelte-ignore a11y_autofocus -->
                  <input
                    type="text" bind:value={editName}
                    onkeydown={(e) => { if (e.key === 'Enter') saveJobName(job); if (e.key === 'Escape') editingNameId = null }}
                    onblur={() => saveJobName(job)}
                    class="text-sm font-semibold bg-transparent border-b border-vault outline-none text-text w-full max-w-xs"
                    autofocus
                  />
                {:else}
                  <h2 ondblclick={() => startNameEdit(job)} class="text-sm font-semibold text-text truncate cursor-text" title="Double-click to rename">
                    {job.name}
                  </h2>
                {/if}
                <!-- Anomaly badge for this job (hidden when anomaly detection is off) -->
                {#if getAnomalyEnabled() && jobAnomalyCount(job.id) > 0}
                  <AnomalyBadge count={jobAnomalyCount(job.id)} severity={jobWorstSeverity(job.id)} />
                {/if}
                <!-- Missing-item remediation pill (#119) -->
                {#if jobStaleCount(job.id) > 0}
                  <button
                    type="button"
                    onclick={() => openRemediate(job)}
                    class="flex items-center gap-1 text-[11px] px-2 py-0.5 rounded-full bg-amber-500/15 text-amber-500 font-medium shrink-0 hover:bg-amber-500/25 transition-colors"
                    title="Some items in this job no longer exist on the system"
                  >
                    <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/></svg>
                    {jobStaleCount(job.id)} missing
                  </button>
                {/if}
                <!-- Baseline learning indicator (hidden when anomaly detection is off) -->
                {#if getAnomalyEnabled() && baselineSamples(job.id) < 10}
                  <span class="text-[11px] px-2 py-0.5 rounded-full bg-surface-4 text-text-dim font-medium shrink-0">
                    Learning baseline ({baselineSamples(job.id)}/10)
                  </span>
                {/if}
              </div>
              {#if job.description}
                <p class="text-xs text-text-dim mt-1">{job.description}</p>
              {/if}
              <div class="flex flex-wrap gap-x-4 gap-y-1 mt-2 text-xs text-text-muted">
                <span class="flex items-center gap-1" class:text-text-dim={!job.schedule}>
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                  {describeSchedule(job.schedule)}
                </span>
                {#if nextRuns[String(job.id)]}
                  <span class="flex items-center gap-1 text-vault font-medium">
                    <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
                    Next: {relTimeUntil(nextRuns[String(job.id)])}
                  </span>
                {/if}
                <span class="flex items-center gap-1">
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7M4 7c0 2.21 3.582 4 8 4s8-1.79 8-4M4 7c0-2.21 3.582-4 8-4s8 1.79 8 4"/></svg>
                  {getStorageName(job.storage_dest_id)}
                </span>
                <span class="flex items-center gap-1">
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
                  {job.compression || 'none'} · {job.backup_type_chain || 'full'}{#if job.encryption === 'age'} · <svg aria-hidden="true" class="w-3 h-3 inline-block align-text-bottom text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/></svg>{/if}
                </span>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0 ml-4">
              <button
                onclick={() => runNow(job)}
                disabled={runningJob === job.id}
                class="p-2 text-text-muted hover:text-vault hover:bg-vault/10 rounded-lg transition-colors"
                title="Run Now"
                aria-label="Run backup now"
              >
                {#if runningJob === job.id}
                  <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                {:else}
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"/><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                {/if}
              </button>
              <button onclick={() => duplicateJob(job)} class="p-2 text-text-muted hover:text-vault hover:bg-vault/10 rounded-lg transition-colors" title="Duplicate" aria-label="Duplicate job">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
              </button>
              <button onclick={() => openEdit(job.id)} class="p-2 text-text-muted hover:text-text hover:bg-surface-3 rounded-lg transition-colors" title="Edit" aria-label="Edit job">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/></svg>
              </button>
              <button onclick={() => deleteJob(job.id, job.name)} class="p-2 text-text-muted hover:text-danger hover:bg-danger/10 rounded-lg transition-colors" title="Delete" aria-label="Delete job">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
              </button>
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Bulk action bar -->
{#if selectedJobs.size > 0}
  <div class="fixed bottom-6 left-1/2 -translate-x-1/2 z-50 bg-surface-2 border border-border rounded-xl shadow-2xl px-5 py-3 flex items-center gap-4">
    <span class="text-sm font-medium text-text">{selectedJobs.size} selected</span>
    <div class="w-px h-6 bg-border"></div>
    <button
      onclick={() => bulkEnable(true)}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-success hover:bg-success/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
      Enable
    </button>
    <button
      onclick={() => bulkEnable(false)}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-warning hover:bg-warning/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636"/></svg>
      Disable
    </button>
    <button
      onclick={bulkRun}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-vault hover:bg-vault/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"/></svg>
      Run
    </button>
    <button
      onclick={bulkDelete}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-danger hover:bg-danger/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
      Delete
    </button>
    <div class="w-px h-6 bg-border"></div>
    <button
      onclick={() => { selectedJobs = new SvelteSet() }}
      class="text-xs text-text-muted hover:text-text transition-colors"
    >
      Clear
    </button>
  </div>
{/if}
<Modal show={showModal} title={editing ? 'Edit Job' : 'Create Backup Job'} size="lg" onclose={() => showModal = false}>
  {#snippet stepper()}
    <!-- Step indicator – rendered outside the scrollable body so it stays always visible -->
    <div class="flex items-center gap-1 sm:gap-2">
      {#each [{n:1, label:'Type'}, {n:2, label:'Items'}, {n:3, label:'Schedule'}, {n:4, label:'Details'}, {n:5, label:'Advanced'}, {n:6, label:'Review'}] as s (s.n)}
        <button
          type="button"
          onclick={() => { if (s.n < step || canNext) step = s.n }}
          class="flex items-center gap-2 {s.n === step ? '' : 'opacity-60'}"
        >
          <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n < step ? 'bg-vault text-white' : s.n === step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
            {#if s.n < step}
              <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
            {:else}
              {s.n}
            {/if}
          </div>
          <span class="text-xs font-medium {s.n === step ? 'text-text' : 'text-text-muted'} hidden sm:inline">{s.label}</span>
        </button>
        {#if s.n < totalSteps}
          <div class="flex-1 h-px {s.n < step ? 'bg-vault' : 'bg-border'}"></div>
        {/if}
      {/each}
    </div>
  {/snippet}

  <form onsubmit={(e) => { e.preventDefault(); if (step < totalSteps) step++; else saveJob() }}>
    <!-- Step 1: Choose Backup Types -->
    {#if step === 1}
      <div class="space-y-4">
        <TypePicker bind:selectedTypes={form.selectedTypes} />
      </div>

    <!-- Step 2: Select Items -->
    {:else if step === 2}
      <div class="space-y-4">
        <p class="text-sm text-text-muted">Select the specific items to include in this backup job.</p>
        <ItemPicker bind:items={form.items} allowedTypes={form.selectedTypes} />
      </div>

    <!-- Step 3: Schedule & Configuration -->
    {:else if step === 3}
      <div class="space-y-5">
        <div>
          <span class="block text-sm font-medium text-text-muted mb-1.5">Schedule</span>
          <ScheduleBuilder bind:value={form.schedule} />
        </div>

        <div>
          <label for="storage" class="block text-sm font-medium text-text-muted mb-1.5">Storage Destination</label>
          <select id="storage" bind:value={form.storage_dest_id}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
            <option value={0}>– Select –</option>
            {#each storageList as s (s.id)}
              <option value={s.id}>{s.name} ({s.type})</option>
            {/each}
          </select>
          {#if storageList.length === 0}
            <div class="mt-2 bg-vault/5 border border-vault/30 rounded-lg p-4">
              <div class="flex items-start gap-3">
                <svg aria-hidden="true" class="w-5 h-5 text-vault shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                <div>
                  <p class="text-sm font-medium text-text">No storage destinations configured</p>
                  <p class="text-xs text-text-muted mt-0.5">You need at least one storage destination before creating a backup job.</p>
                  <button
                    type="button"
                    onclick={() => { showModal = false; navigate('/storage') }}
                    class="mt-2 inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors"
                  >
                    <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
                    Configure Storage
                  </button>
                </div>
              </div>
            </div>
          {/if}
        </div>

        <div>
          <span class="block text-sm font-medium text-text-muted mb-1.5">Backup Mode</span>
          <BackupModeSelector
            bind:containerMode={form.container_mode}
            bind:vmMode={form.vm_mode}
            {hasContainers}
            {hasVMs}
          />
        </div>

        <div class="grid grid-cols-2 gap-4">
          <div>
            <label for="backup_type" class="block text-sm font-medium text-text-muted mb-1.5">Backup Type</label>
            <select id="backup_type" bind:value={form.backup_type_chain}
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
              <option value="full">Full</option>
              <option value="incremental">Incremental</option>
              <option value="differential">Differential</option>
            </select>
            <p class="text-xs text-text-dim mt-1">
              {form.backup_type_chain === 'full' ? 'Backs up everything every time. Largest but most reliable.' :
               form.backup_type_chain === 'incremental' ? 'Only backs up changes since last backup. Fastest and smallest.' :
               form.backup_type_chain === 'differential' ? 'Backs up changes since last full backup. Balance of speed and safety.' : ''}
            </p>
          </div>
          <div>
            <label for="compression" class="block text-sm font-medium text-text-muted mb-1.5">Compression</label>
            <select id="compression" bind:value={form.compression}
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
              <option value="none">None</option>
              <option value="gzip">Gzip</option>
              <option value="zstd">Zstandard (recommended)</option>
            </select>
            <p class="text-xs text-text-dim mt-1">
              {form.compression === 'none' ? 'No compression – fastest backup with zero CPU overhead but largest size. Best for pre-compressed content like media files or already-encrypted blobs.' :
               form.compression === 'gzip' ? 'Universal compatibility, moderate compression ratio and speed. Good when archives need to be opened by other tools.' :
               form.compression === 'zstd' ? 'Best all-rounder: better compression than gzip and roughly 3–5× faster. Recommended for container images and large volumes.' : ''}
            </p>
          </div>
          <div>
            <label for="encryption" class="block text-sm font-medium text-text-muted mb-1.5">Encryption</label>
            <select id="encryption" bind:value={form.encryption}
              class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
              <option value="none">None</option>
              <option value="age">Age Encryption</option>
            </select>
            {#if form.encryption === 'age'}
              <p class="text-xs text-text-dim mt-1">Requires a global encryption passphrase in Settings.</p>
            {/if}
          </div>
        </div>
      </div>

    <!-- Step 4: Job Details -->
    {:else if step === 4}
      <div class="space-y-5">
        <div>
          <label for="name" class="block text-sm font-medium text-text-muted mb-1.5">Job Name <Tooltip text="Used for display and log identification. No strict naming constraints." /></label>
          <input id="name" type="text" bind:value={form.name} required
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim focus:border-vault focus:ring-1 focus:ring-vault" placeholder="Daily Docker Backup" />
          {#if suggestedName && !form.name.trim()}
            <button
              type="button"
              onclick={() => form.name = suggestedName}
              class="mt-1.5 text-xs text-vault hover:text-vault-dark transition-colors"
            >
              Suggestion: {suggestedName}
            </button>
          {/if}
        </div>

        <div>
          <label for="desc" class="block text-sm font-medium text-text-muted mb-1.5">Description <span class="text-text-dim">(optional)</span></label>
          <input id="desc" type="text" bind:value={form.description}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="Back up all production containers" />
        </div>
      </div>

    <!-- Step 5: Advanced Settings -->
    {:else if step === 5}
      <div class="space-y-5">
        <!-- Advanced: Retention -->
        <details class="group" open>
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Retention Policy <Tooltip text="'Keep Last N' retains only the most recent N backups. 'Keep For N Days' removes backups older than N days. Both limits apply – whichever triggers first will prune old backups." />
          </summary>
          <div class="grid grid-cols-2 gap-4 mt-3 pl-6">
            <div>
              <label for="retention_count" class="block text-xs font-medium text-text-muted mb-1">Keep Last</label>
              <div class="flex items-center gap-2">
                <input id="retention_count" type="number" bind:value={form.retention_count} min="1"
                  class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
                <span class="text-xs text-text-dim shrink-0">backups</span>
              </div>
            </div>
            <div>
              <label for="retention_days" class="block text-xs font-medium text-text-muted mb-1">Keep For</label>
              <div class="flex items-center gap-2">
                <input id="retention_days" type="number" bind:value={form.retention_days} min="1"
                  class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
                <span class="text-xs text-text-dim shrink-0">days</span>
              </div>
            </div>
          </div>
        </details>

        <!-- Advanced: Long-Term Retention (LTR) -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Long-Term Retention (LTR) <Tooltip text="Keep N latest, plus the most recent backup per day/week/month/year. A single backup can fill multiple buckets. If any of these is > 0, the simple Retention Policy above is ignored for this job." />
          </summary>
          <div class="grid grid-cols-2 sm:grid-cols-5 gap-3 mt-3 pl-6">
            <div>
              <label for="keep_latest" class="block text-xs font-medium text-text-muted mb-1">Latest</label>
              <input id="keep_latest" type="number" bind:value={form.keep_latest} min="0" placeholder="0"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
            </div>
            <div>
              <label for="keep_daily" class="block text-xs font-medium text-text-muted mb-1">Daily</label>
              <input id="keep_daily" type="number" bind:value={form.keep_daily} min="0" placeholder="0"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
            </div>
            <div>
              <label for="keep_weekly" class="block text-xs font-medium text-text-muted mb-1">Weekly</label>
              <input id="keep_weekly" type="number" bind:value={form.keep_weekly} min="0" placeholder="0"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
            </div>
            <div>
              <label for="keep_monthly" class="block text-xs font-medium text-text-muted mb-1">Monthly</label>
              <input id="keep_monthly" type="number" bind:value={form.keep_monthly} min="0" placeholder="0"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
            </div>
            <div>
              <label for="keep_yearly" class="block text-xs font-medium text-text-muted mb-1">Yearly</label>
              <input id="keep_yearly" type="number" bind:value={form.keep_yearly} min="0" placeholder="0"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" />
            </div>
          </div>
          {#if ltrActive}
            <p class="mt-2 pl-6 text-xs text-warning">Long-Term Retention is active – the simple Retention Policy above is ignored for this job.</p>
            {#if editing}
              <div class="mt-3 pl-6 text-xs">
                {#if retentionPreviewLoading}
                  <span class="text-text-muted">Calculating preview…</span>
                {:else if retentionPreview}
                  <div class="bg-surface-3/50 border border-border rounded-lg p-3">
                    <p class="text-text font-medium">
                      Would keep {retentionPreview.kept_with_ancestors.length} of {retentionPreview.total_restore_points} current restore points
                    </p>
                    <p class="text-text-muted mt-1">
                      {retentionPreview.would_delete.length} would be pruned · {retentionPreview.kept_directly.length} kept directly · {retentionPreview.kept_with_ancestors.length - retentionPreview.kept_directly.length} kept as chain ancestors
                    </p>
                  </div>
                {:else if retentionPreviewError}
                  <span class="text-danger">Preview unavailable: {retentionPreviewError}</span>
                {/if}
              </div>
            {:else}
              <p class="mt-2 pl-6 text-xs text-text-dim">Save the job first to see how many existing restore points this policy would keep.</p>
            {/if}
          {/if}
        </details>

        <!-- Advanced: Scheduled verification (Feature A) -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Scheduled verification <Tooltip text="Periodically checks that your latest backup is still intact on its destination. Quick: confirms the files exist at the right size (no download). Deep: re-downloads and checksums everything to catch silent corruption. Recommended: Quick, or Deep for critical data." />
          </summary>
          <div class="mt-3 pl-6 space-y-3">
            <label class="flex items-center gap-2 text-sm text-text-muted">
              <input type="checkbox" checked={verifyEnabled} onchange={toggleVerifyEnabled} class="accent-vault" />
              Run verification on a schedule
            </label>
            {#if verifyEnabled}
              <div class="bg-surface-3/50 border border-border rounded-lg p-3">
                <ScheduleBuilder bind:value={form.verify_schedule} />
              </div>
              <div>
                <label for="verify_mode" class="block text-xs font-medium text-text-muted mb-1">Mode</label>
                <select id="verify_mode" bind:value={form.verify_mode}
                  class="w-full sm:w-auto px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
                  <option value="quick">Quick (HEAD + size, no bandwidth)</option>
                  <option value="deep">Deep (full SHA-256 reread)</option>
                </select>
              </div>
            {/if}
          </div>
        </details>

        <!-- Advanced: Scripts -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Scripts <Tooltip text="Pre-backup scripts run before any containers are stopped. Post-backup scripts run after all items are backed up. Environment variables like VAULT_JOB_NAME and VAULT_STATUS are available." />
          </summary>
          <div class="space-y-4 mt-3 pl-6">
            <ScriptBrowser bind:value={form.pre_script} label="Pre-Backup Script" placeholder="/path/to/script.sh" />
            <ScriptBrowser bind:value={form.post_script} label="Post-Backup Script" placeholder="/path/to/script.sh" />
          </div>
        </details>

        <!-- Advanced: Notifications -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Notifications <Tooltip text="Controls when this job sends Unraid system notifications. The global notifications toggle in Settings must be enabled for any to be sent. Discord notifications are configured separately in Settings." />
          </summary>
          <div class="mt-3 pl-6">
            <label for="notify_on" class="block text-xs font-medium text-text-muted mb-1">Notify On</label>
            <select
              id="notify_on"
              bind:value={form.notify_on}
              class="w-full sm:w-auto px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="always">All backups (success &amp; failure)</option>
              <option value="failure">Failures only</option>
              <option value="never">Never</option>
            </select>
          </div>
        </details>

        <!-- Advanced: Verification -->
        <details class="group" open>
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Backup Verification <Tooltip text="Right after each backup, re-reads the archive and checksums it to confirm nothing was corrupted in transit. Adds time but catches problems while you can still re-run. Recommended: on for important jobs." />
          </summary>
          <div class="mt-3 pl-6">
            <div class="flex items-start gap-3">
              <label class="relative inline-flex items-center cursor-pointer mt-0.5">
                <input type="checkbox" bind:checked={form.verify_backup} class="sr-only peer" />
                <div class="w-9 h-5 bg-surface-4 peer-checked:bg-vault rounded-full peer peer-focus:ring-2 peer-focus:ring-vault/50 after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full"></div>
              </label>
              <div>
                <p class="text-sm text-text">Verify backups after writing</p>
                <p class="text-xs text-text-dim mt-0.5">Reads each file back from storage and validates SHA-256 checksums. Adds a small amount of time but ensures backup integrity.</p>
              </div>
            </div>
          </div>
        </details>

        <!-- Advanced: Retry policy override (Task 11 – resilience hardening) -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Override retry policy <Tooltip text="Per-job retry settings that override the global defaults in Settings. Leave blank to use the global values. Failed runs are retried in the background after each delay." />
          </summary>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-3 pl-6">
            <div>
              <label for="retry-max-override" class="block text-xs font-medium text-text-muted mb-1">Max retries <span class="text-text-dim">(blank = global)</span></label>
              <input
                id="retry-max-override"
                type="number"
                min="0"
                max="10"
                bind:value={form.retry_max_override}
                placeholder="Global default"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim"
              />
            </div>
            <div>
              <div class="block text-xs font-medium text-text-muted mb-1">Delays between retries <span class="text-text-dim">(blank = global)</span></div>
              <RetryDelaysEditor bind:value={form.retry_delays_override} placeholder="Use global default" />
            </div>
          </div>
        </details>

        <!-- Advanced: Anomaly sensitivity override (Task 19) – only when anomaly detection is enabled -->
        {#if getAnomalyEnabled()}
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Anomaly sensitivity <Tooltip text="Override the global anomaly detection sensitivity for this job. '(default)' uses the value configured in Settings → General." />
          </summary>
          <div class="mt-3 pl-6">
            <label for="anomaly-sensitivity-override" class="block text-xs font-medium text-text-muted mb-1">
              Sensitivity <span class="text-text-dim">(blank = global default)</span>
            </label>
            <select
              id="anomaly-sensitivity-override"
              bind:value={form.anomaly_sensitivity}
              class="w-full sm:w-auto px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="">(default)</option>
              <option value="strict">Strict – flag small deviations</option>
              <option value="balanced">Balanced</option>
              <option value="permissive">Permissive – flag large deviations only</option>
            </select>
          </div>
        </details>
        {/if}

        <!-- Advanced: Max parallel uploads (Task 12 – storage resilience) -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Upload concurrency <Tooltip text="How many files upload to the destination at once. Higher is faster on fast links; set to 1 for fragile or rate-limited remotes." />
          </summary>
          <div class="mt-3 pl-6">
            <label for="job_parallel" class="block text-xs font-medium text-text-muted mb-1">
              Max parallel uploads <span class="text-text-dim">(1–16)</span>
            </label>
            <input
              id="job_parallel"
              type="number"
              min="1"
              max="16"
              bind:value={form.max_parallel_uploads}
              class="w-full sm:w-32 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
            />
            <p class="text-xs text-text-dim mt-1">Higher values are faster on reliable high-bandwidth links. Lower to 1 for slow or rate-limited remotes to avoid dropped connections.</p>
          </div>
        </details>

        <!-- Advanced: Deferred remote upload (#77) -->
        {#if true}
          {@const _selectedStorage = storageList.find(s => s.id === form.storage_dest_id)}
          {@const _isLocalDest = !_selectedStorage || _selectedStorage.type === 'local'}
          <details class="group">
            <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
              <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              Deferred Remote Upload <Tooltip text="Best for slow upload links. Stops, backs up, and restarts each container locally first, then uploads everything to the remote destination after all containers are running again. Requires sufficient local staging space." />
            </summary>
            <div class="mt-3 pl-6">
              <div class="flex items-start gap-3">
                <label class="relative inline-flex items-center cursor-pointer mt-0.5" class:opacity-50={_isLocalDest} class:cursor-not-allowed={_isLocalDest} title={_isLocalDest ? 'Only available when destination is remote' : ''}>
                  <input type="checkbox" bind:checked={form.defer_remote_upload} disabled={_isLocalDest} class="sr-only peer" />
                  <div class="w-9 h-5 bg-surface-4 peer-checked:bg-vault rounded-full peer peer-focus:ring-2 peer-focus:ring-vault/50 after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full"></div>
                </label>
                <div>
                  <p class="text-sm text-text">Defer remote upload until all backups complete</p>
                  <p class="text-xs text-text-dim mt-0.5">Backs up every container to local staging first and brings them all online before any data is sent to the remote destination. {_isLocalDest ? 'Only available when destination is remote (SFTP, SMB, NFS, S3, WebDAV).' : 'Per-file uploads are retried up to 3 times with exponential backoff (5s → 30s → 2m).'}</p>
                </div>
              </div>
            </div>
          </details>
        {/if}

        {#if hasVMs}
          <details class="group" open>
            <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
              <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              VM Restore Verification <Tooltip text="After restoring a VM that was running at backup time, Vault auto-starts it and checks that it came up successfully." />
            </summary>
            <div class="space-y-4 mt-3 pl-6">
              <p class="text-xs text-text-dim">Applies only when a VM was running at backup time and Vault auto-starts it after restore.</p>
              {#each selectedVMItems as vmItem (vmItem.item_name)}
                {@const verify = getVMRestoreVerifySettings(vmItem)}
                {@const verifyIdBase = `vm-restore-verify-${vmItem.sort_order ?? 0}`}
                <div class="bg-surface-3/50 border border-border rounded-lg p-4 space-y-4">
                  <div>
                    <p class="text-sm font-medium text-text">{vmItem.item_name}</p>
                    <p class="text-xs text-text-dim mt-0.5">Choose how Vault decides the VM is really ready after it starts.</p>
                  </div>

                  <div>
                    <label for={`${verifyIdBase}-mode`} class="block text-xs font-medium text-text-muted mb-1.5">Readiness Check <Tooltip text="Running State: quick check that the VM is powered on. QEMU Guest Agent: waits for the guest OS to respond. TCP Service: waits for a specific port to accept connections." /></label>
                    <select
                      id={`${verifyIdBase}-mode`}
                      value={verify.mode}
                      onchange={(e) => updateVMRestoreVerifySetting(vmItem.item_name, 'restore_verify_mode', e.currentTarget.value)}
                      class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
                    >
                      {#each vmRestoreVerifyModes as mode (mode.value)}
                        <option value={mode.value}>{mode.label}</option>
                      {/each}
                    </select>
                    <p class="text-xs text-text-dim mt-1">{vmRestoreVerifyModes.find((mode) => mode.value === verify.mode)?.description}</p>
                  </div>

                  {#if verify.mode !== 'running'}
                    <div class="grid gap-4 sm:grid-cols-2">
                      <div>
                        <label for={`${verifyIdBase}-timeout`} class="block text-xs font-medium text-text-muted mb-1.5">Timeout</label>
                        <div class="flex items-center gap-2">
                          <input
                            id={`${verifyIdBase}-timeout`}
                            type="number"
                            min="1"
                            value={verify.timeout}
                            oninput={(e) => updateVMRestoreVerifySetting(vmItem.item_name, 'restore_verify_timeout_seconds', e.currentTarget.value)}
                            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
                          />
                          <span class="text-xs text-text-dim shrink-0">seconds</span>
                        </div>
                      </div>

                      {#if verify.mode === 'tcp'}
                        <div>
                          <label for={`${verifyIdBase}-port`} class="block text-xs font-medium text-text-muted mb-1.5">TCP Port</label>
                          <input
                            id={`${verifyIdBase}-port`}
                            type="number"
                            min="1"
                            max="65535"
                            value={verify.port}
                            oninput={(e) => updateVMRestoreVerifySetting(vmItem.item_name, 'restore_verify_tcp_port', e.currentTarget.value)}
                            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
                            placeholder="8123"
                          />
                        </div>

                        <div class="sm:col-span-2">
                          <label for={`${verifyIdBase}-host`} class="block text-xs font-medium text-text-muted mb-1.5">Host <span class="text-text-dim">(optional)</span></label>
                          <input
                            id={`${verifyIdBase}-host`}
                            type="text"
                            value={verify.host}
                            oninput={(e) => updateVMRestoreVerifySetting(vmItem.item_name, 'restore_verify_tcp_host', e.currentTarget.value)}
                            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text"
                            placeholder="Auto-detect from libvirt"
                          />
                          <p class="text-xs text-text-dim mt-1">Leave blank to let Vault ask libvirt for the guest address. Set an explicit host when the VM uses a fixed address or libvirt cannot report it.</p>
                        </div>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          </details>
        {/if}

        {#if hasContainers}
          <details class="group">
            <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
              <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              Container Mounts &amp; Exclusions
            </summary>
            <div class="space-y-4 mt-3 pl-6">
              <p class="text-xs text-text-dim">Choose which mount points each container backs up. Uncheck a mount to exclude its data (e.g. media or downloads). Mounts Vault auto-skips are shown disabled with the reason.</p>
              {#each selectedContainerItems as cItem (cItem.item_name)}
                {@const currentExclusions = getContainerExclusionPaths(cItem)}
                {@const preset = containerPresets[cItem.item_name]}
                {@const allPresetLoaded = preset ? preset.every(p => currentExclusions.includes(p)) : false}
                {@const mounts = containerMounts[cItem.item_name]}
                <div class="bg-surface-3/50 border border-border rounded-lg p-4 space-y-3">
                  <p class="text-sm font-medium text-text">{cItem.item_name}</p>

                  <!-- Mount point toggles -->
                  {#if mounts === undefined}
                    <p class="text-xs text-text-dim italic">Loading mount points…</p>
                  {:else if mounts.length === 0}
                    <p class="text-xs text-text-dim italic">No bind mounts detected for this container.</p>
                  {:else}
                    <div class="space-y-1.5">
                      {#each mounts as mount (mount.destination)}
                        {@const included = !mount.auto_skip && !isMountWholeExcluded(cItem, mount.destination)}
                        <label class="flex items-start gap-2.5 py-1 {mount.auto_skip ? 'opacity-60' : 'cursor-pointer'}">
                          <input
                            type="checkbox"
                            class="mt-0.5 accent-vault"
                            checked={included}
                            disabled={mount.auto_skip}
                            onchange={(e) => toggleExcludedMount(cItem.item_name, mount.destination, e.currentTarget.checked)}
                          />
                          <span class="min-w-0 flex-1">
                            <span class="flex items-center gap-2 flex-wrap">
                              <span class="text-sm font-mono text-text">{mount.destination}</span>
                              {#if mount.auto_skip}
                                <span class="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-surface-4 text-text-dim border border-border">auto-excluded</span>
                              {/if}
                            </span>
                            <span class="block text-xs text-text-dim font-mono truncate">{mount.source}{#if mount.auto_skip && mount.skip_reason} · {mount.skip_reason}{/if}</span>
                          </span>
                        </label>
                      {/each}
                    </div>
                  {/if}

                  <!-- Additional path exclusions (globs / subpaths) -->
                  <div class="space-y-2 pt-1 border-t border-border/60">
                    <p class="text-xs font-medium text-text-muted">Additional path exclusions</p>
                    <p class="text-xs text-text-dim">For subpaths within an included mount or glob patterns. One per line, e.g. /config/Cache or *.log.</p>
                    {#if preset}
                      <button
                        type="button"
                        disabled={allPresetLoaded}
                        onclick={() => {
                          const merged = [...new Set([...currentExclusions, ...preset])]
                          updateContainerExclusionPaths(cItem.item_name, merged)
                        }}
                        class="text-xs px-3 py-1.5 rounded-lg border transition-colors {allPresetLoaded ? 'border-green-500/30 text-green-400 bg-green-500/10 cursor-default' : 'border-vault/30 text-vault hover:bg-vault/10 cursor-pointer'}"
                      >
                        {allPresetLoaded ? 'Recommended exclusions loaded' : 'Load recommended exclusions'}
                      </button>
                    {/if}

                    <textarea
                      value={currentExclusions.join('\n')}
                      oninput={(e) => {
                        const paths = e.currentTarget.value.split('\n').map(p => p.trim()).filter(Boolean)
                        updateContainerExclusionPaths(cItem.item_name, paths)
                      }}
                      placeholder={`/config/Cache
*.log`}
                      rows="3"
                      class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono resize-y placeholder:text-text-dim/50"
                    ></textarea>
                  </div>
                </div>
              {/each}
            </div>
          </details>
        {/if}

        {#if !hasVMs && !hasContainers}
          <p class="text-sm text-text-dim italic">No additional advanced settings for the selected items.</p>
        {/if}
      </div>

    <!-- Step 6: Review & Create -->
    {:else}
      <div class="space-y-5">
        <!-- Summary card -->
        <div class="bg-surface-3/50 border border-border rounded-lg p-4 space-y-3 text-sm">
          <div class="flex justify-between">
            <span class="text-text-muted">Job Name</span>
            <span class="text-text font-medium">{form.name}</span>
          </div>
          {#if form.description}
            <div class="flex justify-between">
              <span class="text-text-muted">Description</span>
              <span class="text-text">{form.description}</span>
            </div>
          {/if}
          <div class="flex justify-between">
            <span class="text-text-muted">Items</span>
            <span class="text-text">
              {#if hasContainers}{form.items.filter(i => i.item_type === 'container').length} container{form.items.filter(i => i.item_type === 'container').length !== 1 ? 's' : ''}{/if}
              {#if hasContainers && (hasVMs || hasFolders || hasFlash || hasPlugins || hasZFS)}, {/if}
              {#if hasVMs}{form.items.filter(i => i.item_type === 'vm').length} VM{form.items.filter(i => i.item_type === 'vm').length !== 1 ? 's' : ''}{/if}
              {#if hasVMs && (hasFolders || hasFlash || hasPlugins || hasZFS)}, {/if}
              {#if hasFolders}{form.items.filter(i => i.item_type === 'folder' && parseItemSettings(i).preset !== 'flash').length} folder{form.items.filter(i => i.item_type === 'folder' && parseItemSettings(i).preset !== 'flash').length !== 1 ? 's' : ''}{/if}
              {#if hasFolders && (hasFlash || hasPlugins || hasZFS)}, {/if}
              {#if hasFlash}{@const flashDriveCount = form.items.filter(i => i.item_type === 'folder' && parseItemSettings(i).preset === 'flash').length}{flashDriveCount} flash drive{flashDriveCount !== 1 ? 's' : ''}{/if}
              {#if hasFlash && (hasPlugins || hasZFS)}, {/if}
              {#if hasPlugins}{form.items.filter(i => i.item_type === 'plugin').length} plugin{form.items.filter(i => i.item_type === 'plugin').length !== 1 ? 's' : ''}{/if}
              {#if hasPlugins && hasZFS}, {/if}
              {#if hasZFS}{form.items.filter(i => i.item_type === 'zfs').length} ZFS dataset{form.items.filter(i => i.item_type === 'zfs').length !== 1 ? 's' : ''}{/if}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Schedule</span>
            <span class="text-text">{describeSchedule(form.schedule)}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Storage</span>
            <span class="text-text">{getStorageName(form.storage_dest_id)}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Backup Type</span>
            <span class="text-text capitalize">{form.backup_type_chain}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Compression</span>
            <span class="text-text capitalize">{form.compression}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Encryption</span>
            <span class="text-text capitalize">{form.encryption === 'age' ? 'Age (encrypted)' : 'None'}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Retention</span>
            {#if (form.keep_latest || 0) + (form.keep_daily || 0) + (form.keep_weekly || 0) + (form.keep_monthly || 0) + (form.keep_yearly || 0) > 0}
              <span class="text-text">
                LTR: {form.keep_latest || 0} latest / {form.keep_daily || 0} daily / {form.keep_weekly || 0} weekly / {form.keep_monthly || 0} monthly / {form.keep_yearly || 0} yearly
              </span>
            {:else}
              <span class="text-text">{form.retention_count} backups / {form.retention_days} days</span>
            {/if}
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Verification</span>
            <span class="text-text">{form.verify_backup ? 'Enabled' : 'Disabled'}</span>
          </div>
          {#if form.pre_script || form.post_script}
            <div class="flex justify-between">
              <span class="text-text-muted">Scripts</span>
              <span class="text-text">{[form.pre_script ? 'Pre' : '', form.post_script ? 'Post' : ''].filter(Boolean).join(' + ')}</span>
            </div>
          {/if}
        </div>

        <!-- Enabled toggle -->
        <div class="flex items-center gap-3">
          <label class="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" bind:checked={form.enabled} class="sr-only peer" />
            <div class="w-9 h-5 bg-surface-4 peer-checked:bg-vault rounded-full peer peer-focus:ring-2 peer-focus:ring-vault/50 after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full"></div>
          </label>
          <span class="text-sm text-text-muted">Enable scheduled execution</span>
        </div>
      </div>
    {/if}

    <!-- Navigation buttons -->
    <div class="flex flex-col gap-3 pt-5 mt-5 border-t border-border">
      {#if stepHint && !canNext}
        <p class="text-xs text-warning flex items-center gap-1.5">
          <svg aria-hidden="true" class="w-3.5 h-3.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/></svg>
          {stepHint}
        </p>
      {/if}
      <div class="flex items-center justify-between">
      <div>
        {#if step > 1}
          <button type="button" onclick={() => step--} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
            Back
          </button>
        {:else}
          <button type="button" onclick={() => showModal = false} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
            Cancel
          </button>
        {/if}
      </div>
      <div class="flex gap-2">
        {#if step < totalSteps}
          <button
            type="submit"
            disabled={!canNext}
            class="px-5 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            Next
          </button>
        {:else}
          <button
            type="button"
            disabled={saving || !canNext}
            onclick={() => saveJob(true)}
            class="px-4 py-2 text-sm font-medium text-vault border border-vault/50 hover:bg-vault/10 rounded-lg transition-colors disabled:opacity-40"
          >
            {editing ? 'Save & Run' : 'Create & Run Now'}
          </button>
          <button
            type="submit"
            disabled={saving || !canNext}
            class="px-5 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40"
          >
            {#if saving}Saving...{:else}{editing ? 'Save Changes' : 'Create Job'}{/if}
          </button>
        {/if}
      </div>
      </div>
    </div>
  </form>
</Modal>

<!-- Missing-item remediation dialog (#119) -->
<Modal
  show={!!remediating}
  title={remediating ? `Review missing items – ${remediating.name}` : 'Review missing items'}
  onclose={() => { remediating = null }}
>
  {#if remediating}
    {@const items = staleItems[remediating.id] || []}
    {#if items.length === 0}
      <p class="text-sm text-text-muted">No missing items.</p>
      <div class="flex justify-end mt-6">
        <button
          type="button"
          onclick={() => { remediating = null }}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors"
        >
          Close
        </button>
      </div>
    {:else}
      <p class="text-sm text-text-muted">
        These items no longer exist on this server. Removing one keeps its existing backups/restore points – it only stops future backups from trying to include it.
      </p>
      <ul class="mt-4 space-y-2">
        {#each items as item (item.id)}
          <li class="flex items-center justify-between gap-3 p-3 rounded-lg border border-border bg-surface-3">
            <div class="min-w-0">
              <p class="text-sm font-medium text-text truncate">{item.item_name}</p>
              <p class="text-xs text-text-dim mt-0.5 capitalize">{item.item_type}</p>
            </div>
            <button
              type="button"
              onclick={() => removeStaleItem(remediating, item)}
              disabled={remediateBusy}
              class="shrink-0 px-3 py-1.5 text-xs font-medium text-danger border border-danger/40 hover:bg-danger/10 rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Remove
            </button>
          </li>
        {/each}
      </ul>
      <div class="flex justify-end gap-3 mt-6">
        <button
          type="button"
          onclick={() => { remediating = null }}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors"
        >
          Close
        </button>
        <button
          type="button"
          onclick={() => removeAllStale(remediating)}
          disabled={remediateBusy}
          class="px-4 py-2 text-sm font-medium text-white bg-danger hover:bg-danger/90 rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        >
          Remove all
        </button>
      </div>
    {/if}
  {/if}
</Modal>

<svelte:window onkeydown={(e) => { if (e.key === 'Escape' && confirmDelete.show) confirmDelete = { show: false, id: 0, name: '', deleteFiles: false } }} />
{#if confirmDelete.show}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm animate-backdrop"
    role="dialog" aria-modal="true" aria-labelledby="delete-title" tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 p-6 animate-panel-up">
      <h2 id="delete-title" class="text-lg font-semibold text-text">Delete Job</h2>
      <p class="text-sm text-text-muted mt-2">Are you sure you want to delete <strong class="text-text">{confirmDelete.name}</strong>?</p>

      <div class="mt-4 space-y-2">
        <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors {!confirmDelete.deleteFiles ? 'border-vault bg-vault/5' : 'border-border hover:border-vault/30'}">
          <input type="radio" name="deleteMode" checked={!confirmDelete.deleteFiles}
            onchange={() => confirmDelete.deleteFiles = false}
            class="mt-0.5 accent-vault" />
          <div>
            <p class="text-sm font-medium text-text">Keep backup files</p>
            <p class="text-xs text-text-dim mt-0.5">Only remove the job from Vault. Backup files remain on storage and can be imported later.</p>
          </div>
        </label>
        <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors {confirmDelete.deleteFiles ? 'border-danger bg-danger/5' : 'border-border hover:border-danger/30'}">
          <input type="radio" name="deleteMode" checked={confirmDelete.deleteFiles}
            onchange={() => confirmDelete.deleteFiles = true}
            class="mt-0.5 accent-[var(--color-danger)]" />
          <div>
            <p class="text-sm font-medium text-text">Delete backup files</p>
            <p class="text-xs text-text-dim mt-0.5">Remove the job <strong class="text-danger">and permanently delete all backup files</strong> from storage. This cannot be undone.</p>
          </div>
        </label>
      </div>

      <div class="flex justify-end gap-3 mt-6">
        <button type="button" onclick={() => { confirmDelete = { show: false, id: 0, name: '', deleteFiles: false } }}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors">
          Cancel
        </button>
        <button type="button" onclick={doDeleteJob}
          class="px-4 py-2 text-sm font-medium rounded-lg transition-colors {confirmDelete.deleteFiles ? 'bg-danger text-white hover:bg-danger/90' : 'bg-danger text-white hover:bg-danger/90'}">
          {confirmDelete.deleteFiles ? 'Delete Job & Files' : 'Delete Job'}
        </button>
      </div>
    </div>
  </div>
{/if}
