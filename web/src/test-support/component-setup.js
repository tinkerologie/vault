// Setup for the jsdom `component` Vitest project (#509). Testing Library's
// auto-cleanup (unmounting rendered components) comes from svelteTesting()
// in vitest.config.js; this file only resets test doubles between tests.
import { afterEach, vi } from 'vitest'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

// jsdom has HTMLDialogElement but neither showModal() nor close(). This models
// the open state and the close event only; tests/dialog-focus.spec.ts covers
// the real top layer, inertness and Escape handling in Chromium.
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function close() {
    if (!this.hasAttribute('open')) return
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
}
