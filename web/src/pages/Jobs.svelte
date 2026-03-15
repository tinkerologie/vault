<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { describeSchedule, relTimeUntil } from '../lib/utils.js'
  import Modal from '../components/Modal.svelte'
  import Toast from '../components/Toast.svelte'
  import Skeleton from '../components/Skeleton.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import ItemPicker from '../components/ItemPicker.svelte'
  import ScheduleBuilder from '../components/ScheduleBuilder.svelte'
  import BackupModeSelector from '../components/BackupModeSelector.svelte'
  import ScriptBrowser from '../components/ScriptBrowser.svelte'

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

  // Bulk selection
  let selectedJobs = $state(new SvelteSet())
  let bulkRunning = $state(false)

  let allSelected = $derived(jobs.length > 0 && selectedJobs.size === jobs.length)

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
      selectedJobs = new SvelteSet(jobs.map(j => j.id))
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
  const totalSteps = 3

  // Form state
  let form = $state(defaultForm())

  function defaultForm() {
    return {
      name: '',
      description: '',
      enabled: true,
      schedule: '0 2 * * *',
      backup_type_chain: 'full',
      retention_count: 5,
      retention_days: 30,
      compression: 'zstd',
      container_mode: 'one_by_one',
      vm_mode: 'snapshot',
      pre_script: '',
      post_script: '',
      notify_on: 'failure',
      verify_backup: true,
      encryption: 'none',
      storage_dest_id: 0,
      items: [],
    }
  }

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

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(() => {
    loadData()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_started' || msg.type === 'job_run_completed') {
        loadData()
      }
    })
    return unsub
  })

  async function loadData() {
    loading = true
    try {
      const [j, s, nr] = await Promise.all([api.listJobs(), api.listStorage(), api.getNextRuns()])
      jobs = j || []
      storageList = s || []
      nextRuns = nr || {}
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loading = false
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
        compression: data.job.compression || 'zstd',
        container_mode: data.job.container_mode || 'one_by_one',
        vm_mode: 'snapshot',
        pre_script: data.job.pre_script || '',
        post_script: data.job.post_script || '',
        notify_on: data.job.notify_on || 'failure',
        verify_backup: data.job.verify_backup ?? true,
        encryption: data.job.encryption || 'none',
        storage_dest_id: data.job.storage_dest_id || 0,
        items: data.items || [],
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
      delete payload.vm_mode
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
        pre_script: fullJob.pre_script || '',
        post_script: fullJob.post_script || '',
        notify_on: fullJob.notify_on || 'failure',
        verify_backup: fullJob.verify_backup ?? true,
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
    if (step === 1) return form.name.trim().length > 0 && form.items.length > 0
    if (step === 2) return form.storage_dest_id > 0
    return vmRestoreVerifyErrors.length === 0
  })

  let stepHint = $derived.by(() => {
    if (step === 1) {
      if (!form.name.trim()) return 'Enter a job name to continue'
      if (form.items.length === 0) return 'Select at least one item to back up'
    }
    if (step === 2 && form.storage_dest_id === 0) return 'Select a storage destination'
    if (step === 3 && vmRestoreVerifyErrors.length > 0) return vmRestoreVerifyErrors[0]
    return ''
  })

  let hasContainers = $derived(form.items.some(i => i.item_type === 'container'))
  let hasVMs = $derived(form.items.some(i => i.item_type === 'vm'))
  let hasFolders = $derived(form.items.some(i => i.item_type === 'folder'))
  let hasPlugins = $derived(form.items.some(i => i.item_type === 'plugin'))
  let selectedVMItems = $derived(form.items.filter(i => i.item_type === 'vm'))
  let vmRestoreVerifyErrors = $derived(selectedVMItems.map(getVMRestoreVerifyError).filter(Boolean))

  // describeSchedule and relTimeUntil imported from utils.js
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="flex items-center justify-between mb-6">
    <div>
      <h1 class="text-2xl font-bold text-text">Backup Jobs</h1>
      <p class="text-sm text-text-muted mt-1">Manage your backup job configurations</p>
    </div>
    <button onclick={openCreate} class="btn btn-primary flex items-center gap-2">
      <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
      New Job
    </button>
  </div>

  {#if !loading && jobs.length > 0}
    <div class="flex items-center gap-3 mb-3">
      <label class="flex items-center gap-2 text-xs text-text-muted cursor-pointer select-none">
        <input type="checkbox" checked={allSelected} onchange={toggleSelectAll}
          class="accent-vault w-3.5 h-3.5" />
        Select all ({selectedJobs.size}/{jobs.length})
      </label>
    </div>
  {/if}

  {#if loading}
    <Skeleton variant="card" count={3} />
  {:else if jobs.length === 0}
    <EmptyState icon="📋" title="No backup jobs" subtitle="Step 1: Set up a job" description="Create your first backup job to get started." actionLabel="Create Job" onaction={() => openCreate()} />
  {:else}
    <div class="space-y-3">
      {#each jobs as job (job.id)}
        <div class="bg-surface-2 border border-border rounded-xl p-5 hover:border-vault/30 transition-colors {selectedJobs.has(job.id) ? 'ring-1 ring-vault/40' : ''}">
          <div class="flex items-start justify-between">
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-3">
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
                  <h3 ondblclick={() => startNameEdit(job)} class="text-sm font-semibold text-text truncate cursor-text" title="Double-click to rename">
                    {job.name}
                  </h3>
                {/if}
              </div>
              {#if job.description}
                <p class="text-xs text-text-dim mt-1">{job.description}</p>
              {/if}
              <div class="flex flex-wrap gap-x-4 gap-y-1 mt-2 text-xs text-text-muted">
                <span class="flex items-center gap-1" class:text-text-dim={!job.schedule}>
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                  {describeSchedule(job.schedule)}
                </span>
                {#if nextRuns[String(job.id)]}
                  <span class="flex items-center gap-1 text-vault font-medium">
                    <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
                    Next: {relTimeUntil(nextRuns[String(job.id)])}
                  </span>
                {/if}
                <span class="flex items-center gap-1">
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7M4 7c0 2.21 3.582 4 8 4s8-1.79 8-4M4 7c0-2.21 3.582-4 8-4s8 1.79 8 4"/></svg>
                  {getStorageName(job.storage_dest_id)}
                </span>
                <span class="flex items-center gap-1">
                  <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
                  {job.compression || 'none'} · {job.backup_type_chain || 'full'}{#if job.encryption === 'age'} · 🔒{/if}
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
                  <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                {:else}
                  <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"/><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                {/if}
              </button>
              <button onclick={() => duplicateJob(job)} class="p-2 text-text-muted hover:text-vault hover:bg-vault/10 rounded-lg transition-colors" title="Duplicate" aria-label="Duplicate job">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
              </button>
              <button onclick={() => openEdit(job.id)} class="p-2 text-text-muted hover:text-text hover:bg-surface-3 rounded-lg transition-colors" title="Edit" aria-label="Edit job">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/></svg>
              </button>
              <button onclick={() => deleteJob(job.id, job.name)} class="p-2 text-text-muted hover:text-danger hover:bg-danger/10 rounded-lg transition-colors" title="Delete" aria-label="Delete job">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
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
      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
      Enable
    </button>
    <button
      onclick={() => bulkEnable(false)}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-warning hover:bg-warning/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636"/></svg>
      Disable
    </button>
    <button
      onclick={bulkRun}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-vault hover:bg-vault/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"/></svg>
      Run
    </button>
    <button
      onclick={bulkDelete}
      disabled={bulkRunning}
      class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-danger hover:bg-danger/10 rounded-lg transition-colors disabled:opacity-40"
    >
      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
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
<Modal show={showModal} title={editing ? 'Edit Job' : 'Create Backup Job'} onclose={() => showModal = false}>
  <!-- Step indicator -->
  <div class="flex items-center gap-2 mb-6">
    {#each [{n:1, label:'Items'}, {n:2, label:'Schedule'}, {n:3, label:'Review'}] as s (s.n)}
      <button
        type="button"
        onclick={() => { if (s.n < step || canNext) step = s.n }}
        class="flex items-center gap-2 {s.n === step ? '' : 'opacity-60'}"
      >
        <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n < step ? 'bg-vault text-white' : s.n === step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
          {#if s.n < step}
            <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
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

  <form onsubmit={(e) => { e.preventDefault(); if (step < totalSteps) step++; else saveJob() }}>
    <!-- Step 1: Name & Items -->
    {#if step === 1}
      <div class="space-y-5">
        <div>
          <label for="name" class="block text-sm font-medium text-text-muted mb-1.5">Job Name</label>
          <input id="name" type="text" bind:value={form.name} required
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim focus:border-vault focus:ring-1 focus:ring-vault" placeholder="Daily Docker Backup" />
        </div>

        <div>
          <label for="desc" class="block text-sm font-medium text-text-muted mb-1.5">Description <span class="text-text-dim">(optional)</span></label>
          <input id="desc" type="text" bind:value={form.description}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="Back up all production containers" />
        </div>

        <div>
          <span class="block text-sm font-medium text-text-muted mb-1.5">Select Items to Back Up</span>
          <ItemPicker bind:items={form.items} />
        </div>
      </div>

    <!-- Step 2: Schedule & Configuration -->
    {:else if step === 2}
      <div class="space-y-5">
        <div>
          <span class="block text-sm font-medium text-text-muted mb-1.5">Schedule</span>
          <ScheduleBuilder bind:value={form.schedule} />
        </div>

        <div>
          <label for="storage" class="block text-sm font-medium text-text-muted mb-1.5">Storage Destination</label>
          <select id="storage" bind:value={form.storage_dest_id}
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
            <option value={0}>— Select —</option>
            {#each storageList as s (s.id)}
              <option value={s.id}>{s.name} ({s.type})</option>
            {/each}
          </select>
          {#if storageList.length === 0}
            <div class="mt-2 bg-vault/5 border border-vault/30 rounded-lg p-4">
              <div class="flex items-start gap-3">
                <svg class="w-5 h-5 text-vault shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
                <div>
                  <p class="text-sm font-medium text-text">No storage destinations configured</p>
                  <p class="text-xs text-text-muted mt-0.5">You need at least one storage destination before creating a backup job.</p>
                  <button
                    type="button"
                    onclick={() => { showModal = false; navigate('/storage') }}
                    class="mt-2 inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors"
                  >
                    <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
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

    <!-- Step 3: Review & Advanced -->
    {:else}
      <div class="space-y-5">
        <!-- Summary card -->
        <div class="bg-surface-3/50 border border-border rounded-lg p-4 space-y-3 text-sm">
          <div class="flex justify-between">
            <span class="text-text-muted">Job Name</span>
            <span class="text-text font-medium">{form.name}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Items</span>
            <span class="text-text">
              {#if hasContainers}{form.items.filter(i => i.item_type === 'container').length} container{form.items.filter(i => i.item_type === 'container').length !== 1 ? 's' : ''}{/if}
              {#if hasContainers && (hasVMs || hasFolders || hasPlugins)}, {/if}
              {#if hasVMs}{form.items.filter(i => i.item_type === 'vm').length} VM{form.items.filter(i => i.item_type === 'vm').length !== 1 ? 's' : ''}{/if}
              {#if hasVMs && (hasFolders || hasPlugins)}, {/if}
              {#if hasFolders}{form.items.filter(i => i.item_type === 'folder').length} folder{form.items.filter(i => i.item_type === 'folder').length !== 1 ? 's' : ''}{/if}
              {#if hasFolders && hasPlugins}, {/if}
              {#if hasPlugins}{form.items.filter(i => i.item_type === 'plugin').length} plugin{form.items.filter(i => i.item_type === 'plugin').length !== 1 ? 's' : ''}{/if}
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
            <span class="text-text-muted">Compression</span>
            <span class="text-text capitalize">{form.compression}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Encryption</span>
            <span class="text-text capitalize">{form.encryption === 'age' ? 'Age (encrypted)' : 'None'}</span>
          </div>
        </div>

        <!-- Advanced: Retention -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Retention Policy
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

        <!-- Advanced: Scripts -->
        <details class="group">
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Scripts & Notifications
          </summary>
          <div class="space-y-4 mt-3 pl-6">
            <ScriptBrowser bind:value={form.pre_script} label="Pre-Backup Script" placeholder="/path/to/script.sh" />
            <ScriptBrowser bind:value={form.post_script} label="Post-Backup Script" placeholder="/path/to/script.sh" />
          </div>
        </details>

        <!-- Advanced: Verification -->
        <details class="group" open>
          <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
            <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
            Backup Verification
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

        {#if hasVMs}
          <details class="group" open>
            <summary class="flex items-center gap-2 cursor-pointer text-sm font-medium text-text-muted hover:text-text">
              <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
              VM Restore Verification
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
                    <label for={`${verifyIdBase}-mode`} class="block text-xs font-medium text-text-muted mb-1.5">Readiness Check</label>
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
          <svg class="w-3.5 h-3.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/></svg>
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

{#if confirmDelete.show}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
    onclick={(e) => { if (e.target === e.currentTarget) confirmDelete = { show: false, id: 0, name: '', deleteFiles: false } }}
    onkeydown={(e) => { if (e.key === 'Escape') confirmDelete = { show: false, id: 0, name: '', deleteFiles: false } }}
    role="dialog" aria-modal="true" aria-labelledby="delete-title" tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
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
