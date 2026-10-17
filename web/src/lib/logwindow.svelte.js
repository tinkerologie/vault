// logwindow.svelte.js — the Logs console's render window (#507).
//
// The console used to put every loaded log line on the page; with the full
// history that was ~8,600 rows and ~78,000 DOM nodes, and leaving the Logs
// page (or anything else that walks the document, such as Chrome's
// accessibility tree) stalled the UI. This module keeps only a window of
// `rows` on the page and moves it as the reader scrolls, compensating
// scrollTop whenever rows above the viewport are added or removed so the
// line being read stays put. The anchor arithmetic lives in logrows.js; this
// module owns the state and the scroll-box choreography, against an injected
// box and tick so it is unit-tested without a DOM.

import {
  RENDER_WINDOW_BASE,
  RENDER_WINDOW_MAX,
  renderedCount,
  extendWindow,
  renderedStart,
  raiseWindow,
  oldestWindow,
  trimTop,
  trimBottom,
  windowSlice,
} from './logrows.js'

// What a scroll event should do, from where the viewport is and what the
// window hides. Pure, so every combination is tested directly.
//
// - At the top with newer rows hidden above: grow the window upwards
//   (`raise`); nothing else happens on that event.
// - At the top otherwise: the live edge (`enterLive` when not following).
// - Away from the top while following: `leaveLive`.
// - Near the oldest edge: render loaded rows first (`grow`), and only once
//   every loaded row is on the page fetch further back (`loadOlder`).
export function scrollAction({ atTop, nearOldest, follow, hiddenAbove, hiddenBelow, hasMore, loadingOlder }) {
  if (atTop && hiddenAbove) return { raise: true }
  const action = {}
  if (atTop && !follow) action.enterLive = true
  else if (!atTop && follow) action.leaveLive = true
  if (nearOldest) {
    if (hiddenBelow) action.grow = true
    else if (hasMore && !loadingOlder) action.loadOlder = true
  }
  return action
}

