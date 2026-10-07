<script>
  import { onMount } from 'svelte'
  import { SvelteMap, SvelteSet } from 'svelte/reactivity'
  import { api } from '../lib/api.js'
  import { onWsMessage } from '../lib/ws.svelte.js'
  import { getProgress, restoreFromStatus } from '../lib/progress.svelte.js'
  import { shouldClearOnSnapshot } from '../lib/restore-sync.js'
  import { getPointCoverage, assignItemsToChosenPoints } from '../lib/restore-plan.js'
  import { formatDate, formatBytes, itemDisplayLabel, itemTypeIcon, itemTypeColor, itemTypeLabel, effectiveItemType, commonItemType, buildFileTree } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import Spinner from './Spinner.svelte'
  import RestorePointTimeline from './RestorePointTimeline.svelte'
  import Tooltip from './Tooltip.svelte'
  import FileTree from './FileTree.svelte'

  let { jobs = [], onrestore = () => {}, initialJobId = null, initialType = null, initialName = null, onmount = null } = $props()

  let step = $state(1)
  let selectedItems = $state(new SvelteMap()) // key: "type:name", value: item object
  let autoSelectApplied = false // deep-link auto-select is one-time per mount

  // Chosen restore points per job (key: jobId -> point object)
  let chosenPoints = $state(new SvelteMap())
  // Track restore point fetch status per job (key: jobId -> { jobId, jobName, loading, error, points })
  let jobPointsStatus = $state(new SvelteMap())

  let loading = $state(false)
  let allItems = $state([])
  let restorePoints = $state([])
  let typeFilter = $state('all')
  let deletingRpId = $state(null)
  let confirmDeleteRpId = $state(null)

  // Per-unit settings (key: jobId -> { restoreDestination, showDestOverride, cleanDestination, acknowledgeContainerRemap, passphrase, preflightResult, preflightRunning, preflightSig })
  let unitSettings = $state(new SvelteMap())

  const DEFAULT_UNIT_SETTINGS = Object.freeze({
    restoreDestination: '',
    showDestOverride: false,
    cleanDestination: true,
    acknowledgeContainerRemap: false,
    passphrase: '',
    preflightResult: null,
    preflightRunning: false,
    preflightSig: '',
  })

  function getUnitSettings(jobId) {
    return unitSettings.get(jobId) || DEFAULT_UNIT_SETTINGS
  }

  function updateUnitSetting(jobId, patch) {
    const cur = unitSettings.get(jobId) || DEFAULT_UNIT_SETTINGS
    unitSettings.set(jobId, { ...cur, ...patch })
  }

  // Restore progress and live logs
  const progress = getProgress()
  let restoring = $state(false)
  let restoringJobId = $state(null)
  let restoreOutcome = $state(null)
  let restoreLogs = $state([])
  let restoreLogsRunId = $state(null)
  let selectedLogUnitJobId = $state(null)
  let logContainerEl = $state(null)

  // Track unit runs: jobId -> { jobId, jobName, state, runId, done, total, failed, error, sizeBytes }
  let unitRuns = $state(new SvelteMap())

  let isRestoreRunning = $derived(
    restoring ||
    Array.from(unitRuns.values()).some(u => u.state === 'queued' || u.state === 'running') ||
    (progress.running && progress.activeRun?.run_type === 'restore' &&
      (!restoringJobId || progress.activeRun.job_id === restoringJobId ||
       Array.from(unitRuns.values()).some(u => u.jobId === progress.activeRun.job_id || (u.runId && u.runId === progress.activeRun.run_id))))
  )

  function scrollToLogBottom() {
    if (logContainerEl) {
      setTimeout(() => {
        if (logContainerEl) {
          logContainerEl.scrollTop = logContainerEl.scrollHeight
        }
      }, 50)
    }
  }

  function timeOnly(ts) {
    if (!ts) return ''
    const d = new Date(ts)
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  function resetWizard() {
    step = 1
    selectedItems.clear()
    chosenPoints.clear()
    jobPointsStatus.clear()
    unitSettings.clear()
    unitRuns.clear()
    restorePoints = []
    restoreOutcome = null
    restoreLogs = []
    restoreLogsRunId = null
    selectedLogUnitJobId = null
    mountedSession = null
    mountError = null
  }

  function checkAllUnitsFinished() {
    const entries = Array.from(unitRuns.values())
    if (entries.length === 0) return
    const anyActive = entries.some(u => u.state === 'queued' || u.state === 'running')
    if (anyActive) return

    restoring = false
    const anySuccess = entries.some(u => u.state === 'completed' || (u.state === 'partial' && u.done > 0))
    const anyFailure = entries.some(u => u.state !== 'completed')

    const totalDone = entries.reduce((s, u) => s + (u.done || 0), 0)
    const totalItems = entries.reduce((s, u) => s + (u.total || 0), 0)
    const totalFailed = entries.reduce((s, u) => {
      if (u.state === 'completed') return s
      if (u.failed) return s + u.failed
      if (u.state === 'partial') return s + Math.max(0, (u.total || 0) - (u.done || 0))
      return s + (u.total || 0)
    }, 0)
    const totalSize = entries.reduce((s, u) => s + (u.sizeBytes || 0), 0)

    let finalStatus = 'completed'
    if (!anySuccess) {
      finalStatus = 'failed'
    } else if (anyFailure) {
      finalStatus = 'partial'
    }

    restoreOutcome = {
      status: finalStatus,
      done: totalDone,
      total: totalItems,
      failed: totalFailed,
      sizeBytes: totalSize,
    }
  }

  async function reconcileRestoring() {
    try {
      const status = await api.getRunnerStatus()
      restoreFromStatus(status)
      const submittedJobIds = new Set(Array.from(unitRuns.keys()))
      if (status?.active && status.run_type === 'restore') {
        if (submittedJobIds.size === 0 || submittedJobIds.has(status.job_id)) {
          restoring = true
          restoringJobId = status.job_id
          const runEntry = unitRuns.get(status.job_id)
          if (runEntry) {
            unitRuns.set(status.job_id, {
              ...runEntry,
              state: 'running',
              runId: status.run_id || runEntry.runId,
            })
          }
          if (status.run_id && status.run_id !== restoreLogsRunId) {
            restoreLogsRunId = status.run_id
            api.getRunLogs(status.run_id, { limit: 500, tail: true }).then(res => {
              if (res?.entries) {
                restoreLogs = res.entries
                scrollToLogBottom()
              }
            }).catch(() => {})
          }
        }
      } else if (restoring && shouldClearOnSnapshot(status, submittedJobIds.size > 0 ? submittedJobIds : restoringJobId)) {
        restoring = false
      }
    } catch {
      // Leaves state unchanged on any API error
    }
  }

  // Pre-flight checks per unit
  function getUnitRestoreSig(unit) {
    const s = getUnitSettings(unit.jobId)
    return JSON.stringify({
      r: unit?.point?.id ?? null,
      p: s.passphrase,
      o: s.showDestOverride,
      d: s.showDestOverride ? s.restoreDestination : '',
    })
  }

  function isUnitPreflightFresh(unit) {
    const s = getUnitSettings(unit.jobId)
    return s.preflightResult != null && s.preflightSig === getUnitRestoreSig(unit)
  }

  async function runPreflight(unit) {
    if (!unit?.point) return
    const s = getUnitSettings(unit.jobId)
    const sigAtRun = getUnitRestoreSig(unit)
    updateUnitSetting(unit.jobId, { preflightRunning: true, preflightResult: null })
    try {
      const payload = {}
      if (s.showDestOverride && s.restoreDestination.trim()) payload.destination = s.restoreDestination.trim()
      if (s.passphrase) payload.passphrase = s.passphrase
      const res = await api.preflightRestore(unit.jobId, unit.point.id, payload)
      updateUnitSetting(unit.jobId, { preflightResult: res, preflightSig: sigAtRun, preflightRunning: false })
    } catch (e) {
      updateUnitSetting(unit.jobId, {
        preflightResult: { ok: false, checks: [{ id: 'error', label: 'Pre-flight could not run', status: 'fail', detail: e.message || 'request failed' }] },
        preflightSig: sigAtRun,
        preflightRunning: false,
      })
    }
  }

  async function runAllPreflights() {
    await Promise.all(restoreUnits.map(unit => runPreflight(unit)))
  }

  // Partial-restore file picker (Feature B + Issue #323).
  let picker = $state(new SvelteMap())

  function ensurePickerEntry(item) {
    const key = itemKey(item)
    if (!picker.has(key)) {
      picker.set(key, {
        contents: null,
        tree: null,
        totalFiles: 0,
        selected: new SvelteSet(),
        expandedPaths: new SvelteSet(),
        loading: false,
        error: '',
        errorStatus: 0,
        loadId: 0,
        search: '',
        open: false,
      })
    }
    return picker.get(key)
  }

  function updateEntry(item, patch) {
    const key = itemKey(item)
    const cur = ensurePickerEntry(item)
    picker.set(key, { ...cur, ...patch })
  }

  function supportsFilePicker(type) {
    return type === 'container' || type === 'folder' || type === 'plugin'
  }

  // Open or close an item's file picker, loading its listing on first open.
  async function togglePickerOpen(item) {
    const cur = ensurePickerEntry(item)
    const willOpen = !cur.open
    updateEntry(item, { open: willOpen })
    if (willOpen && !cur.contents) await loadPickerContents(item)
  }

  // 404 (no index / incomplete chain / item missing) and 424 (encrypted index,
  // no passphrase) mean this point cannot be browsed. Anything else — a
  // timeout, network error or 5xx — is transient and worth a retry (#449).
  function isPickerUnavailable(entry) {
    return entry?.errorStatus === 404 || entry?.errorStatus === 424
  }

  // Each load is tagged so a slow response that lands after the user changed
  // restore point (which clears or deletes the entry) is dropped instead of
  // filling the picker with the previous point's files.
  let pickerLoadSeq = 0

  async function loadPickerContents(item) {
    const key = itemKey(item)
    const cur = ensurePickerEntry(item)
    if (cur.loading) return
    const loadId = ++pickerLoadSeq
    updateEntry(item, { loading: true, error: '', errorStatus: 0, wholeItem: false, loadId })
    const isCurrent = () => picker.get(key)?.loadId === loadId
    try {
      const assignment = restorePlan.assignments.get(key)
      const point = assignment?.point
      if (!point) throw new Error('No restore point found for item')
      const contents = await api.getRestorePointContents(point.jobId, point.id, item.name)
      if (!isCurrent()) return
      const files = contents?.files || []
      const tree = buildFileTree(files)
      const totalFiles = tree.reduce((sum, n) => sum + (n.descendantLeafCount || 0), 0)
      const selected = new SvelteSet()
      for (const root of tree) {
        for (const p of root.descendantLeafPaths) {
          selected.add(p)
        }
      }
      updateEntry(item, { contents, tree, totalFiles, selected, loading: false })
    } catch (e) {
      if (!isCurrent()) return
      updateEntry(item, { error: e?.message || 'failed to load file list', errorStatus: e?.status || 0, loading: false })
    }
  }

  function toggleNodePicked(item, node) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry) return
    if (!node.isDir) {
      if (entry.selected.has(node.path)) entry.selected.delete(node.path)
      else entry.selected.add(node.path)
    } else {
      const paths = node.descendantLeafPaths || []
      const allSelected = paths.length > 0 && paths.every(p => entry.selected.has(p))
      if (allSelected) {
        for (const p of paths) entry.selected.delete(p)
      } else {
        for (const p of paths) entry.selected.add(p)
      }
    }
    updateEntry(item, {})
  }

  function toggleFolderExpanded(item, dirPath) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry) return
    if (!entry.expandedPaths) entry.expandedPaths = new SvelteSet()
    if (entry.expandedPaths.has(dirPath)) {
      entry.expandedPaths.delete(dirPath)
    } else {
      entry.expandedPaths.add(dirPath)
    }
    updateEntry(item, {})
  }

  function selectAllFiles(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry?.tree) return
    const q = entry.search.trim().toLowerCase()
    if (!q) {
      for (const root of entry.tree) {
        for (const p of root.descendantLeafPaths) {
          entry.selected.add(p)
        }
      }
    } else {
      function selectMatching(nodes) {
        for (const n of nodes) {
          if (!n.isDir) {
            if (n.name.toLowerCase().includes(q) || n.path.toLowerCase().includes(q)) {
              entry.selected.add(n.path)
            }
          } else {
            if (n.name.toLowerCase().includes(q) || n.path.toLowerCase().includes(q)) {
              for (const p of n.descendantLeafPaths) entry.selected.add(p)
            } else if (n.children) {
              selectMatching(n.children)
            }
          }
        }
      }
      selectMatching(entry.tree)
    }
    updateEntry(item, {})
  }

  function deselectAllFiles(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry?.tree) return
    const q = entry.search.trim().toLowerCase()
    if (!q) {
      entry.selected.clear()
    } else {
      function deselectMatching(nodes) {
        for (const n of nodes) {
          if (!n.isDir) {
            if (n.name.toLowerCase().includes(q) || n.path.toLowerCase().includes(q)) {
              entry.selected.delete(n.path)
            }
          } else {
            if (n.name.toLowerCase().includes(q) || n.path.toLowerCase().includes(q)) {
              for (const p of n.descendantLeafPaths) entry.selected.delete(p)
            } else if (n.children) {
              deselectMatching(n.children)
            }
          }
        }
      }
      deselectMatching(entry.tree)
    }
    updateEntry(item, {})
  }

  function expandAllFolders(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry?.tree) return
    if (!entry.expandedPaths) entry.expandedPaths = new SvelteSet()
    function collectDirs(nodes) {
      for (const n of nodes) {
        if (n.isDir) {
          entry.expandedPaths.add(n.path)
          if (n.children) collectDirs(n.children)
        }
      }
    }
    collectDirs(entry.tree)
    updateEntry(item, {})
  }

  function collapseAllFolders(item) {
    const key = itemKey(item)
    const entry = picker.get(key)
    if (!entry?.expandedPaths) return
    entry.expandedPaths.clear()
    updateEntry(item, {})
  }

  function switchLogUnit(unitRun) {
    selectedLogUnitJobId = unitRun.jobId
    if (unitRun.runId) {
      restoreLogsRunId = unitRun.runId
      api.getRunLogs(unitRun.runId, { limit: 500, tail: true }).then(res => {
        if (res?.entries) {
          restoreLogs = res.entries
          scrollToLogBottom()
        }
      }).catch(() => {})
    }
  }

  onMount(() => {
    reconcileRestoring()
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'job_run_started') {
        if (msg.run_type === 'restore') {
          let runEntry = unitRuns.get(msg.job_id)
          if (!runEntry && unitRuns.size === 0 && (!restoringJobId || msg.job_id === restoringJobId)) {
            restoring = true
            restoringJobId = msg.job_id
            restoreOutcome = null
            if (msg.run_id) {
              restoreLogsRunId = msg.run_id
              restoreLogs = []
            }
          } else if (runEntry) {
            restoring = true
            unitRuns.set(msg.job_id, {
              ...runEntry,
              state: 'running',
              runId: msg.run_id || runEntry.runId,
            })
            const curSelected = selectedLogUnitJobId ? unitRuns.get(selectedLogUnitJobId) : null
            const curSelectedActive = curSelected && (curSelected.state === 'queued' || curSelected.state === 'running')
            if (!selectedLogUnitJobId || selectedLogUnitJobId === msg.job_id || !curSelectedActive) {
              selectedLogUnitJobId = msg.job_id
              restoreLogsRunId = msg.run_id
              restoreLogs = []
              if (msg.run_id) {
                api.getRunLogs(msg.run_id, { limit: 500, tail: true }).then(res => {
                  if (res?.entries) {
                    restoreLogs = res.entries
                    scrollToLogBottom()
                  }
                }).catch(() => {})
              }
            }
          }
        }
      } else if (msg.type === 'job_run_completed') {
        if (msg.run_type === 'restore') {
          let runEntry = null
          if (msg.run_id) {
            runEntry = Array.from(unitRuns.values()).find(u => u.runId === msg.run_id)
          }
          if (!runEntry && msg.job_id) {
            runEntry = unitRuns.get(msg.job_id)
          }

          if (runEntry) {
            unitRuns.set(runEntry.jobId, {
              ...runEntry,
              state: msg.status,
              done: msg.items_done || 0,
              total: msg.items_total || runEntry.total,
              failed: msg.items_failed || 0,
              sizeBytes: msg.size_bytes || 0,
            })
            checkAllUnitsFinished()
          } else if (unitRuns.size === 0 && (!restoringJobId || msg.job_id === restoringJobId)) {
            restoring = false
            restoreOutcome = {
              status: msg.status,
              done: msg.items_done || 0,
              total: msg.items_total || 0,
              failed: msg.items_failed || 0,
              sizeBytes: msg.size_bytes || 0,
            }
          }

          if (restoreLogsRunId) {
            api.getRunLogs(restoreLogsRunId, { limit: 500, tail: true }).then(res => {
              if (res?.entries) {
                restoreLogs = res.entries
                scrollToLogBottom()
              }
            }).catch(() => {})
          }
        }
      } else if (msg.type === 'runner_status_snapshot') {
        const submittedJobIds = new Set(Array.from(unitRuns.keys()))
        if (shouldClearOnSnapshot(msg.status, submittedJobIds.size > 0 ? submittedJobIds : restoringJobId)) {
          if (unitRuns.size === 0) {
            restoring = false
          } else {
            const activeUnits = Array.from(unitRuns.values()).filter(u => u.state === 'queued' || u.state === 'running')
            if (activeUnits.length === 0) {
              checkAllUnitsFinished()
            } else {
              Promise.all(activeUnits.map(async (u) => {
                try {
                  const res = await api.getJobHistory(u.jobId, 5)
                  const runs = Array.isArray(res) ? res : []
                  const matching = u.runId
                    ? runs.find(r => r.id === u.runId)
                    : runs.find(r => r.run_type === 'restore' && (!u.submittedAt || new Date(r.started_at).getTime() >= u.submittedAt - 10000))
                  if (matching && matching.status !== 'running') {
                    unitRuns.set(u.jobId, {
                      ...u,
                      state: matching.status,
                      runId: matching.id || u.runId,
                      done: matching.items_done || 0,
                      total: matching.items_total || u.total,
                      failed: matching.items_failed || 0,
                      sizeBytes: matching.size_bytes || 0,
                    })
                  }
                } catch {
                  // Keep existing state if API fetch fails
                }
              })).then(() => {
                checkAllUnitsFinished()
              })
            }
          }
        }
      } else if (msg.type === 'run_log' && msg.entry) {
        if (restoreLogsRunId && msg.entry.run_id === restoreLogsRunId) {
          if (!restoreLogs.some(e => e.id === msg.entry.id)) {
            restoreLogs = [...restoreLogs, msg.entry]
            scrollToLogBottom()
          }
        }
      }
    })
    return unsub
  })

  $effect(() => {
    const run = progress.activeRun
    if (run && run.run_type === 'restore' && run.run_id && run.run_id !== restoreLogsRunId) {
      const submittedJobIds = new Set(Array.from(unitRuns.keys()))
      const curSelected = selectedLogUnitJobId ? unitRuns.get(selectedLogUnitJobId) : null
      const curSelectedActive = curSelected && (curSelected.state === 'queued' || curSelected.state === 'running')
      if (
        (submittedJobIds.size === 0 || submittedJobIds.has(run.job_id)) &&
        (!selectedLogUnitJobId || selectedLogUnitJobId === run.job_id || !curSelectedActive)
      ) {
        selectedLogUnitJobId = run.job_id
        restoreLogsRunId = run.run_id
        api.getRunLogs(run.run_id, { limit: 500, tail: true }).then(res => {
          if (res?.entries) {
            restoreLogs = res.entries
            scrollToLogBottom()
          }
        }).catch(() => {})
      }
    }
  })

  function itemKey(item) {
    return `${effectiveItemType(item)}:${itemDisplayLabel(item)}`
  }

  // Gather all backed-up items across all jobs
  $effect(() => {
    gatherItems()
  })

  function gatherItems() {
    loading = true
    try {
      const itemMap = new SvelteMap()
      for (const detail of jobs) {
        if (!detail?.items) continue
        for (const item of detail.items) {
          const effType = effectiveItemType(item)
          const key = `${effType}:${itemDisplayLabel(item)}`
          if (!itemMap.has(key)) {
            itemMap.set(key, {
              name: item.item_name,
              type: effType,
              item_name: item.item_name,
              item_type: effType,
              item_id: item.item_id,
              settings: item.settings,
              jobs: [],
            })
          }
          itemMap.get(key).jobs.push(detail)
        }
      }
      allItems = Array.from(itemMap.values())
      if (!autoSelectApplied && jobs.length > 0) {
        autoSelectApplied = true
        if (initialJobId) {
          const jid = Number(initialJobId)
          for (const item of allItems) {
            if (item.jobs.some(j => j.id === jid)) {
              selectedItems.set(itemKey(item), item)
            }
          }
        }
        if (initialType && initialName && selectedItems.size === 0) {
          const item = allItems.find(i =>
            `${i.type}:${i.name}` === `${initialType}:${initialName}` ||
            itemKey(i) === `${initialType}:${initialName}` ||
            (initialType === 'folder' && i.type === 'flash' && i.name === initialName)
          )
          if (item) selectedItems.set(itemKey(item), item)
        }
      }
    } catch { /* ignore */ } finally {
      loading = false
    }
  }

  let searchQuery = $state('')
  let sortBy = $state('alpha-asc')

  let filteredItems = $derived.by(() => {
    let items = typeFilter === 'all' ? allItems : allItems.filter(i => i.type === typeFilter)
    if (searchQuery.trim()) {
      const q = searchQuery.trim().toLowerCase()
      items = items.filter(i => {
        const label = itemDisplayLabel(i).toLowerCase()
        const name = (i.name || '').toLowerCase()
        const type = (i.type || '').toLowerCase()
        const jobNames = i.jobs.map(j => j.name || '').join(' ').toLowerCase()
        return label.includes(q) || name.includes(q) || type.includes(q) || jobNames.includes(q)
      })
    }
    const sorted = [...items]
    switch (sortBy) {
      case 'alpha-desc':
        sorted.sort((a, b) => itemDisplayLabel(b).localeCompare(itemDisplayLabel(a), undefined, { sensitivity: 'base' }))
        break
      case 'type':
        sorted.sort((a, b) => a.type.localeCompare(b.type) || itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
      case 'jobs':
        sorted.sort((a, b) => b.jobs.length - a.jobs.length || itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
      case 'alpha-asc':
      default:
        sorted.sort((a, b) => itemDisplayLabel(a).localeCompare(itemDisplayLabel(b), undefined, { sensitivity: 'base' }))
        break
    }
    return sorted
  })

  let typeOptions = $derived.by(() => {
    const types = new Set(allItems.map(i => i.type))
    const canonicalOrder = ['all', 'container', 'vm', 'folder', 'flash', 'plugin', 'zfs']
    return canonicalOrder.filter(t => t === 'all' || types.has(t))
  })

  let selectedCount = $derived(selectedItems.size)
  let selectedItemsArray = $derived(Array.from(selectedItems.values()))

  let relevantJobs = $derived.by(() => {
    const map = new SvelteMap()
    for (const item of selectedItemsArray) {
      for (const j of (item.jobs || [])) {
        if (!map.has(j.id)) map.set(j.id, j)
      }
    }
    return Array.from(map.values())
  })

  let isMultiJob = $derived(relevantJobs.length > 1)
  let loadingPoints = $derived(Array.from(jobPointsStatus.values()).some(s => s.loading))

  let restorePlan = $derived(assignItemsToChosenPoints(selectedItemsArray, chosenPoints, itemKey))
  let restoreUnits = $derived(restorePlan.units)
  let planComplete = $derived(restorePlan.isComplete)

  // Backward compatibility alias for single unit
  let selectedPoint = $derived(restoreUnits[0]?.point ?? null)

  function toggleItem(item) {
    const key = itemKey(item)
    if (selectedItems.has(key)) {
      selectedItems.delete(key)
    } else {
      selectedItems.set(key, item)
    }
  }

  function isSelected(item) {
    return selectedItems.has(itemKey(item))
  }

  function selectAll() {
    for (const item of filteredItems) {
      selectedItems.set(itemKey(item), item)
    }
  }

  function clearSelection() {
    selectedItems.clear()
  }

  function updateRestorePointsList() {
    const all = []
    for (const status of jobPointsStatus.values()) {
      all.push(...status.points)
    }
    restorePoints = all
      .filter(p => getPointCoverage(p, selectedItemsArray).coverageCount > 0)
      .sort((a, b) => new Date(b.created_at) - new Date(a.created_at))
  }

  async function retryJobFetch(jobId) {
    const cur = jobPointsStatus.get(jobId)
    if (!cur) return
    jobPointsStatus.set(jobId, { ...cur, loading: true, error: null })
    try {
      const job = jobs.find(j => j.id === jobId)
      const pts = (await api.getRestorePoints(jobId)) || []
      const enriched = pts.map(p => ({
        ...p,
        jobName: job?.name || cur.jobName,
        jobId: jobId,
        encryption: job?.encryption,
      }))
      jobPointsStatus.set(jobId, {
        ...cur,
        loading: false,
        error: null,
        points: enriched,
      })
    } catch (e) {
      jobPointsStatus.set(jobId, {
        ...cur,
        loading: false,
        error: e?.message || 'Failed to load restore points',
        points: [],
      })
    }
    updateRestorePointsList()
  }

  async function proceedToStep2() {
    if (selectedItems.size === 0) return
    step = 2
    restorePoints = []
    chosenPoints.clear()
    jobPointsStatus.clear()
    unitSettings.clear()

    const jobsMap = new SvelteMap()
    for (const item of selectedItemsArray) {
      for (const j of (item.jobs || [])) {
        jobsMap.set(j.id, j)
      }
    }

    for (const [jobId, job] of jobsMap.entries()) {
      jobPointsStatus.set(jobId, {
        jobId,
        jobName: job.name || `Job #${jobId}`,
        loading: true,
        error: null,
        points: [],
      })
    }

    const promises = Array.from(jobsMap.entries()).map(async ([jobId, job]) => {
      try {
        const pts = (await api.getRestorePoints(jobId)) || []
        const enriched = pts.map(p => ({
          ...p,
          jobName: job.name,
          jobId: jobId,
          encryption: job.encryption,
        }))
        jobPointsStatus.set(jobId, {
          jobId,
          jobName: job.name,
          loading: false,
          error: null,
          points: enriched,
        })
      } catch (err) {
        jobPointsStatus.set(jobId, {
          jobId,
          jobName: job.name,
          loading: false,
          error: err?.message || 'Failed to load restore points',
          points: [],
        })
      }
    })

    await Promise.all(promises)
    updateRestorePointsList()
  }

  function handlePointSelect(point) {
    confirmDeleteRpId = null
    const cov = getPointCoverage(point, selectedItemsArray)

    // Preserve immediate Step 3 navigation for fully covered single-job selection
    if (!isMultiJob && cov.coversAll) {
      chosenPoints.clear()
      chosenPoints.set(point.jobId, point)
      step = 3
      picker.clear()
      mountedSession = null
      mountError = null
      return
    }

    // Multi-job or partial coverage: record chosen point for this job
    const prevPoint = chosenPoints.get(point.jobId)
    if (prevPoint?.id !== point.id) {
      for (const item of selectedItemsArray) {
        if (item.jobs.some(j => j.id === point.jobId)) {
          picker.delete(itemKey(item))
        }
      }
      chosenPoints.set(point.jobId, point)
    }
  }

  function removeUncoveredItems() {
    for (const item of restorePlan.uncovered) {
      selectedItems.delete(itemKey(item))
    }
    updateRestorePointsList()
  }

  let mounting = $state(false)
  let mountError = $state(null)
  let mountedSession = $state(null)

  let isDeduplicated = $derived.by(() => {
    const pt = restoreUnits[0]?.point
    if (!pt) return false
    if (pt.manifest_id) return true
    if (pt.metadata) {
      try {
        const meta = typeof pt.metadata === 'string' ? JSON.parse(pt.metadata) : pt.metadata
        if (meta?.item_manifests && Object.keys(meta.item_manifests).length > 0) return true
      } catch {
        // ignore parse errors
      }
    }
    return false
  })

  async function doMount(unit) {
    const targetUnit = unit || restoreUnits[0]
    if (!targetUnit?.point) return
    const jobId = targetUnit.jobId
    const pointId = targetUnit.point.id

    mounting = true
    mountError = null
    try {
      const session = await api.mountRestorePoint(jobId, pointId)
      mountedSession = session
      if (onmount) {
        onmount(session)
      }
    } catch (err) {
      mountError = err.message
    } finally {
      mounting = false
    }
  }

  function parseMetadata(meta) {
    if (!meta) return {}
    try { return JSON.parse(meta) } catch { return {} }
  }

  function selectedRestoreSize(rp, items = selectedItemsArray) {
    const meta = parseMetadata(rp.metadata)
    const itemSizes = meta.item_sizes
    if (!itemSizes) return rp.size_bytes
    const selectedNames = new Set((items || []).map(i => i.name))
    let total = 0
    for (const [name, size] of Object.entries(itemSizes)) {
      if (selectedNames.has(name)) total += size
    }
    return total || rp.size_bytes
  }

  function unitHasPartialSelection(unit) {
    return unit.items.some(item => {
      const entry = picker.get(itemKey(item))
      const total = entry?.totalFiles ?? (entry?.contents?.files?.length || 0)
      return entry?.selected && entry.selected.size > 0 && entry.selected.size < total
    })
  }

  function unitHasEmptySelection(unit) {
    return unit.items.some(item => {
      const entry = picker.get(itemKey(item))
      const total = entry?.totalFiles ?? (entry?.contents?.files?.length || 0)
      return entry?.contents && total > 0 && entry.selected.size === 0
    })
  }

  // An item whose file list is still loading or failed transiently would
  // submit without file_paths and restore in full. Block until the list
  // arrives or the user explicitly chooses the whole item (#449).
  function unitHasPendingFileList(unit) {
    return unit.items.some(item => {
      const entry = picker.get(itemKey(item))
      return entry?.loading || (entry?.error && !isPickerUnavailable(entry))
    })
  }

  // True when a restore unit is not ready to submit (empty or pending file
  // selection, broken chain, unacknowledged remap, missing passphrase).
  function isUnitBlocked(unit) {
    if (!unit) return true
    if (unit.items.length === 0 || unitHasEmptySelection(unit) || unitHasPendingFileList(unit)) return true
    if (unit.point?.chain_status === 'broken') return true
    const s = getUnitSettings(unit.jobId)
    const hasContainer = unit.items.some(i => i.type === 'container')
    if (hasContainer && s.showDestOverride && !s.acknowledgeContainerRemap) return true
    if (unit.point?.encryption === 'age' && !s.passphrase) return true
    return false
  }

  let anyUnitBlocked = $derived(
    selectedItems.size === 0 ||
    restoreUnits.length === 0 ||
    restoreUnits.some(unit => isUnitBlocked(unit))
  )

  let allUnitsPreflightPassing = $derived(
    restoreUnits.length > 0 && restoreUnits.every(unit => {
      const s = getUnitSettings(unit.jobId)
      return s.preflightResult?.ok && isUnitPreflightFresh(unit)
    })
  )

  let anyUnitPreflightRunning = $derived(
    restoreUnits.some(unit => getUnitSettings(unit.jobId).preflightRunning)
  )

  let allUnitsPreflightFresh = $derived(
    restoreUnits.length > 0 && restoreUnits.every(unit => isUnitPreflightFresh(unit))
  )

  let canStartRestore = $derived(
    !isRestoreRunning &&
    !anyUnitBlocked &&
    allUnitsPreflightPassing
  )

  let canRestoreAnyway = $derived(
    !isRestoreRunning &&
    !anyUnitBlocked
  )

  let recommendedRpId = $derived(restorePoints[0]?.id ?? null)

  function chainDependencies(rp) {
    return Math.max(0, (rp?.chain_depth || 1) - 1)
  }

  function chainHealthTone(rp) {
    if (rp?.chain_status === 'broken') return 'text-danger'
    if (chainDependencies(rp) > 0) return 'text-info'
    return 'text-success'
  }

  function chainHealthLabel(rp) {
    if (!rp) return 'Unknown'
    if (rp.chain_status === 'broken') return 'Broken chain'
    if (chainDependencies(rp) > 0) return `Needs ${chainDependencies(rp)} earlier backup${chainDependencies(rp) === 1 ? '' : 's'}`
    return 'Standalone'
  }

  function retentionPreservedMessage(rp) {
    const count = rp?.retention_preserved_for || 0
    return `Kept because ${count} newer restore point${count === 1 ? '' : 's'} still depend on it.`
  }

  async function doRestore() {
    if (anyUnitBlocked) return

    restoring = true
    restoreOutcome = null
    restoreLogs = []
    restoreLogsRunId = null
    selectedLogUnitJobId = restoreUnits[0]?.jobId ?? null

    const submissionTime = Date.now()
    unitRuns.clear()
    for (const unit of restoreUnits) {
      unitRuns.set(unit.jobId, {
        jobId: unit.jobId,
        jobName: unit.jobName,
        state: 'queued',
        runId: null,
        submittedAt: submissionTime,
        done: 0,
        total: unit.items.length,
        failed: 0,
        error: null,
        sizeBytes: 0,
      })
    }

    for (const unit of restoreUnits) {
      const s = getUnitSettings(unit.jobId)

      const payload = {
        restore_point_id: unit.point.id,
        items: Array.from(new Set(unit.items.map(i => i.name))),
        clean_destination: s.cleanDestination && !unitHasPartialSelection(unit),
      }
      if (s.showDestOverride && s.restoreDestination.trim()) {
        payload.destination = s.restoreDestination.trim()
      }
      if (s.passphrase) {
        payload.passphrase = s.passphrase
      }

      const filePaths = {}
      for (const item of unit.items) {
        const entry = picker.get(itemKey(item))
        if (entry?.contents && entry?.selected) {
          const total = entry.totalFiles ?? (entry.contents.files?.length || 0)
          if (entry.selected.size > 0 && entry.selected.size < total) {
            filePaths[item.name] = Array.from(entry.selected)
          }
        }
      }
      if (Object.keys(filePaths).length > 0) {
        payload.file_paths = filePaths
      }

      try {
        const res = await onrestore(unit.jobId, payload)
        if (res && res.ok === false) {
          const cur = unitRuns.get(unit.jobId)
          if (cur) {
            unitRuns.set(unit.jobId, {
              ...cur,
              state: 'rejected',
              error: res.error || 'Server rejected restore request',
            })
          }
        }
      } catch (err) {
        const cur = unitRuns.get(unit.jobId)
        if (cur) {
          unitRuns.set(unit.jobId, {
            ...cur,
            state: 'rejected',
            error: err?.message || 'Failed to submit restore request',
          })
        }
      }
    }
    checkAllUnitsFinished()
  }

  function handleRestoreAnyway() {
    const failedUnits = restoreUnits.filter(unit => {
      const s = getUnitSettings(unit.jobId)
      return !s.preflightResult?.ok
    })
    const names = failedUnits.map(u => u.jobName).join(', ')
    if (window.confirm(`Pre-flight checks did not all pass for: ${names}. Restore anyway?`)) {
      doRestore()
    }
  }

  function goBack() {
    if (step === 3) {
      step = 2
    } else if (step === 2) {
      step = 1
      restorePoints = []
      chosenPoints.clear()
      jobPointsStatus.clear()
      unitSettings.clear()
    }
  }

  async function deleteRestorePoint(rp) {
    if (confirmDeleteRpId !== rp.id) {
      confirmDeleteRpId = rp.id
      return
    }
    confirmDeleteRpId = null
    deletingRpId = rp.id
    try {
      await api.deleteRestorePoint(rp.job_id || rp.jobId, rp.id)
      restorePoints = restorePoints.filter(p => p.id !== rp.id)
      for (const [jid, status] of jobPointsStatus.entries()) {
        jobPointsStatus.set(jid, {
          ...status,
          points: status.points.filter(p => p.id !== rp.id)
        })
      }
      if (chosenPoints.get(rp.jobId)?.id === rp.id) {
        chosenPoints.delete(rp.jobId)
      }
    } catch (e) {
      console.error('Failed to delete restore point', e)
    } finally {
      deletingRpId = null
    }
  }
</script>

<div>
  <!-- Step indicator -->
  <div class="flex items-center gap-2 mb-6">
    {#each [{n:1, label:'Select Items'}, {n:2, label:'Choose Version'}, {n:3, label:'Restore'}] as s (s.n)}
      <button type="button" onclick={() => {
        if (s.n < step) {
          const curStep = step
          if (s.n === 1) {
            goBack()
            if (curStep === 3) goBack()
          } else if (s.n === 2) {
            step = 2
          }
        }
      }}
        class="flex items-center gap-2 {s.n <= step ? '' : 'opacity-40'}">
        <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n < step ? 'bg-vault text-white' : s.n === step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
          {#if s.n < step}
            <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
          {:else}
            {s.n}
          {/if}
        </div>
        <span class="text-xs font-medium {s.n === step ? 'text-text' : 'text-text-muted'} hidden sm:inline">{s.label}</span>
      </button>
      {#if s.n < 3}
        <div class="flex-1 h-px {s.n < step ? 'bg-vault' : 'bg-border'}"></div>
      {/if}
    {/each}
  </div>

  <!-- Step 1: What to restore (multi-select) -->
  {#if step === 1}
    {#if loading}
      <Spinner text="Loading backed-up items..." />
    {:else if allItems.length === 0}
      <div class="text-center py-12">
        <div class="mb-3 opacity-30"><svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4"/></svg></div>
        <p class="text-sm text-text-muted">No backed-up items found. Run a backup first.</p>
      </div>
    {:else}
      <!-- Type filter tabs + selection & search controls -->
      <div class="flex flex-col gap-3 mb-4">
        <div class="flex items-center justify-between flex-wrap gap-2">
          <!-- Type filter tabs -->
          <div class="flex items-center gap-1.5 flex-wrap">
            {#each typeOptions as t (t)}
              <button type="button" onclick={() => typeFilter = t}
                class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors flex items-center gap-1.5 {typeFilter === t ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4'}">
                {#if t !== 'all'}
                  <svg aria-hidden="true" class="w-3.5 h-3.5 {typeFilter === t ? 'text-white' : itemTypeColor(t)}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(t)}/>
                  </svg>
                {/if}
                {itemTypeLabel(t)}
              </button>
            {/each}
          </div>
          <!-- Select All / Clear -->
          <div class="flex items-center gap-2 shrink-0">
            <button type="button" onclick={selectAll}
              class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
              Select All
            </button>
            {#if selectedCount > 0}
              <button type="button" onclick={clearSelection}
                class="px-3 py-1.5 text-xs font-medium rounded-lg bg-surface-3 text-text-muted hover:text-text hover:bg-surface-4 transition-colors">
                Clear ({selectedCount})
              </button>
            {/if}
          </div>
        </div>

        <!-- Search and Sort row -->
        <div class="flex items-center justify-between gap-3 flex-wrap sm:flex-nowrap">
          <div class="relative flex-1 min-w-[200px]">
            <svg aria-hidden="true" class="w-4 h-4 text-text-dim absolute left-3 top-1/2 -translate-y-1/2 pointer-events-none" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
            </svg>
            <input
              type="text"
              bind:value={searchQuery}
              placeholder="Search items or jobs..."
              class="w-full pl-9 pr-8 py-1.5 text-xs bg-surface-2 border border-border rounded-lg text-text placeholder-text-dim focus:outline-none focus:border-vault"
            />
            {#if searchQuery}
              <button
                type="button"
                onclick={() => searchQuery = ''}
                class="absolute right-2.5 top-1/2 -translate-y-1/2 text-text-dim hover:text-text"
                title="Clear search"
              >
                <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                </svg>
              </button>
            {/if}
          </div>

          <div class="flex items-center gap-2 shrink-0 text-xs text-text-muted">
            <span>Sort:</span>
            <select
              bind:value={sortBy}
              class="bg-surface-2 border border-border rounded-lg px-2.5 py-1.5 text-xs text-text focus:outline-none focus:border-vault cursor-pointer"
            >
              <option value="alpha-asc">Name (A → Z)</option>
              <option value="alpha-desc">Name (Z → A)</option>
              <option value="type">Type</option>
              <option value="jobs">Most Backed Up</option>
            </select>
          </div>
        </div>
      </div>

      {#if filteredItems.length === 0}
        <div class="text-center py-12 bg-surface-2 border border-border rounded-xl">
          <p class="text-sm text-text-muted">No items match your filter.</p>
        </div>
      {:else}
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2.5">
          {#each filteredItems as item (itemKey(item))}
            {@const selected = isSelected(item)}
            <button type="button" onclick={() => toggleItem(item)}
              class="bg-surface-2 border rounded-xl p-3 text-left hover:shadow-sm transition-all group
                {selected ? 'border-vault ring-1 ring-vault/30 bg-surface-2/90' : 'border-border hover:border-vault/40'}">
              <div class="flex items-center gap-2.5 mb-1.5">
                <!-- Checkbox indicator -->
                <div class="w-4.5 h-4.5 rounded border-2 flex items-center justify-center shrink-0 transition-colors
                  {selected ? 'bg-vault border-vault' : 'border-border group-hover:border-vault/40'}">
                  {#if selected}
                    <svg aria-hidden="true" class="w-3 h-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                  {/if}
                </div>
                <div class="w-7 h-7 rounded-lg bg-surface-3 flex items-center justify-center shrink-0 group-hover:bg-vault/10 transition-colors">
                  <svg aria-hidden="true" class="w-4 h-4 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
                </div>
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</p>
                </div>
                <span class="px-1.5 py-0.5 text-[10px] uppercase font-semibold tracking-wider rounded bg-surface-3 shrink-0 {itemTypeColor(item.type)}">
                  {itemTypeLabel(item.type)}
                </span>
              </div>
              <p class="text-[11px] text-text-dim truncate pl-7">In {item.jobs.length} job{item.jobs.length !== 1 ? 's' : ''}: {item.jobs.map(j => j.name).join(', ')}</p>
            </button>
          {/each}
        </div>
      {/if}

      <!-- Selection summary + Floating Next button -->
      <div class="sticky bottom-4 z-20 mt-6 p-3.5 bg-surface-2/95 backdrop-blur-md border border-border rounded-xl shadow-lg flex items-center justify-between transition-all">
        <div class="flex items-center gap-3">
          <div class="w-2.5 h-2.5 rounded-full {selectedCount > 0 ? 'bg-vault animate-pulse' : 'bg-text-dim'}"></div>
          <span class="text-sm font-medium text-text">
            {#if selectedCount > 0}
              <span class="text-vault font-semibold">{selectedCount}</span> item{selectedCount !== 1 ? 's' : ''} selected
            {:else}
              Select items to restore
            {/if}
          </span>
        </div>
        <button type="button" onclick={proceedToStep2} disabled={selectedCount === 0}
          class="px-5 py-2 text-sm font-semibold text-white bg-vault hover:bg-vault-dark rounded-lg transition-all shadow-sm disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2 cursor-pointer">
          Next
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
        </button>
      </div>
    {/if}

  <!-- Step 2: Which version -->
  {:else if step === 2}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors cursor-pointer">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to items
      </button>
      <div class="flex items-center gap-3 mt-2 flex-wrap">
        {#each selectedItemsArray as item (itemKey(item))}
          <div class="flex items-center gap-1.5 px-2.5 py-1 bg-surface-3 rounded-lg min-w-0">
            <svg aria-hidden="true" class="w-4 h-4 shrink-0 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
            <span class="text-xs font-medium text-text truncate max-w-[200px]" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
            <span class="text-xs text-text-dim capitalize shrink-0">({itemTypeLabel(item.type)})</span>
          </div>
        {/each}
      </div>
      <p class="text-sm text-text-muted mt-3">
        {#if isMultiJob}
          Your selection comes from {relevantJobs.length} backup jobs. Choose one restore point for each job.
        {:else}
          Select a restore point to restore from. Each entry represents a saved backup version showing its archive size and estimated restore size.
        {/if}
      </p>
    </div>

    <!-- Job fetch error alerts with retry -->
    {#each Array.from(jobPointsStatus.values()).filter(s => s.error) as failedJob (failedJob.jobId)}
      <div class="bg-danger/10 border border-danger/30 rounded-xl p-3 mb-3 flex items-center justify-between gap-3 text-xs">
        <span class="text-danger">Failed to load restore points for <strong>{failedJob.jobName}</strong>: {failedJob.error}</span>
        <button type="button" onclick={() => retryJobFetch(failedJob.jobId)}
          class="px-2.5 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text font-medium cursor-pointer">
          Retry
        </button>
      </div>
    {/each}

    <!-- Coverage summary & Recovery actions -->
    {#if isMultiJob || (chosenPoints.size > 0 && !planComplete)}
      <div class="bg-surface-2 border border-border rounded-xl p-4 mb-4">
        <div class="flex items-center justify-between flex-wrap gap-2 mb-2.5">
          <div>
            <h3 class="text-sm font-semibold text-text">Restore Plan Coverage</h3>
            <p class="text-xs text-text-muted mt-0.5">
              {isMultiJob ? `Items span ${relevantJobs.length} backup jobs.` : 'Items in current selection:'}
              Choose restore points below until all items are covered.
            </p>
          </div>
          {#if planComplete}
            <span class="text-xs px-2.5 py-1 rounded-full bg-success/15 text-success font-medium flex items-center gap-1.5">
              <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7"/></svg>
              Plan complete
            </span>
          {:else}
            <span class="text-xs px-2.5 py-1 rounded-full bg-amber-500/15 text-amber-400 font-medium">
              {restorePlan.uncovered.length} item{restorePlan.uncovered.length === 1 ? '' : 's'} not covered yet
            </span>
          {/if}
        </div>

        <!-- Items list with assigned version -->
        <div class="divide-y divide-border/50 border border-border/50 rounded-lg overflow-hidden bg-surface-1 mb-3">
          {#each selectedItemsArray as item (itemKey(item))}
            {@const assignment = restorePlan.assignments.get(itemKey(item))}
            <div class="flex items-center justify-between p-2.5 text-xs">
              <div class="flex items-center gap-2 min-w-0">
                <svg aria-hidden="true" class="w-3.5 h-3.5 shrink-0 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
                <span class="font-medium text-text truncate">{itemDisplayLabel(item)}</span>
                <span class="text-text-dim">({item.type})</span>
              </div>
              <div class="shrink-0 text-right">
                {#if assignment}
                  <span class="text-emerald-400 font-medium">
                    {assignment.point.jobName || `Job #${assignment.jobId}`} · {formatDate(assignment.point.created_at)}
                  </span>
                {:else}
                  <span class="text-amber-400 font-medium">Not covered yet</span>
                {/if}
              </div>
            </div>
          {/each}
        </div>

        <!-- Recovery actions if incomplete -->
        {#if !planComplete}
          <div class="flex items-center justify-between flex-wrap gap-2 text-xs">
            <p class="text-text-dim">Select a restore point from each job below, or remove uncovered items to proceed.</p>
            <div class="flex items-center gap-2">
              <button type="button" onclick={removeUncoveredItems}
                class="px-2.5 py-1.5 rounded-lg border border-border bg-surface-3 hover:bg-surface-4 text-text transition-colors cursor-pointer">
                Remove uncovered items from selection ({restorePlan.uncovered.length})
              </button>
            </div>
          </div>
        {/if}
      </div>
    {/if}

    {#if loadingPoints}
      <Spinner text="Loading restore points..." />
    {:else if restorePoints.length === 0}
      <div class="text-center py-12 bg-surface-2 border border-border rounded-xl">
        <div class="mb-3 opacity-30"><svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/></svg></div>
        <p class="text-sm text-text-muted">No restore points found covering the selected items.</p>
        <p class="text-xs text-text-dim mt-1">Checked jobs: {relevantJobs.map(j => j.name).join(', ')}</p>
      </div>
    {:else}
      <RestorePointTimeline
        points={restorePoints}
        chosenPoints={chosenPoints}
        selectedId={selectedPoint?.id ?? null}
        recommendedId={recommendedRpId}
        onSelect={(rp) => handlePointSelect(rp)}
        onDelete={deleteRestorePoint}
        deletingId={deletingRpId}
        confirmDeleteId={confirmDeleteRpId}
        sizeFor={selectedRestoreSize}
        itemType={commonItemType(selectedItemsArray)}
        selectedItems={selectedItemsArray}
      />
      <p class="text-xs text-text-dim mt-3 text-center">{restorePoints.length} restore point{restorePoints.length !== 1 ? 's' : ''}</p>

      <!-- Step 2 Continue Button -->
      <div class="flex items-center justify-between mt-6">
        <button type="button" onclick={goBack}
          class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text rounded-lg border border-border bg-surface-2 transition-colors cursor-pointer">
          Back
        </button>
        <button type="button" onclick={() => { if (planComplete) step = 3 }} disabled={!planComplete}
          class="px-5 py-2 text-sm font-semibold text-white bg-vault hover:bg-vault-dark rounded-lg transition-all shadow-sm disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2 cursor-pointer">
          Continue
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
        </button>
      </div>
    {/if}

  <!-- Step 3: Restore options -->
  {:else if step === 3}
    <div class="mb-4">
      <button type="button" onclick={goBack}
        class="flex items-center gap-1.5 text-xs text-text-muted hover:text-text transition-colors cursor-pointer">
        <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/></svg>
        Back to versions
      </button>
    </div>

    <!-- Summary header -->
    <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6">
      <h3 class="text-sm font-semibold text-text mb-3">
        {restoreUnits.length === 1 ? 'Restore Summary' : 'Multi-Job Restore Plan'}
      </h3>
      <div class="space-y-2 text-sm">
        <div class="flex justify-between">
          <span class="text-text-muted">Total Items</span>
          <span class="text-text font-medium">
            {selectedCount} item{selectedCount !== 1 ? 's' : ''} across {restoreUnits.length} backup {restoreUnits.length === 1 ? 'job' : 'jobs'}
          </span>
        </div>
        {#if restoreUnits.length === 1}
          {@const unit = restoreUnits[0]}
          <div class="flex justify-between">
            <span class="text-text-muted">Job</span>
            <span class="text-text font-medium">{unit.jobName}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Restore Point</span>
            <span class="text-text">{formatDate(unit.point.created_at)}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Size</span>
            <span class="text-text">{formatBytes(selectedRestoreSize(unit.point, unit.items))}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Backup Type</span>
            <span class="text-text uppercase text-xs">{unit.point.backup_type}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-text-muted">Chain Health</span>
            <span class="inline-flex items-center gap-1">
              <span class="text-xs font-medium {chainHealthTone(unit.point)}">{chainHealthLabel(unit.point)}</span>
              {#if unit.point?.chain_status !== 'broken' && chainDependencies(unit.point) > 0}
                <Tooltip text="Chain length is {unit.point.chain_depth}. Restoring replays the base full backup plus intermediate backups in this chain." />
              {/if}
            </span>
          </div>
        {:else}
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 mt-2">
            {#each restoreUnits as unit (unit.jobId)}
              <div class="bg-surface-3/50 border border-border/60 rounded-lg p-3 text-xs">
                <div class="flex items-center justify-between font-medium text-text mb-1">
                  <span>{unit.jobName}</span>
                  <span class="{chainHealthTone(unit.point)}">{chainHealthLabel(unit.point)}</span>
                </div>
                <div class="text-text-dim text-[11px]">
                  {unit.items.length} item{unit.items.length !== 1 ? 's' : ''} · {formatDate(unit.point.created_at)} ({formatBytes(selectedRestoreSize(unit.point, unit.items))})
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <!-- Render configuration per unit -->
    {#each restoreUnits as unit (unit.jobId)}
      {@const s = getUnitSettings(unit.jobId)}
      {@const hasContainer = unit.items.some(item => item.type === 'container')}
      {@const hasPartial = unitHasPartialSelection(unit)}
      {@const unitEmpty = unitHasEmptySelection(unit)}
      {@const unitPendingList = unitHasPendingFileList(unit)}
      {@const needsAgePassphrase = unit.point?.encryption === 'age'}
      {@const isBroken = unit.point?.chain_status === 'broken'}
      {@const fresh = isUnitPreflightFresh(unit)}
      {@const preflight = s.preflightResult}

      <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6 space-y-5">
        {#if restoreUnits.length > 1}
          <div class="flex items-center justify-between border-b border-border pb-3 flex-wrap gap-2">
            <div class="flex items-center gap-2">
              <span class="text-sm font-bold text-text">{unit.jobName}</span>
              <span class="text-xs px-2 py-0.5 rounded-full bg-surface-3 text-text-dim font-medium uppercase">{unit.point.backup_type}</span>
              <span class="text-xs text-text-dim">({formatDate(unit.point.created_at)})</span>
            </div>
            <span class="text-xs font-medium {chainHealthTone(unit.point)}">{chainHealthLabel(unit.point)}</span>
          </div>
        {/if}

        <!-- Broken chain alert -->
        {#if isBroken}
          <div class="bg-danger/10 border border-danger/30 rounded-xl p-4 flex items-start gap-3">
            <svg aria-hidden="true" class="w-5 h-5 text-danger shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-7.938 4h15.876c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L2.33 16c-.77 1.333.192 3 1.732 3z"/>
            </svg>
            <div>
              <p class="text-sm font-medium text-danger">Restore chain is broken for {unit.jobName}</p>
              <p class="text-xs text-text-muted mt-0.5">{unit.point.chain_warning}</p>
              <button type="button" onclick={() => { step = 2 }}
                class="mt-2 text-xs font-semibold text-danger hover:underline cursor-pointer">
                ← Return to Step 2 to choose a different version for {unit.jobName}
              </button>
            </div>
          </div>
        {:else if chainDependencies(unit.point) > 0}
          <div class="bg-info/10 border border-info/30 rounded-xl p-4 flex items-start gap-3">
            <svg aria-hidden="true" class="w-5 h-5 text-info shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M12 22C6.477 22 2 17.523 2 12S6.477 2 12 2s10 4.477 10 10-4.477 10-10 10z"/>
            </svg>
            <div>
              <p class="text-sm font-medium text-info">Restore will replay the full chain</p>
              <p class="text-xs text-text-muted mt-0.5">This point depends on {chainDependencies(unit.point)} earlier backup{chainDependencies(unit.point) === 1 ? '' : 's'} and Vault will stage them before restoring.</p>
              {#if unit.point?.base_full_created_at}
                <p class="text-xs text-text-muted mt-0.5">Based on the full backup from {formatDate(unit.point.base_full_created_at)}{unit.point.base_full_size_bytes ? ` (${formatBytes(unit.point.base_full_size_bytes)})` : ''}.</p>
              {/if}
            </div>
          </div>
        {/if}

        {#if unit.point?.retention_preserved}
          <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 flex items-start gap-3">
            <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
            </svg>
            <div>
              <p class="text-sm font-medium text-warning">Retention is preserving this restore point</p>
              <p class="text-xs text-text-muted mt-0.5">{retentionPreservedMessage(unit.point)}</p>
            </div>
          </div>
        {/if}

        <!-- Assigned items pill badges -->
        <div>
          <p class="text-xs font-semibold text-text-muted uppercase tracking-wider mb-2">Items to restore</p>
          <div class="flex items-center gap-2 flex-wrap">
            {#each unit.items as item (itemKey(item))}
              <span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-surface-3 text-xs text-text font-medium">
                <svg aria-hidden="true" class="w-3.5 h-3.5 {itemTypeColor(item.type)}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={itemTypeIcon(item.type)}/></svg>
                {itemDisplayLabel(item)}
                <span class="text-text-dim">({item.type})</span>
              </span>
            {/each}
          </div>
        </div>

        <!-- Destination options -->
        <div>
          <p class="text-sm font-medium text-text-muted mb-2">Restore destination</p>
          <div class="space-y-2">
            <label class="flex items-center gap-2 cursor-pointer text-sm text-text">
              <input type="radio" name="rw_dest_{unit.jobId}" class="accent-vault" checked={!s.showDestOverride}
                onchange={() => updateUnitSetting(unit.jobId, { showDestOverride: false, restoreDestination: '', acknowledgeContainerRemap: false })} />
              Restore to original location
            </label>
            <label class="flex items-center gap-2 cursor-pointer text-sm text-text">
              <input type="radio" name="rw_dest_{unit.jobId}" class="accent-vault" checked={s.showDestOverride}
                onchange={() => updateUnitSetting(unit.jobId, { showDestOverride: true })} />
              Custom destination
            </label>
          </div>
          {#if s.showDestOverride}
            <div class="mt-2">
              <PathBrowser
                bind:value={() => s.restoreDestination, (v) => updateUnitSetting(unit.jobId, { restoreDestination: v })}
                onselect={(v) => updateUnitSetting(unit.jobId, { restoreDestination: v })}
                label="Custom restore destination"
              />
              <p class="text-xs text-text-dim mt-1">Files for {unit.jobName} will be written under this path instead of their original location.</p>
            </div>
            {#if hasContainer}
              <div class="mt-3 bg-warning/10 border border-warning/30 rounded-xl p-4">
                <p class="text-sm font-medium text-warning">This also changes the live container</p>
                <p class="text-xs text-text-muted mt-1">
                  Restoring a container to a custom destination stops it, removes it, and recreates it with its
                  volume mappings pointed at the new location. The Unraid template is rewritten to match.
                </p>
                <label class="flex items-start gap-2 cursor-pointer text-sm text-text mt-3">
                  <input type="checkbox" class="accent-vault mt-0.5" checked={s.acknowledgeContainerRemap}
                    onchange={(e) => updateUnitSetting(unit.jobId, { acknowledgeContainerRemap: e.target.checked })} />
                  <span>I understand the live container will be recreated and remapped</span>
                </label>
              </div>
            {/if}
          {/if}
        </div>

        <!-- Replace vs merge -->
        <div>
          <p class="text-sm font-medium text-text-muted mb-2">Existing files at destination</p>
          <label class="flex items-start gap-2 cursor-pointer text-sm text-text">
            <input type="checkbox" class="accent-vault mt-0.5" checked={s.cleanDestination}
              disabled={hasPartial}
              onchange={(e) => updateUnitSetting(unit.jobId, { cleanDestination: e.target.checked })} />
            <span>
              Clear the destination first
              <span class="block text-xs text-text-dim mt-0.5">
                {#if hasPartial}
                  Not available for a partial restore — clearing the destination would delete files you did not select.
                {:else if s.cleanDestination}
                  Anything at the destination not in this backup is deleted, so the restored result matches the backup exactly.
                {:else}
                  The backup is written over existing files. Anything not in the backup is left where it is.
                {/if}
              </span>
            </span>
          </label>
        </div>

        <!-- Passphrase if age encrypted -->
        {#if needsAgePassphrase}
          <div>
            <label for="rw_passphrase_{unit.jobId}" class="block text-sm font-medium text-text-muted mb-2">Encryption Passphrase ({unit.jobName})</label>
            <input id="rw_passphrase_{unit.jobId}" type="password" autocomplete="off" value={s.passphrase}
              oninput={(e) => updateUnitSetting(unit.jobId, { passphrase: e.target.value })}
              placeholder="Enter passphrase used to encrypt this backup"
              class="w-full sm:w-96 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
            <p class="text-xs text-text-dim mt-1">This backup uses age encryption. A passphrase is required to decrypt.</p>
          </div>
        {/if}

        <!-- Partial file picker for items in this unit -->
        <div class="space-y-3 pt-2">
          {#each unit.items as item (itemKey(item))}
            {@const entry = picker.get(itemKey(item))}
            {@const sel = entry?.selected?.size || 0}
            {@const total = entry?.totalFiles ?? (entry?.contents?.files?.length || 0)}
            {@const emptyContents = !!entry?.contents && total === 0}
            {#if !supportsFilePicker(item.type)}
              <div class="bg-surface-1 border border-border rounded-xl p-3 text-sm flex items-center justify-between gap-3">
                <span class="flex items-center gap-2 min-w-0">
                  <span class="font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
                  <span class="text-xs text-text-dim shrink-0">({item.type})</span>
                </span>
                <span class="text-xs text-text-muted shrink-0">
                  {item.type === 'vm' ? 'Restored in full (disk images, domain XML, NVRAM)' : 'Restored in full'}
                </span>
              </div>
            {:else}
              <details class="group bg-surface-1 border border-border rounded-xl" open={entry?.open || false}>
                <summary class="flex items-center justify-between gap-3 cursor-pointer select-none p-3 text-sm"
                  onclick={(e) => { e.preventDefault(); togglePickerOpen(item) }}>
                  <span class="flex items-center gap-2 min-w-0">
                    <svg aria-hidden="true" class="w-4 h-4 transition-transform group-open:rotate-90 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
                    <span class="font-medium text-text truncate" title={itemDisplayLabel(item)}>{itemDisplayLabel(item)}</span>
                    <span class="text-xs text-text-dim shrink-0">({item.type})</span>
                  </span>
                  <span class="text-xs text-text-muted">
                    {#if total > 0}
                      {#if sel === total}
                        <span class="text-emerald-400 font-medium">All {total} files selected</span>
                      {:else if sel > 0}
                        <span class="text-amber-400 font-medium">{sel} of {total} files selected</span>
                      {:else}
                        <span class="text-danger font-medium">0 of {total} files selected (none)</span>
                      {/if}
                    {:else if entry?.error}
                      <span class="text-danger">{entry.error}</span>
                    {:else if entry?.loading}
                      Loading…
                    {:else if emptyContents}
                      No restorable files
                    {:else if entry?.wholeItem}
                      Whole item will be restored
                    {:else}
                      Click to browse contents
                    {/if}
                  </span>
                </summary>
                {#if entry?.open}
                  <div class="border-t border-border p-3 space-y-2">
                    {#if entry.loading}
                      <p class="text-xs text-text-muted">Loading file list…</p>
                    {:else if entry.error && isPickerUnavailable(entry)}
                      <p class="text-xs text-danger">{entry.error}</p>
                      <p class="text-xs text-text-muted">If you continue without a file selection, the whole item is restored.</p>
                    {:else if entry.error}
                      <div class="bg-danger/10 border border-danger/30 rounded-lg p-2.5 flex items-center justify-between gap-3 text-xs">
                        <span class="text-danger">Could not load the file list: {entry.error}</span>
                        <span class="flex items-center gap-2 shrink-0">
                          <button type="button" onclick={() => loadPickerContents(item)}
                            class="px-2.5 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text font-medium cursor-pointer">
                            Retry
                          </button>
                          <button type="button" onclick={() => updateEntry(item, { error: '', errorStatus: 0, open: false, wholeItem: true })}
                            class="px-2.5 py-1 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text cursor-pointer">
                            Restore whole item
                          </button>
                        </span>
                      </div>
                    {:else if !entry.contents}
                      <p class="text-xs text-text-muted">No contents loaded.</p>
                    {:else if emptyContents}
                      <p class="text-xs text-text-muted">This backup captured no restorable files for this item.</p>
                    {:else}
                      <div class="flex flex-wrap items-center gap-2">
                        <input type="text" placeholder="Filter by path…" bind:value={entry.search}
                          class="flex-1 min-w-40 px-3 py-1.5 bg-surface-3 border border-border rounded-lg text-xs text-text placeholder-text-dim focus:outline-none focus:ring-1 focus:ring-vault" />
                        <button type="button" onclick={() => selectAllFiles(item)}
                          class="text-xs px-2.5 py-1.5 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text transition-colors cursor-pointer">Select all</button>
                        <button type="button" onclick={() => deselectAllFiles(item)}
                          class="text-xs px-2.5 py-1.5 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text transition-colors cursor-pointer">Deselect all</button>
                        <button type="button" onclick={() => expandAllFolders(item)} title="Expand all folders"
                          class="text-xs px-2.5 py-1.5 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text transition-colors cursor-pointer">Expand all</button>
                        <button type="button" onclick={() => collapseAllFolders(item)} title="Collapse all folders"
                          class="text-xs px-2.5 py-1.5 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text transition-colors cursor-pointer">Collapse all</button>
                      </div>
                      <div class="max-h-72 overflow-y-auto border border-border rounded-lg bg-surface-3/30 p-1">
                        {#if entry.tree && entry.tree.length > 0}
                          <FileTree
                            nodes={entry.tree}
                            selected={entry.selected}
                            expandedPaths={entry.expandedPaths}
                            search={entry.search}
                            ontoggle={(node) => toggleNodePicked(item, node)}
                            ontoggleexpand={(dirPath) => toggleFolderExpanded(item, dirPath)}
                          />
                        {/if}
                      </div>
                      {#if sel === total && total > 0}
                        <p class="text-xs text-text-muted">All {total} files selected. Entire backup will be restored.</p>
                      {:else if sel > 0 && sel < total}
                        <p class="text-xs text-info">Partial restore: restoring {sel} of {total} selected files.</p>
                      {:else if sel === 0 && total > 0}
                        <p class="text-xs text-danger font-medium">No files selected. You must select at least one file to restore, or remove {item.name} from the restore in Step 1.</p>
                      {/if}
                    {/if}
                  </div>
                {/if}
              </details>
            {/if}
          {/each}
        </div>

        {#if unitEmpty}
          <p class="text-xs text-danger font-medium">One or more items have 0 files selected. Please select at least one file or remove the item in Step 1.</p>
        {/if}
        {#if unitPendingList}
          <p class="text-xs text-danger font-medium">Wait for the file list to load, retry it, or choose to restore the whole item.</p>
        {/if}

        <!-- Preflight card per unit -->
        <div class="bg-surface-1 border border-border rounded-xl p-4">
          <div class="flex items-center justify-between gap-3">
            <div>
              <p class="text-sm font-medium text-text">Pre-flight checks ({unit.jobName})</p>
              <p class="text-xs text-text-dim mt-0.5">Confirm backup can be restored before starting.</p>
            </div>
            <button type="button" onclick={() => runPreflight(unit)} disabled={s.preflightRunning || (needsAgePassphrase && !s.passphrase)}
              class="text-xs px-3 py-1.5 rounded-lg border border-border text-text-muted hover:text-text hover:border-vault/40 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer inline-flex items-center gap-1.5 shrink-0">
              {#if s.preflightRunning}
                <svg class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                Checking…
              {:else}
                {preflight && fresh ? 'Re-check' : 'Run checks'}
              {/if}
            </button>
          </div>
          {#if preflight && !fresh}
            <p class="mt-3 text-xs text-text-dim">Inputs changed since the last check. Run the checks again.</p>
          {:else if preflight}
            <ul class="mt-3 space-y-1.5">
              {#each preflight.checks as c (c.id)}
                <li class="flex items-start gap-2 text-xs">
                  <span class="mt-0.5 shrink-0 {c.status === 'ok' ? 'text-success' : c.status === 'fail' ? 'text-danger' : c.status === 'warn' ? 'text-warning' : 'text-text-dim'}">
                    {#if c.status === 'ok'}
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="passed"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
                    {:else if c.status === 'fail'}
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="failed"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M6 18L18 6M6 6l12 12"/></svg>
                    {:else if c.status === 'warn'}
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="warning"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/></svg>
                    {:else}
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-label="skipped"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18 12H6"/></svg>
                    {/if}
                  </span>
                  <span class="text-text">{c.label}</span>
                  {#if c.detail}<span class="text-text-dim">· {c.detail}</span>{/if}
                </li>
              {/each}
            </ul>
            {#if !preflight.ok}
              <p class="text-xs text-danger mt-2">Resolve the failing checks above, then re-check before restoring.</p>
            {/if}
          {/if}
        </div>
      </div>
    {/each}

    <!-- Warning banner -->
    <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6 flex items-start gap-3">
      <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"/>
      </svg>
      <div>
        <p class="text-sm font-medium text-warning">This will overwrite existing data</p>
        <p class="text-xs text-text-muted mt-0.5">
          Restoring will replace current files for
          <strong class="text-text">{selectedCount} selected item{selectedCount !== 1 ? 's' : ''}</strong>
          with their backup versions. Files that are not in the backups are left where they are unless destination clearing is active.
        </p>
      </div>
    </div>

    <!-- Actions bar -->
    <div class="flex items-center gap-4 flex-wrap">
      <button type="button" onclick={doRestore}
        disabled={!canStartRestore}
        class="w-full sm:w-auto px-6 py-2.5 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2 cursor-pointer">
        {#if isRestoreRunning}
          <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
          Restoring...
        {:else}
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
          Start Restore {restoreUnits.length > 1 ? `(${restoreUnits.length} Jobs)` : ''}
        {/if}
      </button>

      {#if isDeduplicated && restoreUnits.length === 1}
        <button type="button" onclick={() => doMount(restoreUnits[0])} disabled={mounting || isRestoreRunning || !!mountedSession}
          class="w-full sm:w-auto px-4 py-2.5 text-sm font-medium text-text bg-surface-3 hover:bg-surface-4 border border-border rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2 cursor-pointer">
          {#if mounting}
            <svg aria-hidden="true" class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
            Mounting…
          {:else}
            <svg aria-hidden="true" class="w-4 h-4 text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
            Mount as Filesystem
          {/if}
        </button>
      {/if}

      {#if allUnitsPreflightFresh && !allUnitsPreflightPassing && !isRestoreRunning && !restoreUnits.some(u => u.point?.chain_status === 'broken')}
        <button type="button"
          disabled={!canRestoreAnyway}
          onclick={handleRestoreAnyway}
          class="text-xs text-text-dim hover:text-text underline cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:no-underline">
          Restore anyway
        </button>
      {:else if !isRestoreRunning && !allUnitsPreflightFresh && !restoreUnits.some(u => u.point?.chain_status === 'broken')}
        <button type="button" onclick={runAllPreflights} disabled={anyUnitPreflightRunning}
          class="text-xs text-text-dim hover:text-text underline cursor-pointer">
          Run all pre-flight checks to enable Start Restore
        </button>
      {/if}
    </div>

    {#if mountedSession}
      <div class="bg-surface-2 border border-emerald-500/40 rounded-xl p-4 mt-4 flex items-start justify-between gap-3">
        <div class="flex items-start gap-3">
          <svg aria-hidden="true" class="w-5 h-5 text-emerald-400 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
          </svg>
          <div>
            <p class="text-sm font-medium text-text">Backup mounted read-only</p>
            <p class="text-xs text-text-muted mt-1">Available at <code class="text-text font-mono bg-surface-3 px-1.5 py-0.5 rounded select-all">{mountedSession.mount_path}</code></p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" onclick={() => navigator.clipboard?.writeText(mountedSession.mount_path).catch(() => {})}
            class="text-xs px-2.5 py-1.5 rounded-lg border border-border bg-surface-3 hover:bg-surface-4 text-text transition-colors cursor-pointer">
            Copy Path
          </button>
        </div>
      </div>
    {/if}
    {#if mountError}
      <p class="text-xs text-danger font-medium mt-2">{mountError}</p>
    {/if}

    <!-- Live Restore Progress Bar & Unit Rows -->
    {#if isRestoreRunning}
      {@const progressItems = Object.entries(progress.itemProgress)}
      {@const activeItemPct = progressItems.reduce((maxPct, [, info]) => info.status === 'running' ? Math.max(maxPct, info.percent || 0) : maxPct, 0)}
      {@const overallPct = progress.overallTotal > 0 ? Math.min(100, Math.round((((progress.overallDone + progress.overallFailed) + (activeItemPct / 100)) / progress.overallTotal) * 100)) : activeItemPct}
      {@const elapsedStr = progress.elapsedSec >= 3600 ? `${Math.floor(progress.elapsedSec / 3600)}h ${Math.floor((progress.elapsedSec % 3600) / 60)}m` : progress.elapsedSec >= 60 ? `${Math.floor(progress.elapsedSec / 60)}m ${progress.elapsedSec % 60}s` : `${progress.elapsedSec}s`}

      <div class="bg-surface-2 border border-vault/30 rounded-xl p-4 mt-5" role="status" aria-live="polite">
        <div class="flex items-center gap-2 mb-3">
          <div class="w-2.5 h-2.5 rounded-full bg-vault animate-pulse shrink-0"></div>
          <span class="text-xs font-semibold uppercase tracking-wider text-text-muted">Restore in progress</span>
          {#if progress.activeRun?.job_name}
            <span class="ml-auto text-xs px-2 py-0.5 rounded-full bg-vault/15 text-vault font-medium truncate max-w-[45%]">{progress.activeRun.job_name}</span>
          {/if}
        </div>
        <div class="flex items-center justify-between text-xs text-text-muted mb-1.5">
          <span>Overall progress</span>
          <span class="font-mono text-text-dim tabular-nums font-medium">{overallPct}%</span>
        </div>
        <div class="w-full h-2.5 bg-surface-4 rounded-full overflow-hidden">
          <div class="h-full rounded-full transition-all duration-300 {overallPct < 100 ? 'shimmer-bar' : 'bg-vault'}" style="width: {overallPct}%"></div>
        </div>
        <div class="flex items-center justify-between text-xs text-text-dim mt-2 tabular-nums">
          <span>{progress.overallDone}/{progress.overallTotal} items · {elapsedStr}</span>
          {#if progress.overallFailed > 0}
            <span class="text-danger font-medium">{progress.overallFailed} failed</span>
          {/if}
        </div>
        {#if progress.currentItem}
          <p class="text-xs text-text-dim mt-2 truncate">
            Restoring: <span class="text-text font-medium">{progress.currentItem.name}</span>
            {#if progress.currentItem.item_type} <span class="text-text-muted">({progress.currentItem.item_type})</span>{/if}
          </p>
        {/if}
        {#if progress.phaseMessage}
          <p class="text-xs text-warning animate-pulse mt-1.5">{progress.phaseMessage}</p>
        {/if}

        <!-- Per-unit status rows -->
        {#if restoreUnits.length > 1}
          <div class="mt-4 pt-3 border-t border-border space-y-2">
            {#each restoreUnits as unit (unit.jobId)}
              {@const run = unitRuns.get(unit.jobId)}
              <div class="flex items-center justify-between text-xs p-2.5 rounded-lg bg-surface-3">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="font-medium text-text truncate">{unit.jobName}</span>
                  <span class="text-text-dim shrink-0">({unit.items.length} items)</span>
                </div>
                <div class="flex items-center gap-2 shrink-0">
                  {#if !run || run.state === 'queued'}
                    <span class="px-2 py-0.5 rounded-full bg-surface-4 text-text-dim font-medium">Queued</span>
                  {:else if run.state === 'running'}
                    <span class="px-2 py-0.5 rounded-full bg-vault/20 text-vault font-medium animate-pulse">Running</span>
                  {:else if run.state === 'completed'}
                    <span class="px-2 py-0.5 rounded-full bg-success/20 text-success font-medium">Completed ({run.done}/{run.total})</span>
                  {:else if run.state === 'partial'}
                    <span class="px-2 py-0.5 rounded-full bg-warning/20 text-warning font-medium">Partial ({run.done}/{run.total})</span>
                  {:else if run.state === 'failed' || run.state === 'rejected'}
                    <span class="px-2 py-0.5 rounded-full bg-danger/20 text-danger font-medium" title={run.error || ''}>Failed</span>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- Outcome Banner -->
    {#if restoreOutcome}
      <div class="mt-5 p-4 rounded-xl border flex items-start justify-between gap-3
        {restoreOutcome.status === 'completed' ? 'bg-success/10 border-success/30' : restoreOutcome.status === 'partial' ? 'bg-warning/10 border-warning/30' : 'bg-danger/10 border-danger/30'}">
        <div class="flex items-start gap-3">
          {#if restoreOutcome.status === 'completed'}
            <svg aria-hidden="true" class="w-5 h-5 text-success shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-success">Restore completed successfully</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.done}/{restoreOutcome.total} items restored{restoreOutcome.sizeBytes ? ` · ${formatBytes(restoreOutcome.sizeBytes)}` : ''}</p>
            </div>
          {:else if restoreOutcome.status === 'partial'}
            <svg aria-hidden="true" class="w-5 h-5 text-warning shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-warning">Restore completed with errors</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.done}/{restoreOutcome.total} items restored, {restoreOutcome.failed} failed{restoreOutcome.sizeBytes ? ` · ${formatBytes(restoreOutcome.sizeBytes)}` : ''}</p>
            </div>
          {:else}
            <svg aria-hidden="true" class="w-5 h-5 text-danger shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
            </svg>
            <div>
              <p class="text-sm font-semibold text-danger">Restore failed</p>
              <p class="text-xs text-text-muted mt-0.5">{restoreOutcome.failed} of {restoreOutcome.total} items failed to restore</p>
            </div>
          {/if}
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <button type="button" onclick={resetWizard}
            class="text-xs px-3 py-1.5 rounded-lg bg-surface-3 hover:bg-surface-4 text-text font-medium transition-colors cursor-pointer">
            Done
          </button>
          <button type="button" onclick={() => { restoreOutcome = null }}
            title="Dismiss notification" aria-label="Dismiss notification"
            class="p-1 text-text-dim hover:text-text rounded transition-colors cursor-pointer">
            <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
          </button>
        </div>
      </div>
    {/if}

    <!-- Live Restore Logs -->
    {#if restoreLogs.length > 0 || Array.from(unitRuns.values()).some(u => u.runId)}
      <div class="bg-surface-2 border border-border rounded-xl p-4 mt-4">
        <div class="flex items-center justify-between mb-2.5 flex-wrap gap-2">
          <div class="flex items-center gap-2">
            <svg aria-hidden="true" class="w-4 h-4 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/>
            </svg>
            <span class="text-xs font-semibold text-text">Restore Log</span>
            {#if restoreLogs.length > 0}
              <span class="text-[10px] px-1.5 py-0.5 rounded-full bg-surface-3 text-text-dim font-medium tabular-nums">{restoreLogs.length}</span>
            {/if}
          </div>
          {#if isRestoreRunning}
            <span class="flex items-center gap-1.5 text-[11px] text-vault font-medium">
              <span class="w-1.5 h-1.5 rounded-full bg-vault animate-ping"></span>
              Live
            </span>
          {/if}
        </div>

        {#if restoreUnits.length > 1 && Array.from(unitRuns.values()).some(u => u.runId)}
          <div class="flex items-center gap-1.5 mb-3 flex-wrap border-b border-border/50 pb-2">
            <span class="text-[11px] text-text-dim mr-1">Log source:</span>
            {#each Array.from(unitRuns.values()).filter(u => u.runId) as u (u.jobId)}
              <button type="button" onclick={() => switchLogUnit(u)}
                class="text-xs px-2.5 py-1 rounded-lg transition-colors cursor-pointer {selectedLogUnitJobId === u.jobId ? 'bg-vault text-white font-medium' : 'bg-surface-3 text-text-muted hover:text-text'}">
                {u.jobName}
              </button>
            {/each}
          </div>
        {/if}

        <div bind:this={logContainerEl} class="max-h-56 overflow-y-auto bg-surface-1 border border-border rounded-lg p-3 font-mono text-xs space-y-1.5 select-text">
          {#if restoreLogs.length === 0}
            <p class="text-text-dim">Waiting for logs…</p>
          {:else}
            {#each restoreLogs as entry (entry.id)}
              <div class="flex items-baseline gap-2">
                <span class="text-text-dim text-[10px] shrink-0 tabular-nums">{timeOnly(entry.ts)}</span>
                <span class="text-[10px] px-1 py-0.5 rounded uppercase font-semibold shrink-0 {entry.level === 'error' ? 'bg-danger/20 text-danger' : entry.level === 'warn' ? 'bg-warning/20 text-warning' : 'bg-surface-3 text-text-dim'}">{entry.level}</span>
                <span class="text-text-muted break-all flex-1 {entry.level === 'error' ? 'text-danger' : ''}">{entry.message}</span>
              </div>
            {/each}
          {/if}
        </div>
      </div>
    {/if}
  {/if}
</div>
