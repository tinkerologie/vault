// Guards for work that hasn't been saved yet (#687).
//
// Two pieces:
// - snapshot()/isDirty(): compare a form against the copy taken when it opened,
//   so an untouched form closes without asking.
// - createDialogHistory(): give an open dialog its own browser history entry,
//   so Back (or the Android back gesture) closes the dialog instead of leaving
//   the page. The hash router only reacts to hashchange, and an entry pushed
//   with the same URL changes nothing but history.state, so popping it fires
//   popstate alone and the page stays mounted.

/** A comparable copy of plain form data. */
export function snapshot(value) {
  return JSON.stringify(value ?? null)
}

/** True when value differs from a snapshot taken earlier. A missing baseline is clean. */
export function isDirty(baseline, value) {
  return baseline != null && baseline !== snapshot(value)
}

let seq = 0

/**
 * @param {Window} [win]
 */
export function createDialogHistory(win = globalThis.window) {
  /** @type {string | null} */
  let token = null
  /** @type {(() => void) | null} */
  let onBack = null
  // Pops this helper caused itself (closing the dialog), to be swallowed.
  let ignore = 0
  let listening = false

  function listen() {
    if (listening) return
    win.addEventListener('popstate', onPop)
    listening = true
  }

  function unlistenIfIdle() {
    if (!listening || token !== null || ignore > 0) return
    win.removeEventListener('popstate', onPop)
    listening = false
  }

  function push() {
    token = `vault-dialog-${++seq}`
    const state = win.history.state && typeof win.history.state === 'object' ? win.history.state : {}
    win.history.pushState({ ...state, vaultDialog: token }, '')
  }

  function onPop() {
    if (ignore > 0) {
      ignore--
      unlistenIfIdle()
      return
    }
    if (token === null) {
      unlistenIfIdle()
      return
    }
    // Back popped the dialog's entry.
    token = null
    const cb = onBack
    onBack = null
    cb?.()
    unlistenIfIdle()
  }

  return {
    /** Push an entry for a dialog that just opened. Back then calls cb. */
    open(cb) {
      onBack = cb
      if (token !== null) return
      listen()
      push()
    },
    /** After Back, when the dialog has to stay open (unsaved or busy): push the entry again. */
    keepOpen(cb) {
      onBack = cb
      listen()
      if (token === null) push()
    },
    /** The dialog closed by itself: drop its entry so Back isn't swallowed later. */
    close() {
      if (token === null) return
      const ours = win.history.state?.vaultDialog === token
      token = null
      onBack = null
      if (ours) {
        ignore++
        win.history.back()
      } else {
        unlistenIfIdle()
      }
    },
    /** The page is going away: stop listening without touching history. */
    destroy() {
      token = null
      onBack = null
      ignore = 0
      unlistenIfIdle()
    },
    get active() {
      return token !== null
    },
  }
}

/**
 * Hold in-app links that would leave the current page while isDirty() is
 * true. A click on an `<a href="#/other">` is cancelled and onBlocked(hash)
 * is called, so the page can ask first and then navigate itself. Links within
 * the same page (sub-routes such as '#/settings/security') pass through.
 * Returns a cleanup function.
 * @param {string} page e.g. '/settings'
 * @param {() => boolean} isDirtyNow
 * @param {(hash: string) => void} onBlocked
 * @param {Document} [doc]
 */
export function guardLinks(page, isDirtyNow, onBlocked, doc = globalThis.document) {
  function onClick(e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const a = e.target?.closest?.('a[href^="#/"]')
    if (!a) return
    const hash = a.getAttribute('href')
    const path = hash.slice(1).split('?')[0]
    if (path === page || path.startsWith(`${page}/`)) return
    if (!isDirtyNow()) return
    e.preventDefault()
    onBlocked(hash)
  }
  doc.addEventListener('click', onClick, true)
  return () => doc.removeEventListener('click', onClick, true)
}

/**
 * Warn before the tab closes or reloads while isDirty() is true. Returns a
 * cleanup function.
 * @param {() => boolean} isDirtyNow
 * @param {Window} [win]
 */
export function guardUnload(isDirtyNow, win = globalThis.window) {
  function onBeforeUnload(e) {
    if (!isDirtyNow()) return
    e.preventDefault()
    // Older browsers need returnValue set to show the prompt.
    e.returnValue = ''
  }
  win.addEventListener('beforeunload', onBeforeUnload)
  return () => win.removeEventListener('beforeunload', onBeforeUnload)
}

