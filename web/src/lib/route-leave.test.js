import { describe, it, expect, vi } from 'vitest'
import { createNavigationGuard } from './unsaved.js'

// A browser-like session history for a hash-routed page. Fragment navigation
// and traversal fire popstate then hashchange asynchronously, like a browser.
function fakeBrowser(startHash = '/settings') {
  const listeners = new Map()
  const entries = [{ hash: startHash, state: null }]
  let index = 0
  const on = (type, fn) => {
    if (!listeners.has(type)) listeners.set(type, new Set())
    listeners.get(type).add(fn)
  }
  const off = (type, fn) => listeners.get(type)?.delete(fn)
  const fire = (type, event = {}) => {
    for (const fn of [...(listeners.get(type) || [])]) fn(event)
  }
  const fireLater = (hashChanged) =>
    queueMicrotask(() => {
      fire('popstate')
      if (hashChanged) fire('hashchange')
    })
  const urlHash = (url) => (url ? String(url).replace(/^#/, '') : null)

  const win = {
    addEventListener: on,
    removeEventListener: off,
    location: {
      get hash() {
        return '#' + entries[index].hash
      },
      set hash(v) {
        const next = String(v).replace(/^#/, '')
        if (next === entries[index].hash) return
        entries.splice(index + 1)
        entries.push({ hash: next, state: null })
        index++
        fireLater(true)
      },
    },
    history: {
      get state() {
        return entries[index].state
      },
      get length() {
        return entries.length
      },
      pushState(state, _t, url) {
        entries.splice(index + 1)
        entries.push({ hash: urlHash(url) ?? entries[index].hash, state })
        index++
      },
      replaceState(state, _t, url) {
        entries[index] = { hash: urlHash(url) ?? entries[index].hash, state }
      },
      go(n) {
        const to = Math.max(0, Math.min(entries.length - 1, index + n))
        if (to === index) return
        const changed = entries[to].hash !== entries[index].hash
        index = to
        fireLater(changed)
      },
      back() {
        this.go(-1)
      },
      forward() {
        this.go(1)
      },
    },
  }
  const doc = {
    addEventListener: on,
    removeEventListener: off,
    // A plain left click on an in-app link; returns whether it was cancelled.
    click(href) {
      let prevented = false
      const a = { getAttribute: () => href }
      fire('click', {
        button: 0,
        defaultPrevented: false,
        target: { closest: () => a },
        preventDefault: () => {
          prevented = true
        },
      })
      return prevented
    },
  }
  return {
    win,
    doc,
    fire,
    get index() {
      return index
    },
    get hashes() {
      return entries.map((e) => e.hash)
    },
  }
}

const pageOf = (raw) => {
  const path = raw.split('?')[0]
  const slash = path.indexOf('/', 1)
  return slash === -1 ? path : path.slice(0, slash)
}

const flush = () => new Promise((r) => setTimeout(r, 0))

function setup(startHash = '/settings') {
  const b = fakeBrowser(startHash)
  let raw = startHash
  const commit = vi.fn((r) => {
    raw = r
  })
  const nav = createNavigationGuard({ win: b.win, doc: b.doc, current: () => raw, commit, pageOf })
  nav.start()
  return { b, nav, commit, current: () => raw }
}

// Walk to #/jobs and back to #/settings so there is history in both directions.
async function withHistory() {
  const t = setup('/jobs')
  t.nav.navigate('/settings')
  await flush()
  expect(t.current()).toBe('/settings')
  return t
}

describe('createNavigationGuard', () => {
  it('commits every route change while no page guards leaving', async () => {
    const { b, nav, current } = setup('/')
    nav.navigate('/jobs')
    await flush()
    expect(current()).toBe('/jobs')
    b.win.history.back()
    await flush()
    expect(current()).toBe('/')
  })

  it('asks before Back leaves the page and stays put when the answer is no', async () => {
    const t = await withHistory()
    const guard = vi.fn(() => false)
    t.nav.setGuard(guard)
    t.commit.mockClear()
    t.b.win.history.back()
    await flush()
    await flush()
    expect(guard).toHaveBeenCalledWith('/jobs', '/settings')
    expect(t.commit).not.toHaveBeenCalled()
    expect(t.current()).toBe('/settings')
    // The URL is back on #/settings in the same entry: nothing added, and
    // Forward history is intact.
    expect(t.b.hashes).toEqual(['/jobs', '/settings'])
    expect(t.b.index).toBe(1)
    expect(t.b.win.location.hash).toBe('#/settings')
  })

  it('leaves on Back once the guard allows it', async () => {
    const t = await withHistory()
    t.nav.setGuard(() => Promise.resolve(true))
    t.b.win.history.back()
    await flush()
    expect(t.current()).toBe('/jobs')
    expect(t.b.index).toBe(0)
  })

  it('restores the page after a cancelled Forward', async () => {
    const t = await withHistory()
    t.b.win.history.back()
    await flush()
    expect(t.current()).toBe('/jobs')
    // Now on Jobs with Settings ahead; a guard registered here refuses Forward.
    t.nav.setGuard(() => false)
    t.b.win.history.forward()
    await flush()
    await flush()
    expect(t.current()).toBe('/jobs')
    expect(t.b.index).toBe(0)
    expect(t.b.hashes).toEqual(['/jobs', '/settings'])
  })

  it('holds navigate() (the command palette) until the guard answers', async () => {
    const { b, nav, current } = setup('/settings')
    let answer
    const guard = vi.fn(() => new Promise((r) => (answer = r)))
    nav.setGuard(guard)
    const done = nav.navigate('/logs')
    expect(b.win.location.hash).toBe('#/settings')
    answer(false)
    await done
    await flush()
    expect(current()).toBe('/settings')
    expect(b.hashes).toEqual(['/settings'])

    const again = nav.navigate('/logs')
    answer(true)
    await again
    await flush()
    expect(current()).toBe('/logs')
    expect(guard).toHaveBeenCalledTimes(2)
  })

  it('does not ask when moving between sub-routes of the same page', async () => {
    const { b, nav, current } = setup('/settings')
    const guard = vi.fn(() => false)
    nav.setGuard(guard)
    nav.navigate('/settings/security')
    await flush()
    expect(current()).toBe('/settings/security')
    b.win.history.back()
    await flush()
    expect(current()).toBe('/settings')
    expect(b.doc.click('#/settings/notifications')).toBe(false)
    expect(guard).not.toHaveBeenCalled()
  })

  it('holds an in-app link to another page and follows it when allowed', async () => {
    const { b, nav, current } = setup('/settings')
    const guard = vi.fn(() => true)
    nav.setGuard(guard)
    expect(b.doc.click('#/history')).toBe(true)
    await flush()
    await flush()
    expect(guard).toHaveBeenCalledWith('/history', '/settings')
    expect(current()).toBe('/history')
  })

  it('stops asking once the guard is removed', async () => {
    const t = await withHistory()
    const guard = vi.fn(() => false)
    const remove = t.nav.setGuard(guard)
    remove()
    expect(t.b.doc.click('#/jobs')).toBe(false)
    t.b.win.history.back()
    await flush()
    expect(guard).not.toHaveBeenCalled()
    expect(t.current()).toBe('/jobs')
  })

  it('replaces the current entry without asking or adding history', async () => {
    const { b, nav, current } = setup('/settings/general')
    nav.setGuard(() => false)
    nav.navigate('/settings/security', { replace: true })
    expect(current()).toBe('/settings/security')
    expect(b.hashes).toEqual(['/settings/security'])
  })

  it('restores the committed page when Back is pressed again during the prompt', async () => {
    const t = setup('/')
    t.nav.navigate('/jobs')
    await flush()
    t.nav.navigate('/settings')
    await flush()
    let answer
    t.nav.setGuard(() => new Promise((r) => (answer = r)))
    t.b.win.history.back()
    await flush()
    t.b.win.history.back()
    await flush()
    expect(t.b.index).toBe(0)
    answer(false)
    await flush()
    await flush()
    expect(t.current()).toBe('/settings')
    expect(t.b.index).toBe(2)
    expect(t.b.hashes).toEqual(['/', '/jobs', '/settings'])
  })

  it('ignores same-URL history entries such as an open dialog being popped', async () => {
    const t = setup('/settings')
    const guard = vi.fn(() => false)
    t.nav.setGuard(guard)
    t.b.win.history.pushState({ ...t.b.win.history.state, vaultDialog: 'x' }, '')
    t.b.win.history.back()
    await flush()
    expect(guard).not.toHaveBeenCalled()
    expect(t.current()).toBe('/settings')
  })

  it('treats a guard that throws as a refusal', async () => {
    const { b, nav, current } = setup('/settings')
    nav.setGuard(() => {
      throw new Error('boom')
    })
    await nav.navigate('/logs')
    expect(current()).toBe('/settings')
    expect(b.win.location.hash).toBe('#/settings')
  })

  it('undoes a refused URL change in place when it stayed in the same entry', async () => {
    const t = setup('/settings')
    t.nav.setGuard(() => false)
    // The URL changed without a new entry (same index), e.g. a replaced hash.
    t.b.win.history.replaceState(t.b.win.history.state, '', '#/logs')
    t.b.fire('hashchange')
    await flush()
    expect(t.current()).toBe('/settings')
    expect(t.b.win.location.hash).toBe('#/settings')
    expect(t.b.hashes).toEqual(['/settings'])
  })

  it('stops listening on stop()', async () => {
    const t = setup('/')
    t.nav.stop()
    t.b.win.location.hash = '#/jobs'
    await flush()
    expect(t.current()).toBe('/')
  })
})
