// consoleport.js — where the Logs console scrolls (#526).
//
// Standalone, the console is its own scroll box. Embedded in Unraid's page,
// the frame sizes to its content and the parent page scrolls instead, so a
// console with its own scroll box would be a nested scroller. There the
// console takes its natural height and this module presents the part of it
// that the user can see as if it were a scroll box: the follow, anchoring and
// render-window logic keeps reading scrollTop, scrollHeight and clientHeight,
// and its writes scroll the parent page.

/**
 * The console's scroll geometry inside the visible part of the frame. All
 * inputs are frame document coordinates, in px.
 *
 * - scrollTop: how far the console's top has passed the top of the visible
 *   part; 0 while its top is on screen or below it.
 * - clientHeight: the visible height from wherever the console starts
 *   showing, so the distance left to the console's end
 *   (scrollHeight - scrollTop - clientHeight) is exact in both cases.
 * - viewTop: where the first visible line of the console sits.
 *
 * @param {{ consoleTop: number, consoleHeight: number, visibleTop: number, visibleHeight: number }} g
 */
export function embeddedGeometry({ consoleTop, consoleHeight, visibleTop, visibleHeight }) {
  const viewTop = Math.max(visibleTop, consoleTop)
  return {
    scrollTop: Math.max(0, visibleTop - consoleTop),
    scrollHeight: consoleHeight,
    clientHeight: Math.max(0, visibleTop + visibleHeight - viewTop),
    viewTop,
  }
}

/**
 * Where a scrollTop write puts the top of the visible part, in frame document
 * coordinates, or null when it would not move: a write never scrolls the page
 * just to bring the console's top up to the top of the screen.
 *
 * @param {{ consoleTop: number, consoleHeight: number, visibleTop: number, visibleHeight: number }} g
 * @param {number} px
 */
export function embeddedScrollTarget(g, px) {
  const max = Math.max(0, g.consoleHeight - g.visibleHeight)
  const target = Math.max(0, Math.min(px, max))
  if (Math.abs(target - embeddedGeometry(g).scrollTop) >= 1) return g.consoleTop + target
  // Already at that offset. A write further down that reaches the console's
  // end must also show the end: a console shorter than the screen can start
  // low enough that its last rows are below it. A write of 0 (follow, filter
  // reset) asks only for the top and never moves an on-screen console.
  const hiddenBelow = g.consoleTop + g.consoleHeight - (g.visibleTop + g.visibleHeight)
  if (px > 0 && px >= g.consoleHeight - g.visibleHeight && hiddenBelow >= 1) return g.visibleTop + hiddenBelow
  return null
}

/**
 * The console's own scroll box.
 * @param {HTMLElement} el
 * @param {Window} [win]
 */
export function elementPort(el, win = window) {
  return {
    get scrollTop() { return el.scrollTop },
    set scrollTop(v) { el.scrollTop = v },
    get scrollHeight() { return el.scrollHeight },
    get clientHeight() { return el.clientHeight },
    measure() { return { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight } },
    viewTop() { return el.getBoundingClientRect().top },
    /**
     * Smooth scroll; `settled` runs once it ends.
     * @param {number} top
     * @param {() => void} [settled]
     */
    smoothScrollTo(top, settled) {
      el.scrollTo({ top, behavior: 'smooth' })
      if (!settled) return
      if ('onscrollend' in win) el.addEventListener('scrollend', settled, { once: true })
      else win.setTimeout(settled, 700)
    },
  }
}

/**
 * The console inside the page-level scroll of an embedded frame.
 * @param {HTMLElement} el the console element, at its natural height
 * @param {{ visibleArea: () => { top: number, height: number }, scrollTo: (top: number) => void }} host
 * @param {Window} [win]
 */
export function embeddedPort(el, host, win = window) {
  const geometry = () => {
    const rect = el.getBoundingClientRect()
    const area = host.visibleArea()
    return {
      consoleTop: rect.top + (win.scrollY || 0),
      consoleHeight: rect.height,
      visibleTop: area.top,
      visibleHeight: area.height,
    }
  }
  const scrollTo = (px) => {
    const target = embeddedScrollTarget(geometry(), px)
    if (target != null) host.scrollTo(target)
  }
  return {
    get scrollTop() { return embeddedGeometry(geometry()).scrollTop },
    set scrollTop(v) { scrollTo(v) },
    get scrollHeight() { return embeddedGeometry(geometry()).scrollHeight },
    get clientHeight() { return embeddedGeometry(geometry()).clientHeight },
    // All three from one measurement of the page, for a caller that needs
    // them together: each getter above measures the page again.
    measure() {
      const { scrollTop, scrollHeight, clientHeight } = embeddedGeometry(geometry())
      return { scrollTop, scrollHeight, clientHeight }
    },
    viewTop() { return embeddedGeometry(geometry()).viewTop - (win.scrollY || 0) },
    // The host scrolls the page instantly, so the scroll has settled as soon
    // as it is asked for.
    smoothScrollTo(top, settled) {
      scrollTo(top)
      settled?.()
    },
  }
}
