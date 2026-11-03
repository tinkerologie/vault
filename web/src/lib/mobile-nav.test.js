import { describe, expect, it } from 'vitest'
import { groupBySection, isMobileNavActive, isMoreActive, moreNavItems } from './mobile-nav.js'

const nav = [
  { path: '/', label: 'Dashboard' },
  { path: '/jobs', label: 'Jobs', section: 'Protect' },
  { path: '/storage', label: 'Storage', section: 'Protect' },
  { path: '/recovery', label: 'Disaster Recovery', section: 'Recover' },
  { path: '/logs', label: 'Logs', section: 'System' },
  { path: '/docs-external', label: 'Documentation', section: 'System', external: true },
]

describe('moreNavItems', () => {
  it('returns what the bar leaves out, in inventory order', () => {
    expect(moreNavItems(nav, ['/', '/jobs']).map((i) => i.path)).toEqual(['/storage', '/recovery', '/logs', '/docs-external'])
  })

  it('never invents a page the filtered inventory hid', () => {
    const replica = nav.filter((i) => i.path !== '/recovery')
    expect(moreNavItems(replica, ['/', '/logs']).map((i) => i.path)).toEqual(['/jobs', '/storage', '/docs-external'])
  })
})

describe('isMobileNavActive', () => {
  it('matches Home only on the exact route', () => {
    expect(isMobileNavActive('/', '/')).toBe(true)
    expect(isMobileNavActive('/', '/jobs')).toBe(false)
  })

  it('treats the recover wizard as part of Recovery', () => {
    expect(isMobileNavActive('/recovery', '/recover')).toBe(true)
    expect(isMobileNavActive('/recovery', '/recovery')).toBe(true)
    expect(isMobileNavActive('/recovery', '/jobs')).toBe(false)
  })
})

describe('isMoreActive', () => {
  const items = moreNavItems(nav, ['/', '/jobs'])

  it('is true for pages in the sheet, including the recover wizard', () => {
    expect(isMoreActive(items, '/storage')).toBe(true)
    expect(isMoreActive(items, '/recover')).toBe(true)
  })

  it('is false for bar pages, and an external entry is never current', () => {
    expect(isMoreActive(items, '/')).toBe(false)
    expect(isMoreActive(items, '/jobs')).toBe(false)
    expect(isMoreActive(items, '/docs-external')).toBe(false)
  })
})

describe('groupBySection', () => {
  it('keeps the order and leaves no empty section', () => {
    expect(groupBySection(moreNavItems(nav, ['/', '/jobs', '/recovery'])).map((g) => [g.section, g.items.length])).toEqual([
      ['Protect', 1],
      ['System', 2],
    ])
  })

  it('leaves entries without a section under a null header', () => {
    expect(groupBySection(nav.slice(0, 1))).toEqual([{ section: null, items: [nav[0]] }])
  })
})
