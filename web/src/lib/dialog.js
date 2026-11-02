// The one implementation of modal behaviour (#528). Modal.svelte is the only
// caller of modalDialog(); everything else composes Modal. The native top layer
// gives inertness and topmost-only Escape, and this adds what it does not:
// a deterministic focus target on open, Tab wrapping, and focus restoration when
// the dialog is removed from the DOM (native restoration only runs on close()).
import { tick } from 'svelte'

const TABBABLE = 'a[href], button, input, select, textarea, summary, [tabindex]'
const OPENER_WATCH_MS = 10000

/** Open dialogs, oldest first. @type {HTMLDialogElement[]} */
const stack = []
/** Nodes that must stay usable above a modal, mapped to where they belong otherwise. @type {Map<Element, Comment>} */
const floating = new Map()
/** Safari does not focus a button on click, so remember the last one pressed. @type {Element | null} */
let lastPressed = null

if (typeof document !== 'undefined') {
  document.addEventListener('pointerdown', (e) => { lastPressed = e.target.closest?.(TABBABLE) ?? null }, true)
}

function topDialog() {
  return stack[stack.length - 1] ?? null
}

/** @param {Element} el */
function isRendered(el) {
  if (el.closest('[hidden], [inert]')) return false
  return typeof el.checkVisibility === 'function' ? el.checkVisibility({ visibilityProperty: true }) : true
}

/** Matches the browser's own Tab order: one stop per radio group, on its checked radio. @param {Element} el */
function isSkippedRadio(el) {
  if (el.type !== 'radio' || el.checked || !el.name) return false
  const scope = el.form ?? el.ownerDocument
  return [...scope.querySelectorAll('input[type="radio"]')].some((r) => r.name === el.name && r.checked)
}

/** @param {Element} el */
export function isTabbable(el) {
  return el.isConnected && !el.disabled && el.getAttribute('tabindex') !== '-1' && !isSkippedRadio(el) && isRendered(el)
}

/** Tabbable controls owned by a dialog, excluding those in a dialog nested inside it. @param {Element} root */
export function tabbablesIn(root) {
  return [...root.querySelectorAll(TABBABLE)].filter((el) => isTabbable(el) && el.closest('dialog') === root)
}

/** @param {HTMLDialogElement} dialog */
function initialTarget(dialog) {
  const marked = [...dialog.querySelectorAll('[data-autofocus]')].find((el) => !el.disabled && isRendered(el))
  return marked ?? tabbablesIn(dialog)[0] ?? dialog
}

/** @param {Element | null | undefined} el @param {FocusOptions} [options] */
function tryFocus(el, options) {
  if (!el?.isConnected || el.disabled || !isRendered(el)) return false
  el.focus(options)
  return document.activeElement === el
}

function focusMain() {
  return tryFocus(document.getElementById('main-content'), { preventScroll: true })
}

function currentOpener() {
  const active = document.activeElement
  return active && active !== document.body ? active : lastPressed
}

// A confirmed delete closes first and removes the opener's row only when the
// reload finishes, so a restored opener can still vanish a moment later.
/** @param {Element} opener */
function watchOpener(opener) {
  const observer = new MutationObserver(() => {
    if (document.activeElement !== opener && opener.isConnected) stop()
    else if (!opener.isConnected) {
      stop()
      if (document.activeElement === document.body && !topDialog()) focusMain()
    }
  })
  const timer = setTimeout(() => stop(), OPENER_WATCH_MS)
  function stop() {
    clearTimeout(timer)
    observer.disconnect()
  }
  observer.observe(document.body, { childList: true, subtree: true })
}

/** @param {Element | null} opener @param {HTMLDialogElement | null} topAtClose @param {Element | null} focusAtClose */
async function restoreFocus(opener, topAtClose, focusAtClose) {
  await tick()
  const top = topDialog()
  if (top !== topAtClose) return // a newer dialog opened and already owns focus
  // Focus moved on purpose since the dialog closed, such as a route change focusing
  // its heading. The dialog underneath and <main> hold focus only as fallbacks.
  const active = document.activeElement
  const fallbacks = [focusAtClose, document.body, top, document.getElementById('main-content')]
  if (!fallbacks.includes(active)) return
  if (tryFocus(opener)) {
    watchOpener(opener)
    return
  }
  if (top) tryFocus(initialTarget(top))
  else focusMain()
}

