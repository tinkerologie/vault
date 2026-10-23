// A controllable stand-in for createUnifiedLogStore (src/lib/unifiedlog.svelte.js)
// used by the Logs.svelte component tests (#509). It exposes the same public
// contract (getters + actions) but lets a test set the data directly and hold
// load()/loadOlder() open with deferred promises, so loading, pagination and
// error states can be driven deterministically.
//
// The .svelte.js suffix is required: the Svelte plugin compiles the $state
// runes below, so Logs.svelte reacts to fixture changes like the real store.
import { vi } from 'vitest'
import { matchesSearch } from '../lib/logsearch.js'
import { deferred } from './logs-entries.js'

/**
 * Create a fixture store. Returns { store, control }: `store` is what
 * Logs.svelte sees, `control` is the test's handle on it.
 */
export function createFixtureStore({ entries = [], hasMore = false } = {}) {
  let _entries = $state(entries)
  let _loaded = $state(false)
  let _loading = $state(false)
  let _loadingOlder = $state(false)
  let _error = $state('')
  let _hasMore = $state(hasMore)
  let _pruned = $state(false)
  let _category = $state('')
  let _levelFilter = $state('')
  let _jobFilter = $state('')
  let _search = $state('')
  let _searching = $state(false)

  // Same filtering as the real store (unifiedlog.svelte.js), so `filtered`
  // and `entries` can never disagree.
  const matchesLevel = (e) =>
    !_levelFilter || (_levelFilter === 'warn' ? e.level === 'warn' || e.level === 'warning' : e.level === _levelFilter)
  const matchesJob = (e) => !_jobFilter || e.jobName === _jobFilter
  const filtered = $derived(_entries.filter((e) => matchesLevel(e) && matchesJob(e) && matchesSearch(e, _search)))
  const jobs = $derived(
    _entries.map((e) => e.jobName).filter((n, i, all) => n && all.indexOf(n) === i).sort(),
  )

  let heldLoad = null
  let heldOlder = null
  let olderBatch = null // entries appended when the next loadOlder settles

  const unsubscribe = vi.fn()

  const store = {
    get entries() { return _entries },
    get filtered() { return filtered },
    get jobs() { return jobs },
    get loaded() { return _loaded },
    get loading() { return _loading },
    get loadingOlder() { return _loadingOlder },
    get error() { return _error },
    get searching() { return _searching },
    get hasMore() { return _hasMore },
    get pruned() { return _pruned },
    get category() { return _category },
    get levelFilter() { return _levelFilter },
    get jobFilter() { return _jobFilter },
    get search() { return _search },

    load: vi.fn(async () => {
      _loading = true
      if (heldLoad) await heldLoad.promise
      _loading = false
      _loaded = true
    }),
    loadNewer: vi.fn(async () => {}),
    loadOlder: vi.fn(async () => {
      _loadingOlder = true
      try {
        if (heldOlder) await heldOlder.promise
        if (olderBatch) {
          _entries = [...olderBatch, ..._entries]
          olderBatch = null
        }
      } finally {
        _loadingOlder = false
      }
      return 'advanced'
    }),
    loadAll: vi.fn(async () => {}),
    setCategory: vi.fn(async (v) => { _category = v }),
    setLevelFilter: vi.fn((v) => { _levelFilter = v }),
    setJobFilter: vi.fn((v) => { _jobFilter = v }),
    setSearchFilter: vi.fn((v) => { _search = v }),
    setError: vi.fn((v) => { _error = v }),
    setupWs: vi.fn(() => unsubscribe),
    dispose: vi.fn(),
  }

  const control = {
    unsubscribe,
    setEntries(list) { _entries = list },
    /** Add entries newer than everything loaded (they render at the top). */
    addNewer(list) { _entries = [..._entries, ...list] },
    setHasMore(v) { _hasMore = v },
    setError(v) { _error = v },
    setPruned(v) { _pruned = v },
    /** Entries the next loadOlder() prepends to the (oldest-first) buffer. */
    setOlderBatch(list) { olderBatch = list },
    /** Keep the next load() pending until the returned handle resolves. */
    holdLoad() {
      heldLoad = deferred()
      return heldLoad
    },
    /** Keep loadOlder() pending (loadingOlder stays true) until resolved. */
    holdOlder() {
      heldOlder = deferred()
      return heldOlder
    },
    releaseOlder() {
      const h = heldOlder
      heldOlder = null
      h?.resolve()
    },
  }

  return { store, control }
}
