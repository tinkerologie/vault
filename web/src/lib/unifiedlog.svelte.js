// unifiedlog.svelte.js — merges activity-log entries with run-log lines into
// one chronologically-sorted stream for the unified console on the Logs page.
// Entries are sorted OLDEST-FIRST (newest at bottom, terminal-style).
//
// Loading strategy:
//   • Initial load fetches the most recent batch of activity entries (default 30).
//   • Terminal entries (run_log marker) are automatically expanded: their
//     run-log lines are fetched and interleaved.
//   • Older entries are loaded automatically via scroll-to-top detection
//     (loadOlder called from the component's scroll handler).
//   • Total entries in memory are capped at MAX_ENTRIES; oldest are pruned.
//   • Duplicate activity+summary entries for the same run are deduped
//     (backend logs both an activity entry and a run-log summary for
//     backup/restore completions — only the run-log summary is kept).

import { api } from './api.js'
import { onWsMessage } from './ws.svelte.js'
import { planBatch } from './logbatch.js'
import { matchesSearch } from './logsearch.js'

const BATCH_SIZE = 30       // activity entries per interactive page
const FULL_LOAD_LIMIT = 1000 // activity entries per background full-history page (API clamps to 1000)
const OLDER_BATCH_BUDGET = 150 // max unified entries added per loadOlder step
// Hard cap on unified entries in memory. The console is designed to hold the
// ENTIRE history (loadAll pins it, search loads it), so this is a safety
// bound for pathological histories only — the DB itself caps activity_log at
// MaxActivityLogRows (10k), and run-log lines are capped per run (#328).
const MAX_ENTRIES = 50000
const WS_DEDUP_CAP = 200    // seen-IDs cap for WS dedup
const MIN_LOAD_OLDER_MS = 700 // floor for user-initiated loadOlder so the spinner reads as real work (#328 round 2)

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export function createUnifiedLogStore() {
  let _entries = $state([])
  let _loaded = $state(false)
  let _loading = $state(false)
  let _error = $state('')
  let _hasMore = $state(false)
  let _loadingOlder = $state(false)
  let _oldestActivityId = $state(null) // cursor for "load older"
  let _category = $state('')
  let _levelFilter = $state('')
  let _jobFilter = $state('')
  let _search = $state('')
  let _searching = $state(false) // background auto-load of older entries during search (#328)
  let _pruned = $state(false)    // prune dropped oldest entries since the last fresh load (#328)
  // Plain objects used as sets (key = id, value = true). Avoids svelte/prefer-svelte-reactivity
  // lint rule on Set/Map constructors inside .svelte.js files.
  let _expanded = {}   // activity IDs whose run-log has been fetched
  let _seen = {}       // WS dedup ring
  let _loadSeq = 0     // load() sequence token: stale async responses are discarded
  let _contextSeq = 0  // bumped on every fresh load/reset: aborts an in-flight loadAll (#328)
  let _pendingRunLogs = [] // leftover run-log lines from a split terminal expansion (#328 r4 #5)
  let _fullHistoryLoaded = false // the buffer is (or is intended to become) the whole history (#328)

  function reset() {
    _entries = []
    _loaded = false
    _loading = false
    _error = ''
    _hasMore = false
    _loadingOlder = false
    _oldestActivityId = null
    _expanded = {}
    _seen = {}
    _pendingRunLogs = []
    _pruned = false
    _fullHistoryLoaded = false
    _contextSeq++
  }

  // Category is applied server-side via load(); the job and level filters are
  // client-side and independent — switching category must NOT clear them, so
  // filters compose (#328 round 2).
  async function setCategory(v) {
    _category = v
    await load()
    // If the active job filter no longer appears in the (category-filtered)
    // buffer, fall back to All so the Job dropdown doesn't render blank when
    // the category has no entries for that job (#328 r3 #1). (Level and
    // category options are static, so only the dynamic Job list can go blank.)
    if (_jobFilter && !jobs.includes(_jobFilter)) {
      _jobFilter = ''
    }
    // Background: load the new category's entire history so the console
    // stays uniformly scrollable to the true end (#328).
    loadAll()
  }
  function setLevelFilter(v) { _levelFilter = v }
  function setJobFilter(v) { _jobFilter = v }
  function setSearchFilter(v) {
    _search = v
    if (!v) {
      _searching = false
      return
    }
    // Search is client-side over the loaded buffer, so a term that only
    // matches an OLDER (not-yet-loaded) log would prematurely show "No
    // logs". Load the ENTIRE history, then let the client-side filter
    // narrow it: stopping at the first match hid deeper matches and left
    // the tail of history unreachable while a query was active (#328 issue 2).
    if (_searching) return                 // guard against concurrent loops
    if (!_hasMore && !_pruned) return      // buffer already spans the whole history
    _searching = true
    ;(async () => {
      try {
        if (!_hasMore && _pruned) {
          // The cursor is exhausted but the buffer was truncated (the scroll
          // window dropped its oldest entries): a search over this window
          // would miss the dropped older logs. Reload from the newest page
          // so the search covers the whole history (#328).
          await load()
        }
        while (_search && _hasMore) {
          // Big pages + silent: a full-history search must not make ~333
          // round-trips of BATCH_SIZE, and a transient error mid-search must
          // not surface as the console-wide error box (the scroll path
          // reports errors; the background search just retries) (#328).
          await loadOlder({ limit: FULL_LOAD_LIMIT, silent: true })
        }
        // The loop ended because _hasMore is false: the buffer now IS the
        // whole history. Pin it — pruning would drop the oldest entries
        // while _hasMore stays false, so the "— End of logs —" marker would
        // display over a truncated buffer and lie about the end (#328).
        // (Guard on _search: a query cleared mid-loop aborts the load.)
        if (_search) _fullHistoryLoaded = true
      } finally {
        _searching = false
      }
    })()
  }

  // ---- Unified entry shape ----
  // { id, ts, level, message, type, category, jobName, jobId, runId, meta }

  function makeUnifiedEntry(raw, overrides = {}) {
    const meta = raw.details ? tryParse(raw.details) : null
    return {
      id: raw.id,
      ts: raw.created_at || raw.ts,
      level: raw.level,
      message: raw.message,
      type: overrides.type || 'activity',
      category: raw.category || 'backup',
      jobName: overrides.jobName || '',
      jobId: overrides.jobId || null,
      // Normalize run_id out of the details JSON so dedupe can correlate
      // activity entries with their run-log summaries (details is stored as
      // a JSON string like {"run_id":203,...} in the activity_log table).
      runId: overrides.runId || meta?.run_id || null,
      meta,
      ...overrides,
    }
  }

  function makeRunLogUnified(rle, activityMeta) {
    return {
      id: `rl-${rle.id}`,
      ts: rle.ts,
      level: rle.level || 'info',
      message: rle.message,
      type: 'runlog',
      category: activityMeta?.category || 'backup',
      jobName: activityMeta?.jobName || '',
      jobId: activityMeta?.jobId || null,
      runId: rle.run_id,
      meta: rle.data ? tryParse(rle.data) : null,
    }
  }

  // ---- Parsing helpers ----

  function tryParse(str) {
    if (!str) return null
    if (typeof str === 'object') return str
    try { return JSON.parse(str) } catch { return null }
  }

  // Extract job name/id from activity details
  function jobFromDetails(parsed) {
    if (!parsed) return {}
    return {
      jobName: parsed.job_name || '',
      jobId: parsed.job_id || null,
    }
  }

  // Parse an ISO timestamp string to epoch ms.
  function tsMs(ts) {
    const n = Date.parse(ts)
    return Number.isNaN(n) ? 0 : n
  }

  // ---- Loading ----

  // load() — full reset: fetch the newest page and REPLACE the buffer.
  // Used on mount, category switch, and purge.
  async function load() {
    return _loadNewest(true)
  }

  // loadNewer() — incremental top-up: fetch the newest page and MERGE it into
  // the scrollback (idempotent; dedup by id). This is the "newer" side of the
  // cursor-keyed lazy-load (the "older" side is loadOlder, keyed on
  // _oldestActivityId). Used by the poll timer and scroll-to-bottom refresh.
  async function loadNewer() {
    return _loadNewest(false)
  }

  // _loadNewest fetches the newest activity page and expands its terminal
  // entries. With fresh=true the console is replaced; with fresh=false the
  // batch is MERGED into the loaded scrollback so poll cycles don't discard
  // "Load older" history.
  async function _loadNewest(fresh) {
    const seq = ++_loadSeq
    _loading = true
    _error = ''
    try {
      const activityEntries = (await api.getActivity(BATCH_SIZE, _category)) || []
      if (seq !== _loadSeq) return

      // Terminal entries (run_log flag) carry the run whose log expands.
      // The fresh path fetches and materializes them all (bounded by
      // planBatch); the merge path only handles first-seen ones.
      const terminal = activityEntries.filter(e => {
        const d = tryParse(e.details)
        return d?.run_log === true
      })

      // Seed the WS dedup ring with fetched ids so a broadcast racing this
      // fetch can't insert a duplicate of an entry we already have.
      for (const ae of activityEntries) _seen[ae.id] = true

      const oldestFetched = activityEntries.length > 0 ? activityEntries[activityEntries.length - 1].id : null

      if (fresh) {
        _expanded = {}
        _pendingRunLogs = []
        _pruned = false
        _fullHistoryLoaded = false
        _contextSeq++

        // Expand terminal entries in parallel, then merge. Only the fresh
        // path fetches: the merge path must NOT refetch every terminal on
        // the page each poll (bounded, idempotent — see the else branch).
        const runLogs = await fetchRunLogsForBatch(terminal)
        if (seq !== _loadSeq) return

        // Per-entry unified rows, newest-first. The fresh path runs these
        // through planBatch so an oversized terminal run's expansion is
        // bounded and split (overflow drains via _pendingRunLogs), instead
        // of dumping every run-log line on the initial load (#328 r5 #8).
        const perEntry = activityEntries.map(ae => buildUnified([ae], runLogs))
        // The step budget bounds RUN-LOG lines only: plain activity rows
        // (health checks, started rows) are one line each and must always
        // materialize — an oversized terminal expansion must not defer the
        // rest of the newest page out of the first paint (the console
        // visibly flipped when those rows appeared on the next merge).
        const { fullCount, split } = planBatch(
          perEntry.map(list => list.filter(e => e.type === 'runlog').length),
          OLDER_BATCH_BUDGET,
        )
        let newUnified = perEntry.slice(0, fullCount).flat()
        let consumed = fullCount
        if (split > 0) {
          const splitEntry = perEntry[fullCount]
          newUnified = [...newUnified, ...splitEntry.slice(0, split)]
          // A terminal run's log ends in its "… finished" summary — the line an
          // operator most needs. Keep that tail line in this batch and defer
          // only the middle lines, so the terminal status renders immediately
          // instead of surfacing on a later load-older step (#328 r9 #5).
          const middle = splitEntry.slice(split, -1)
          const tail = splitEntry.slice(-1)
          if (tail.length > 0) newUnified = [...newUnified, ...tail]
          if (middle.length > 0) _pendingRunLogs = [..._pendingRunLogs, ...middle]
          consumed++
        }
        // Everything after the split point: materialize the activity rows now
        // (they cost no budget) and defer only the run-log lines to the
        // pending drain, so the newest page is fully present at first paint.
        for (let i = consumed; i < perEntry.length; i++) {
          const rows = perEntry[i].filter(e => e.type !== 'runlog')
          if (rows.length > 0) newUnified = [...newUnified, ...rows]
          const rl = perEntry[i].filter(e => e.type === 'runlog')
          if (rl.length > 0) _pendingRunLogs = [..._pendingRunLogs, ...rl]
        }
        // Every terminal on the page is now accounted for: its lines are in
        // the buffer or in _pendingRunLogs, so no later step re-expands it.
        for (const ae of activityEntries) {
          const d = tryParse(ae.details)
          if (d?.run_log === true) _expanded[ae.id] = true
        }
        _entries = prune(dedupeSummaries(newUnified).sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
        _oldestActivityId = activityEntries.length > 0 ? activityEntries[activityEntries.length - 1].id : null
        _hasMore = (activityEntries.length === BATCH_SIZE) || (_pendingRunLogs.length > 0)
      } else {
        // Merge path (poll / scroll-to-bottom refresh). Bounded and
        // idempotent: already-expanded terminals are NOT refetched or
        // re-materialized (their lines are already in the buffer or in
        // _pendingRunLogs), and first-seen expansions are split through
        // planBatch exactly like the fresh path (#328).
        //
        // First, drain deferred lines from a previously split terminal at
        // the same bounded rate loadOlder uses. This matters when the
        // buffer is pinned to the full history (_hasMore false): nothing
        // else would drive the drain (loadAll and the scroll path both
        // require _hasMore), so a long run first seen mid-poll would
        // otherwise stall mid-log. _hasMore is intentionally NOT touched
        // here (see the note below).
        if (_pendingRunLogs.length > 0) {
          const take = _pendingRunLogs.slice(0, OLDER_BATCH_BUDGET)
          _pendingRunLogs = _pendingRunLogs.slice(OLDER_BATCH_BUDGET)
          const seenIds = {}
          const merged = [...take, ..._entries].filter(e => {
            if (seenIds[e.id]) return false
            seenIds[e.id] = true
            return true
          })
          _entries = prune(dedupeSummaries(merged).sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
        }

        // Only terminal entries NOT already expanded need their run log
        // fetched. Exception — the catch-up that surfaces a missing
        // terminal summary: an expanded terminal whose expansion produced
        // no runlog lines (transient fetch failure, or a run that only
        // started logging after the first fetch) is retried so its lines
        // (or its summary) can still appear on a later poll.
        const needExpansion = terminal.filter(ae => {
          if (!_expanded[ae.id]) return true
          const runId = tryParse(ae.details)?.run_id
          return runId != null &&
            !_entries.some(e => e.type === 'runlog' && e.runId === runId) &&
            !_pendingRunLogs.some(e => e.type === 'runlog' && e.runId === runId)
        })
        const runLogs = await fetchRunLogsForBatch(needExpansion)
        if (seq !== _loadSeq) return
        for (const ae of needExpansion) _expanded[ae.id] = true

        // Materialize only what contributes something new: plain activity
        // rows plus terminals that were (re)expanded this step. Already
        // expanded terminals contribute nothing — their lines are in the
        // buffer and their summary rows were superseded by those lines.
        const expansionIds = Object.fromEntries(needExpansion.map(ae => [ae.id, true]))
        const needsMaterialization = activityEntries.filter(ae => {
          const d = tryParse(ae.details)
          if (d?.run_log === true) return expansionIds[ae.id] === true
          return true
        })
        const perEntry = needsMaterialization.map(ae => buildUnified([ae], runLogs))
        const { fullCount, split } = planBatch(
          perEntry.map(list => list.filter(e => e.type === 'runlog').length),
          OLDER_BATCH_BUDGET,
        )
        let newUnified = perEntry.slice(0, fullCount).flat()
        let consumed = fullCount
        if (split > 0) {
          const splitEntry = perEntry[fullCount]
          newUnified = [...newUnified, ...splitEntry.slice(0, split)]
          // Same as the fresh path: never defer a run's terminal summary.
          const middle = splitEntry.slice(split, -1)
          const tail = splitEntry.slice(-1)
          if (tail.length > 0) newUnified = [...newUnified, ...tail]
          if (middle.length > 0) _pendingRunLogs = [..._pendingRunLogs, ...middle]
          consumed++
        }
        // Same as the fresh path: activity rows always materialize; only
        // run-log lines are budgeted and deferred to the pending drain.
        for (let i = consumed; i < perEntry.length; i++) {
          const rows = perEntry[i].filter(e => e.type !== 'runlog')
          if (rows.length > 0) newUnified = [...newUnified, ...rows]
          const rl = perEntry[i].filter(e => e.type === 'runlog')
          if (rl.length > 0) _pendingRunLogs = [..._pendingRunLogs, ...rl]
        }
        const seenIds = {}
        const merged = [...newUnified, ..._entries].filter(e => {
          if (seenIds[e.id]) return false
          seenIds[e.id] = true
          return true
        })
        _entries = prune(dedupeSummaries(merged).sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
        if (oldestFetched != null) {
          _oldestActivityId = _oldestActivityId != null ? Math.min(_oldestActivityId, oldestFetched) : oldestFetched
        }
        // NOTE: do NOT touch _hasMore here. loadNewer fetches the NEWEST page,
        // which tells us nothing about whether OLDER entries remain. Flipping
        // _hasMore back to true on a full page made the "End of logs" marker
        // vanish ~30s after the user reached the end (#328 r10 #1).
      }

      _loaded = true
    } catch (e) {
      if (seq === _loadSeq) _error = e.message || 'Failed to load logs'
    } finally {
      if (seq === _loadSeq) _loading = false
    }
  }

  async function loadOlder({ smooth = false, limit = BATCH_SIZE, silent = false } = {}) {
    if (_loadingOlder) return
    if (!_hasMore && _pendingRunLogs.length === 0) return
    _loadingOlder = true
    const startedAt = Date.now()
    let loaded = false
    try {
      // Drain leftover run-log lines from a previously split terminal
      // expansion first, so a long run's log is inserted across bounded
      // steps instead of one unbounded dump (#328 r4 #5). A drain step
      // inserts at most OLDER_BATCH_BUDGET lines and then returns; the
      // activity fetch resumes on a later step once pending is empty.
      if (_pendingRunLogs.length > 0) {
        const take = _pendingRunLogs.slice(0, OLDER_BATCH_BUDGET)
        _pendingRunLogs = _pendingRunLogs.slice(OLDER_BATCH_BUDGET)
        _entries = prune(dedupeSummaries([..._entries, ...take]).sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
        loaded = true
        return
      }

      if (!_hasMore || !_oldestActivityId) return
      const older = (await api.getActivity(limit, _category, _oldestActivityId)) || []
      if (older.length === 0) { _hasMore = false; return }
      loaded = true

      const terminal = older.filter(e => {
        const d = tryParse(e.details)
        return d?.run_log === true && !_expanded[e.id]
      })
      const runLogs = await fetchRunLogsForBatch(terminal)

      const perEntry = older.map(ae => buildUnified([ae], runLogs))
      // The step budget bounds RUN-LOG lines only; plain activity rows
      // always materialize (same rationale as the fresh path).
      const { fullCount, split } = planBatch(
        perEntry.map(list => list.filter(e => e.type === 'runlog').length),
        OLDER_BATCH_BUDGET,
      )

      // Consume fullCount entries in full, plus `split` lines of the next
      // terminal entry (whose expansion exceeded the remaining budget).
      let newUnified = perEntry.slice(0, fullCount).flat()
      let consumed = fullCount
      if (split > 0) {
        const splitEntry = perEntry[fullCount]
        newUnified = [...newUnified, ...splitEntry.slice(0, split)]
        // Same as the fresh path: never defer a run's terminal summary.
        const middle = splitEntry.slice(split, -1)
        const tail = splitEntry.slice(-1)
        if (tail.length > 0) newUnified = [...newUnified, ...tail]
        if (middle.length > 0) {
          _pendingRunLogs = [..._pendingRunLogs, ...middle]
        }
        consumed++
      }
      for (let i = consumed; i < perEntry.length; i++) {
        const rows = perEntry[i].filter(e => e.type !== 'runlog')
        if (rows.length > 0) newUnified = [...newUnified, ...rows]
        const rl = perEntry[i].filter(e => e.type === 'runlog')
        if (rl.length > 0) _pendingRunLogs = [..._pendingRunLogs, ...rl]
      }

      // Every terminal entry in this batch is accounted for: its run log is
      // captured either in the buffer or in _pendingRunLogs, so a later
      // fetch must not re-expand it.
      for (const ae of older) {
        const d = tryParse(ae.details)
        if (d?.run_log === true) _expanded[ae.id] = true
      }

      _oldestActivityId = older.length > 0 ? older[older.length - 1].id : null
      _hasMore = (older.length === limit) || (_pendingRunLogs.length > 0)

      const combined = [..._entries, ...newUnified]
      _entries = prune(dedupeSummaries(combined).sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
    } catch (e) {
      // Background full-history loads (loadAll) must not surface a transient
      // fetch error as the console-wide error box — they just stop and leave
      // the scroll path (which reports errors) to retry (#328).
      if (!silent) _error = e.message || 'Failed to load older logs'
    } finally {
      // Artificial minimum latency (see MIN_LOAD_OLDER_MS). Only hold the
      // floor when a batch actually loaded — an empty page means the user is
      // already at the oldest log, and lingering would show the spinner with
      // nothing to show (#328 r3 #4).
      if (smooth && loaded) {
        const remaining = MIN_LOAD_OLDER_MS - (Date.now() - startedAt)
        if (remaining > 0) await sleep(remaining)
      }
      _loadingOlder = false
    }
  }

  // loadAll() — background full-history load: keep pulling older pages until
  // the cursor is exhausted, so the console spans the ENTIRE history and can
  // be scrolled to the true end (where the "— End of logs —" marker shows).
  // This is the uniform model: the search view already loads everything and
  // filters client-side; the standard view now does the same (#328).
  // Big pages (FULL_LOAD_LIMIT) keep the round-trip count low. Stale loops
  // abort via _contextSeq (fresh load / category switch / reset).
  async function loadAll() {
    if (_fullHistoryLoaded && !_hasMore) return // already complete
    _fullHistoryLoaded = true
    const ctx = _contextSeq
    try {
      while (ctx === _contextSeq && _hasMore && !_error) {
        await loadOlder({ limit: FULL_LOAD_LIMIT, silent: true })
      }
    } finally {
      // If the loop was aborted by a context change, the fresh load already
      // reset _fullHistoryLoaded; leave it otherwise so the pinned buffer
      // keeps the marker honest.
      if (ctx === _contextSeq) _fullHistoryLoaded = !_hasMore ? true : _fullHistoryLoaded
    }
  }

  // Build unified entries from activity entries and their run-log expansions.
  function buildUnified(activityEntries, runLogs) {
    const unified = []
    for (const ae of activityEntries) {
      const d = tryParse(ae.details)
      const job = jobFromDetails(d)
      const runId = d?.run_id || null
      if (d?.run_log === true && runId) {
        const rlEntries = runLogs[runId] || []
        for (const rle of rlEntries) {
          unified.push(makeRunLogUnified(rle, { category: ae.category, ...job }))
        }
        // The run-log lines end with the run's own summary line
        // ("Backup finished status=…"), which already represents the
        // completion — keeping the terminal activity row here would show
        // the same completion twice. Keep the activity-derived summary row
        // only when the expansion produced no lines (run-log fetch failed
        // or the run pre-dates run-logging).
        if (rlEntries.length === 0) {
          unified.push(makeUnifiedEntry(ae, { type: 'summary', runId, ...job }))
        }
      } else if (d?.job_id) {
        unified.push(makeUnifiedEntry(ae, { runId, ...job }))
      } else {
        unified.push(makeUnifiedEntry(ae, { runId }))
      }
    }
    return unified
  }

  // Dedupe: a run's streamed run-log lines end with the run's own summary
  // line, which supersedes the terminal summary row for the same run_id.
  // Plain activity entries (e.g. "Backup started") are kept — they are no
  // longer duplicated in the run-log stream (the server-side run-log
  // "started" line was removed, #328), so they must survive. Also collapse
  // duplicate summaries (same run_id).
  function dedupeSummaries(arr) {
    const runsWithRunlogLines = {}   // runs with actual streamed run-log lines
    for (const e of arr) {
      if (e.type === 'runlog' && e.runId) {
        runsWithRunlogLines[e.runId] = true
      }
    }
    const seen = {}
    return arr.filter(e => {
      // Run-log lines (which end with the run's own summary line) supersede
      // the terminal summary row for the same run.
      if (e.type === 'summary' && e.runId && runsWithRunlogLines[e.runId]) return false
      const key = e.type === 'summary' && e.runId ? `s-${e.runId}` : e.id
      if (seen[key]) return false
      seen[key] = true
      return true
    })
  }

  async function fetchRunLogsForBatch(terminalActivityEntries) {
    const ids = terminalActivityEntries.map(ae => tryParse(ae.details)?.run_id).filter(Boolean)
    const runIds = Object.fromEntries(ids.map(id => [id, true]))
    if (Object.keys(runIds).length === 0) return {}
    const results = {}
    await Promise.all(Object.keys(runIds).map(async runId => {
      try {
        // Tail-first (limit 1000 = the repo's ceiling): the run's terminal
        // summary is its NEWEST line, and a head-first fetch would never
        // reach it for runs longer than the limit — the console would show
        // the run as mid-flight forever. The tail window always ends with
        // the summary line (status/size/duration) (#328).
        const res = await api.getRunLogs(runId, { tail: true, limit: 1000 })
        if (res?.entries?.length) results[runId] = res.entries
      } catch { /* partial failure ok */ }
    }))
    return results
  }

  function prune(arr) {
    if (arr.length <= MAX_ENTRIES) return arr
    // During an active search the whole point of loadOlder is to surface
    // matches that may live far back in history. Dropping the oldest entries
    // (what slice(-MAX_ENTRIES) does) would immediately discard the
    // just-loaded older batch, so "older logs" never render during a search.
    if (_search) return arr
    // A pinned full-history buffer (_fullHistoryLoaded, _hasMore false) used
    // to skip pruning so the "— End of logs —" marker wouldn't lie over a
    // truncated buffer — but that let a long-lived page grow without bound
    // (WS arrivals + poll merges). Capping from the oldest side here keeps
    // the marker honest instead: the UI gates the marker on _pruned, and a
    // subsequent search sees _pruned and reloads from the newest page before
    // searching (see setSearchFilter) (#328).
    _pruned = true
    return arr.slice(-MAX_ENTRIES)
  }

  // ---- Filters ----

  function matchesLevel(entry) {
    if (!_levelFilter) return true
    if (_levelFilter === 'warn') return entry.level === 'warn' || entry.level === 'warning'
    return entry.level === _levelFilter
  }

  function matchesJob(entry) {
    if (!_jobFilter) return true
    return entry.jobName === _jobFilter
  }

  let filtered = $derived(
    _entries.filter(e => matchesLevel(e) && matchesJob(e) && matchesSearch(e, _search))
  )

  let jobs = $derived.by(() => {
    const seen = {}
    for (const e of _entries) {
      const key = e.jobName || ''
      if (key && !seen[key]) seen[key] = true
    }
    return Object.keys(seen).sort()
  })

  // ---- WS real-time ----

  function setupWs() {
    const unsub = onWsMessage((msg) => {
      if (msg.type === 'activity' && msg.entry) {
        if (_category && msg.entry.category !== _category) return
        if (_seen[msg.entry.id]) return
        _seen[msg.entry.id] = true
        const keys = Object.keys(_seen)
        if (keys.length > WS_DEDUP_CAP) delete _seen[keys[0]]

        const ae = msg.entry
        const d = tryParse(ae.details)
        const job = jobFromDetails(d)
        const runId = d?.run_id || null
        const newEntries = []

        if (d?.run_log === true && runId) {
          _expanded[ae.id] = true
          // Live run-log lines for this run have already streamed in — the
          // run's own summary line ("Backup finished status=…") represents
          // the completion, so the activity row would duplicate it. Keep the
          // row only when nothing streamed (fetch failure / legacy data).
          if (!_entries.some(e => e.type === 'runlog' && e.runId === runId)) {
            newEntries.push(makeUnifiedEntry(ae, { type: 'summary', runId, ...job }))
          }
        } else {
          // Plain activity row (e.g. "Backup started") — keep it. The
          // run-log "started" line was removed server-side, so this is the
          // only "started" emission and must not be dropped (#328).
          newEntries.push(makeUnifiedEntry(ae, { runId, ...job }))
        }

        _entries = prune([..._entries, ...newEntries].sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
      } else if (msg.type === 'run_log' && msg.entry) {
        const rle = msg.entry
        const ctx = _entries.find(e => e.runId === rle.run_id && e.type === 'summary') ||
                     _entries.find(e => e.runId === rle.run_id)
        // Category-filtered consoles must not leak lines from runs outside
        // the active category. The activity branch filters on the entry's
        // own category, but a run-log line's category is only known via its
        // run's context in the buffer — so resolve it and drop the line
        // when the run is unknown or belongs to another category. (A live
        // run in the active category always has its activity row in the
        // buffer by the time its lines stream, since the run must start
        // before it logs; a missed line self-heals on the next poll.)
        if (_category && ctx?.category !== _category) return
        const newRl = makeRunLogUnified(rle, {
          category: ctx?.category || 'backup',
          jobName: ctx?.jobName || '',
          jobId: ctx?.jobId || null,
        })
        // Streamed run-log lines no longer supersede the "Backup started"
        // activity row (the run-log "started" line was removed server-side,
        // #328), so plain activity entries stay. Terminal-summary dedup is
        // handled in the activity branch above. The line is inserted in
        // chronological position, NOT appended: a streamed line for a run
        // that is mid-buffer (older than the newest entries) must not render
        // at the bottom as if it were the newest log — the next sorted merge
        // would yank it back and the visible set at the bottom would flip
        // (the "set replaced by another set" flash on refresh during a run).
        _entries = prune([..._entries, newRl].sort((a, b) => tsMs(a.ts) - tsMs(b.ts)))
      }
    })
    return unsub
  }

  return {
    get entries() { return _entries },
    get filtered() { return filtered },
    get loaded() { return _loaded },
    get loading() { return _loading },
    get loadingOlder() { return _loadingOlder },
    get error() { return _error },
    setError(v) { _error = v },
    get hasMore() { return _hasMore },
    get pruned() { return _pruned },
    get jobs() { return jobs },
    get category() { return _category },
    get levelFilter() { return _levelFilter },
    get jobFilter() { return _jobFilter },
    get search() { return _search },
    get searching() { return _searching },
    reset, load, loadNewer, loadOlder, loadAll, setCategory, setLevelFilter, setJobFilter, setSearchFilter, setupWs,
  }
}