function syncFloating() {
  const top = topDialog()
  for (const [node, anchor] of floating) {
    if (top) {
      if (node.parentNode !== top) top.append(node)
    } else if (anchor.parentNode && node.previousSibling !== anchor) {
      anchor.after(node)
    }
  }
}

/**
 * Svelte action for content that must stay usable above a modal, such as toasts.
 * Everything outside the topmost modal is inert and painted below the top layer,
 * whatever its z-index, so the node has to live inside that dialog.
 * @param {Element} node
 */
export function floatAboveDialogs(node) {
  const anchor = document.createComment('')
  node.before(anchor)
  floating.set(node, anchor)
  syncFloating()
  return {
    destroy() {
      floating.delete(node)
      anchor.remove()
      node.remove()
    },
  }
}

/**
 * Svelte action that makes a freshly mounted <dialog> modal. Unmounting closes it.
 * @param {HTMLDialogElement} dialog
 * @param {() => void} requestClose
 */
export function modalDialog(dialog, requestClose) {
  const opener = currentOpener()

  function onCancel(e) {
    e.preventDefault()
    requestClose()
  }

  // The browser has already closed it here, so an owner that declines (a dialog
  // locked while its request runs) needs it shown again.
  function onClose() {
    requestClose()
    tick().then(() => {
      if (dialog.isConnected && !dialog.open) dialog.showModal()
    })
  }

  function onKeydown(e) {
    if (e.key !== 'Tab' || e.defaultPrevented || e.target.closest('dialog') !== dialog) return
    const items = tabbablesIn(dialog)
    if (items.length === 0) {
      e.preventDefault()
      return
    }
    const first = items[0]
    const last = items[items.length - 1]
    const active = document.activeElement
    const precedes = (a, b) => a === b || !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
    if (e.shiftKey && (active === dialog || precedes(active, first))) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && precedes(last, active)) {
      e.preventDefault()
      first.focus()
    }
  }

  // Re-rendered content (a directory listing, a dismissed toast) can remove the
  // focused element, and Shift+Tab from <body> would leave the dialog.
  const contentObserver = new MutationObserver(() => {
    if (topDialog() === dialog && document.activeElement === document.body) dialog.focus()
  })

  dialog.tabIndex = -1
  dialog.addEventListener('cancel', onCancel)
  // Chromium skips a cancelable `cancel` on repeated Escape and closes directly.
  dialog.addEventListener('close', onClose)
  dialog.addEventListener('keydown', onKeydown)
  if (!dialog.open) dialog.showModal()
  stack.push(dialog)
  syncFloating()
  initialTarget(dialog).focus()
  contentObserver.observe(dialog, { childList: true, subtree: true })

  return {
    destroy() {
      contentObserver.disconnect()
      dialog.removeEventListener('cancel', onCancel)
      dialog.removeEventListener('close', onClose)
      dialog.removeEventListener('keydown', onKeydown)
      stack.splice(stack.indexOf(dialog), 1)
      syncFloating()
      if (dialog.open) dialog.close()
      restoreFocus(opener, topDialog(), document.activeElement)
    },
  }
}

/**
 * Svelte action that closes a dialog when its backdrop is clicked. The dialog fills
 * the viewport, so a click that targets the element itself landed outside the panel.
 * @param {HTMLDialogElement} dialog
 * @param {(() => void) | null} requestClose Null leaves the backdrop inactive.
 */
export function closeOnBackdropClick(dialog, requestClose) {
  let handler = requestClose
  /** @param {MouseEvent} e */
  function onClick(e) {
    if (e.target === dialog) handler?.()
  }
  dialog.addEventListener('click', onClick)
  return {
    /** @param {(() => void) | null} next */
    update(next) { handler = next },
    destroy() { dialog.removeEventListener('click', onClick) },
  }
}

/**
 * Tracks a wizard's step and moves focus to its [data-step-heading] when the step
 * changes. The first value only records the starting point, so a wizard that is
 * merely appearing never steals focus.
 */
export function createStepFocus() {
  let last
  /** @param {unknown} step @param {() => Element | null | undefined} getRoot */
  function track(step, getRoot) {
    const changed = last !== undefined && last !== step
    last = step
    if (changed) tick().then(() => getRoot()?.querySelector('[data-step-heading]')?.focus())
  }
  track.reset = () => { last = undefined }
  return track
}