/**
 * Route changes for the hash router, with one optional leave guard (#719).
 *
 * A page with unsaved work registers a guard. Before the route moves to
 * another page, whichever way it moves, the guard is asked and the page stays
 * mounted until it answers:
 * - navigate() (the command palette and other code) waits before touching the
 *   URL;
 * - in-app links are held with guardLinks() and then go through navigate();
 * - Back, Forward and a typed URL have already changed the URL when we hear
 *   about them, so the new location is not committed while the guard is
 *   asked, and a refusal walks history back to the page's own entry.
 *
 * Each entry carries a `vaultIdx` in history.state, so a refusal knows how far
 * to walk (history.go) and the page returns to its own entry: nothing is
 * added to history and Forward stays intact. Moving between sub-routes of the
 * same page (#/settings/general to #/settings/security) never asks, because
 * the page stays mounted and keeps its state.
 *
 * @param {{
 *   win?: Window,
 *   doc?: Document,
 *   current: () => string,
 *   commit: (raw: string) => void,
 *   pageOf: (raw: string) => string,
 * }} opts current() is the committed raw hash ('/settings/security'),
 *   commit(raw) makes a new one current, pageOf(raw) is its page ('/settings').
 */
export function createNavigationGuard({ win = globalThis.window, doc = globalThis.document, current, commit, pageOf }) {
  /** @type {((to: string, from: string) => boolean | Promise<boolean>) | null} */
  let guard = null
  let stopLinks = null
  let asking = false
  let committedIdx = 0

  const readRaw = () => win.location.hash.slice(1) || '/'
  const idxOf = (state) => (state && typeof state === 'object' && typeof state.vaultIdx === 'number' ? state.vaultIdx : null)

  // Give the current entry an index if it has none: a fresh entry sits one
  // past the committed one.
  function stamp(next) {
    const state = win.history.state
    const idx = idxOf(state)
    if (idx !== null) return idx
    win.history.replaceState({ ...(state && typeof state === 'object' ? state : {}), vaultIdx: next }, '')
    return next
  }

  function settle(raw) {
    committedIdx = idxOf(win.history.state) ?? committedIdx
    commit(raw)
  }

  const leavesPage = (raw) => guard !== null && pageOf(raw) !== pageOf(current())

  async function allowed(to) {
    asking = true
    try {
      return !!(await guard?.(to, current()))
    } catch {
      return false
    } finally {
      asking = false
    }
  }

  // popstate and hashchange both land here, often twice for one change.
  async function onLocationChange() {
    stamp(committedIdx + 1)
    const raw = readRaw()
    if (raw === current() || asking) return
    if (!leavesPage(raw)) {
      settle(raw)
      return
    }
    const ok = await allowed(raw)
    const now = readRaw()
    if (now === current()) return
    if (ok) {
      settle(now)
      return
    }
    const idx = idxOf(win.history.state)
    if (idx !== null && idx !== committedIdx) win.history.go(committedIdx - idx)
    else win.history.replaceState(win.history.state, '', '#' + current())
  }

  /**
   * @param {string} path e.g. '/jobs' or '/restore?job=3'
   * @param {{ replace?: boolean }} [opts] replace: swap the current entry
   *   (no Back step); used for sub-route changes, so it never asks.
   */
  async function navigate(path, { replace = false } = {}) {
    const raw = path || '/'
    if (replace) {
      win.history.replaceState(win.history.state, '', '#' + raw)
      commit(raw)
      return
    }
    if (raw === readRaw()) return
    if (leavesPage(raw)) {
      if (asking || !(await allowed(raw))) return
    }
    win.location.hash = '#' + raw
    stamp(committedIdx + 1)
    settle(raw)
  }

  function setLinks() {
    stopLinks?.()
    stopLinks = guard
      ? guardLinks(pageOf(current()), () => guard !== null, (hash) => { navigate(hash.slice(1)) }, doc)
      : null
  }

  return {
    navigate,
    /**
     * Ask fn(to, from) before the route leaves the current page; it returns
     * (or resolves to) true to leave. Returns a function that removes it.
     */
    setGuard(fn) {
      guard = fn
      setLinks()
      return () => {
        if (guard !== fn) return
        guard = null
        setLinks()
      }
    },
    start() {
      committedIdx = stamp(0)
      win.addEventListener('popstate', onLocationChange)
      win.addEventListener('hashchange', onLocationChange)
    },
    stop() {
      win.removeEventListener('popstate', onLocationChange)
      win.removeEventListener('hashchange', onLocationChange)
      guard = null
      setLinks()
    },
  }
}
