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
