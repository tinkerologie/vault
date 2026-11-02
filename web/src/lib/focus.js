/**
 * Moves focus to the first h1 in a page container, or to the container if the
 * page has none, and returns to the top so the heading is on screen. A heading
 * focused by script is what a screen reader announces after a client-side route
 * change, which the browser doesn't do by itself.
 * @param {HTMLElement | null | undefined} container
 */
export function focusPageHeading(container) {
  if (!container) return
  const target = container.querySelector('h1') ?? container
  if (!target.hasAttribute('tabindex')) target.setAttribute('tabindex', '-1')
  container.scrollTop = 0
  target.focus({ preventScroll: true })
}

/**
 * Returns a function reporting whether its route differs from the previous call.
 * The first call only records the starting point, so initial entry isn't a change.
 */
export function createRouteChangeDetector() {
  /** @type {string | undefined} */
  let last
  /** @param {string} route */
  return (route) => {
    const changed = last !== undefined && last !== route
    last = route
    return changed
  }
}
