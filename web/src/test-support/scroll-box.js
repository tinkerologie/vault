// jsdom has no layout: scrollHeight/clientHeight are always 0 and setting
// scrollTop does nothing. The Logs console's behaviour hangs entirely on that
// geometry, so the component tests (#509) install controllable getters for
// the console element (role="log") only. Every other element keeps jsdom's
// behaviour.
//
// The content height follows the rendered rows (rowHeight px per
// [data-entry-id] row), so adding entries grows the console the way it would
// in a browser. A test can also force a height with `setScrollHeight`.
import { tick } from 'svelte'
import { fireEvent } from '@testing-library/svelte'

const PROPS = ['scrollHeight', 'clientHeight', 'scrollTop']

/**
 * Install the console geometry stubs and return their controls: `writes`,
 * `scrollToCalls`, `setScrollHeight`, `position` and `restore`.
 */
export function installScrollBox({ clientHeight = 400, rowHeight = 20 } = {}) {
  const proto = HTMLElement.prototype
  const saved = {
    scrollHeight: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollHeight'),
    clientHeight: Object.getOwnPropertyDescriptor(Element.prototype, 'clientHeight'),
    scrollTop: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop'),
    scrollTo: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTo'),
  }
  const state = new WeakMap()
  const geo = {
    clientHeight,
    rowHeight,
    forcedHeight: null,
    writes: [], // every scrollTop assignment, in order
    scrollToCalls: [],
  }
  const isLog = (el) => el?.getAttribute?.('role') === 'log'
  const contentHeight = (el) =>
    geo.forcedHeight ?? Math.max(geo.clientHeight, el.querySelectorAll('[data-entry-id]').length * geo.rowHeight)
  const clamp = (el, v) => Math.max(0, Math.min(v, contentHeight(el) - geo.clientHeight))
  const original = (name, el) => saved[name]?.get?.call(el) ?? 0

  Object.defineProperty(proto, 'scrollHeight', {
    configurable: true,
    get() { return isLog(this) ? contentHeight(this) : original('scrollHeight', this) },
  })
  Object.defineProperty(proto, 'clientHeight', {
    configurable: true,
    get() { return isLog(this) ? geo.clientHeight : original('clientHeight', this) },
  })
  Object.defineProperty(proto, 'scrollTop', {
    configurable: true,
    get() { return isLog(this) ? (state.get(this) ?? 0) : original('scrollTop', this) },
    set(v) {
      if (!isLog(this)) {
        saved.scrollTop?.set?.call(this, v)
        return
      }
      geo.writes.push(v)
      state.set(this, clamp(this, v))
    },
  })
  Object.defineProperty(proto, 'scrollTo', {
    configurable: true,
    writable: true,
    value(opts) {
      if (!isLog(this)) return saved.scrollTo?.value?.call(this, opts)
      geo.scrollToCalls.push(opts)
    },
  })

  geo.setScrollHeight = (h) => { geo.forcedHeight = h }
  /** Scroll position without recording a programmatic write. */
  geo.position = (el, v) => state.set(el, clamp(el, v))
  geo.restore = () => {
    for (const name of PROPS) delete proto[name]
    delete proto.scrollTo
  }
  return geo
}

/** Let Svelte flush effects and any chained promises settle. */
export async function flush(rounds = 4) {
  for (let i = 0; i < rounds; i++) {
    await tick()
    await Promise.resolve()
    await new Promise((r) => setTimeout(r, 0))
  }
}

/**
 * The reader scrolls the console to `top`: move it there, then fire the
 * scroll event the browser would fire.
 */
export async function userScroll(geo, el, top) {
  geo.position(el, top)
  await fireEvent.scroll(el)
  await flush()
}

/** Offset that leaves the viewport `fromBottom` px above the content's end. */
export function offsetFromBottom(geo, el, fromBottom) {
  return el.scrollHeight - el.clientHeight - fromBottom
}

/** Minimal api.js mock for pages that only render (Logs uses purgeActivity). */
export function apiModuleMock(overrides = {}) {
  return {
    api: { purgeActivity: async () => ({}), ...overrides },
    isReplicaMode: () => false,
  }
}
