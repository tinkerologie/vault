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
  let moving = false // a grow/raise is mid-flight; ignore re-entry
  let suppress = false // one-shot: swallow the scroll event of our own write

  const count = () => renderedCount(rows(), bottom)
  // The top edge never starts within RENDER_WINDOW_BASE rows of the bottom
  // edge: when a collapse toggle re-keys rows, the two anchors' fallbacks can
  // otherwise cross and leave an empty window.
  const start = () => Math.min(renderedStart(rows(), top), Math.max(0, count() - RENDER_WINDOW_BASE))

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
    // Newer rows are loaded but not rendered.
    get hiddenAbove() {
      return start() > 0
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
      if (!top) top = { key: all[s].key, index: s }
    },

    // Render the next slice of older rows below the viewport. Content added
    // below needs no compensation, but rows dropped from the top to keep the
    // window bounded do: scrollTop moves up by exactly their height.
    async grow() {
      const el = box()
      if (!el || moving) return
      moving = true
      try {
        bottom = extendWindow(rows(), count())
        await tick()
        const trimmed = trimTop(rows(), top, start(), count())
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
      moving = true
      try {
        const all = rows()
        const prevHeight = el.scrollHeight
        const prevTop = el.scrollTop
        top = raiseWindow(all, start()) ?? (all.length ? { key: all[0].key, index: 0 } : null)
        await tick()
        write(prevTop + (el.scrollHeight - prevHeight))
        bottom = trimBottom(rows(), bottom, start(), count())
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