// rows:  () => the console's full row list (logrows.buildRows output)
// box:   () => the scroll container element, or null before mount
// tick:  () => Promise that resolves once the DOM reflects state changes
export function createLogWindow({ rows, box, tick }) {
  let bottom = $state(null) // bottom edge anchor { key, count } (logrows.js)
  let top = $state(null) // top edge anchor { key, index }; null at the live edge
  let moving = $state(false) // a grow/raise is mid-flight; ignore re-entry
  let suppress = false // one-shot: swallow the scroll event of our own write

  // Where the bottom anchor alone puts the window's end.
  const anchoredEnd = () => renderedCount(rows(), bottom)

  // A top anchor on the first ENTRY row at or after index i. Anchoring on a
  // date header would not hold: today's header stays at index 0 while new
  // lines for today are inserted below it, inside the window.
  const topAt = (all, i) => {
    let j = i
    while (j < all.length && all[j].kind !== 'entry') j++
    return j < all.length ? { key: all[j].key, index: j } : null
  }
  // Where the window starts. A top anchor whose row is still above the
  // bottom edge is used exactly. When a collapse toggle re-keys rows, the
  // anchor's fallback can cross the bottom edge; the window then keeps at
  // least RENDER_WINDOW_BASE rows instead of going empty.
  const start = () => {
    if (!top) return 0
    const all = rows()
    const c = anchoredEnd()
    const idx = all.findIndex((r) => r.key === top.key)
    if (idx >= 0 && idx < c) return idx
    return Math.min(renderedStart(all, top), Math.max(0, c - RENDER_WINDOW_BASE))
  }

  // Where the window ends: the bottom anchor, but never more than
  // RENDER_WINDOW_MAX rows past the start. Both anchors can stay valid while
  // the rows between them multiply (turning Collapse off or Details on
  // expands every folded group), so the cap is enforced here for every
  // reader, not only when the window grows or rises.
  // grow() and raise() lift the cap while they move the window: they render
  // the new rows, measure, then trim back under the cap with scroll
  // compensation, which needs the overflow to be on the page in between.
  const count = () => (moving ? anchoredEnd() : Math.min(anchoredEnd(), start() + RENDER_WINDOW_MAX))

  // Pull a bottom anchor that sits past the capped end back to that end.
  // grow() and raise() call this before lifting the cap, so a window whose
  // anchors drifted apart (rows multiplied between them) moves on from what
  // is actually on the page instead of rendering everything in between.
  const settleBottom = () => {
    const c = count()
    if (anchoredEnd() > c) bottom = extendWindow(rows(), c, 0)
  }

  // A programmatic scrollTop write whose own scroll event must be ignored.
  // Browsers fire no event when the position does not change, so the
  // suppress flag is armed only for a write that actually moves; otherwise it
  // would swallow the reader's next real scroll.
  function write(px) {
    const el = box()
    if (!el) return
    const target = Math.max(0, Math.min(px, el.scrollHeight - el.clientHeight))
    if (Math.abs(target - el.scrollTop) < 1) return
    suppress = true
    el.scrollTop = target
  }

  return {
    // The rows to render.
    get rows() {
      return windowSlice(rows(), start(), count())
    },
    // Older rows are loaded but not rendered.
    get hiddenBelow() {
      return count() < rows().length
    },
    // Newer entries are loaded but not rendered. A date header above the
    // window is not hidden content: windowSlice shows it as context.
    get hiddenAbove() {
      const all = rows()
      const s = start()
      for (let i = 0; i < s; i++) if (all[i].kind === 'entry') return true
      return false
    },

    write,

    // True (and cleared) when the current scroll event is our own write's.
    consumeSuppressed() {
      if (!suppress) return false
      suppress = false
      return true
    },

    // Back to the default window: the newest rows.
    reset() {
      bottom = null
      top = null
    },

    // Leaving the live edge: pin both edges to the rows rendered now, so
    // lines arriving at the top stay hidden above (the caller's new-entry
    // count reports them) instead of pushing the rows being read out of the
    // window or growing it past the cap.
    pin() {
      const all = rows()
      const s = start()
      const c = count()
      if (c - s <= 0) return
      if (!bottom) bottom = extendWindow(all, c, 0)
      if (!top) top = topAt(all, s)
    },

    // Render the next slice of older rows below the viewport. Content added
    // below needs no compensation, but rows dropped from the top to keep the
    // window bounded do: scrollTop moves up by exactly their height.
    async grow() {
      const el = box()
      if (!el || moving) return
      settleBottom()
      moving = true
      try {
        bottom = extendWindow(rows(), count())
        await tick()
        // The raw anchored end, not the capped one: the cap is what this
        // slide restores by moving the top edge down.
        const trimmed = trimTop(rows(), top, start(), anchoredEnd())
        if (trimmed !== top) {
          const prevHeight = el.scrollHeight
          const prevTop = el.scrollTop
          top = trimmed
          await tick()
          write(prevTop - (prevHeight - el.scrollHeight))
        }
      } finally {
        moving = false
      }
    },

    // Render the next slice of newer rows ABOVE the viewport, moving
    // scrollTop down by exactly the height they add. Reaching the newest row
    // pins the top edge to it rather than clearing it: the compensation
    // leaves the reader just short of the live edge, and an unpinned top
    // would let every new line render above them, past the cap. Rows past the
    // cap are dropped from the bottom, below the viewport.
    async raise() {
      const el = box()
      if (!el || moving) return
      settleBottom()
      moving = true
      try {
        const all = rows()
        const prevHeight = el.scrollHeight
        const prevTop = el.scrollTop
        top = raiseWindow(all, start()) ?? topAt(all, 0)
        await tick()
        write(prevTop + (el.scrollHeight - prevHeight))
        bottom = trimBottom(rows(), bottom, start(), anchoredEnd())
      } finally {
        moving = false
      }
    },

    // "Jump to oldest": render only the oldest rows and put the viewport at
    // the bottom. Returns false when nothing older is hidden, leaving the
    // caller to scroll there itself.
    async jumpToOldest() {
      const el = box()
      if (!el || count() >= rows().length) return false
      const w = oldestWindow(rows())
      top = w.top
      bottom = w.bottom
      await tick()
      write(el.scrollHeight)
      return true
    },

    // An older batch was fetched while the reader sat at the bottom: it lands
    // beyond the window, so grow it now rather than on the next scroll.
    async afterOlderFetch(nearOldest) {
      if (nearOldest && count() < rows().length) await this.grow()
    },
  }
}
