import { afterEach, describe, expect, it, vi } from 'vitest'

// Tests for the small browser-facing helpers (issue #458): auth stub,
// clipboard, and the hash router. DOM globals are faked per case.

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('auth stub', () => {
  it('reports an always-authenticated session with no client-side key', async () => {
    const auth = await import('./auth.svelte.js')
    expect(auth.getApiKey()).toBe('')
    expect(auth.isAuthenticated()).toBe(true)
    await expect(auth.checkAuthStatus()).resolves.toBeUndefined()
  })
})

describe('copyText', () => {
  function fakeDom({ execResult = true, execThrows = false, previousRange = null } = {}) {
    const textarea = {
      value: '',
      style: {},
      setAttribute: vi.fn(),
      select: vi.fn(),
      setSelectionRange: vi.fn(),
      remove: vi.fn(),
    }
    const selection = {
      rangeCount: previousRange ? 1 : 0,
      getRangeAt: vi.fn(() => previousRange),
      removeAllRanges: vi.fn(),
      addRange: vi.fn(),
    }
    const doc = {
      createElement: vi.fn(() => textarea),
      body: { appendChild: vi.fn() },
      getSelection: vi.fn(() => selection),
      execCommand: vi.fn(() => {
        if (execThrows) throw new Error('unsupported')
        return execResult
      }),
    }
    vi.stubGlobal('document', doc)
    return { doc, textarea, selection }
  }

  async function load() {
    return (await import('./clipboard.js')).copyText
  }

  it('uses the async clipboard API in a secure context', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    vi.stubGlobal('window', { isSecureContext: true })
    const { doc } = fakeDom()
    const copyText = await load()
    await expect(copyText('hi')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('hi')
    expect(doc.execCommand).not.toHaveBeenCalled()
  })

  it('falls back to execCommand when the clipboard API rejects, restoring the selection', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => { throw new Error('denied') }) } })
    vi.stubGlobal('window', { isSecureContext: true })
    const range = { r: 1 }
    const { doc, textarea, selection } = fakeDom({ previousRange: range })
    const copyText = await load()
    await expect(copyText('secret')).resolves.toBe(true)
    expect(textarea.value).toBe('secret')
    expect(textarea.style.position).toBe('fixed')
    expect(doc.execCommand).toHaveBeenCalledWith('copy')
    expect(textarea.remove).toHaveBeenCalled()
    expect(selection.addRange).toHaveBeenCalledWith(range)
  })

  it('uses the legacy path in a non-secure context and reports failure', async () => {
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('window', { isSecureContext: false })
    const { selection } = fakeDom({ execResult: false })
    const copyText = await load()
    await expect(copyText('x')).resolves.toBe(false)
    expect(selection.addRange).not.toHaveBeenCalled()
  })

  it('returns false when execCommand throws', async () => {
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('window', { isSecureContext: false })
    fakeDom({ execThrows: true })
    const copyText = await load()
    await expect(copyText('x')).resolves.toBe(false)
  })

  it('copes with a missing selection object', async () => {
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('window', { isSecureContext: false })
    const { doc } = fakeDom()
    doc.getSelection.mockReturnValue(null)
    const copyText = await load()
    await expect(copyText('x')).resolves.toBe(true)
  })
})

describe('hash router', () => {
  function fakeWindow(hash) {
    const listeners = {}
    const win = {
      location: { hash },
      addEventListener: vi.fn((type, fn) => { listeners[type] = fn }),
      removeEventListener: vi.fn((type, fn) => {
        if (listeners[type] === fn) delete listeners[type]
      }),
      listeners,
    }
    vi.stubGlobal('window', win)
    return win
  }

  it('defaults to / when there is no hash', async () => {
    fakeWindow('')
    const router = await import('./router.svelte.js')
    expect(router.getRoute()).toBe('/')
    expect(router.getRawHash()).toBe('/')
  })

  it('strips query params from the route but keeps them in the raw hash', async () => {
    fakeWindow('#/logs?run=5')
    const router = await import('./router.svelte.js')
    expect(router.getRoute()).toBe('/logs')
    expect(router.getRawHash()).toBe('/logs?run=5')
  })

  it('navigates by setting the hash and tracks hashchange events', async () => {
    const win = fakeWindow('#/')
    const router = await import('./router.svelte.js')
    router.navigate('/jobs')
    expect(win.location.hash).toBe('#/jobs')
    win.listeners.hashchange()
    expect(router.getRoute()).toBe('/jobs')
  })

  it('replaces a previously registered handler on re-evaluation (HMR)', async () => {
    const win = fakeWindow('#/')
    await import('./router.svelte.js')
    const first = win.__vaultRouterHandler
    expect(typeof first).toBe('function')
    vi.resetModules()
    await import('./router.svelte.js')
    expect(win.removeEventListener).toHaveBeenCalledWith('hashchange', first)
    expect(win.__vaultRouterHandler).not.toBe(first)
  })
})
