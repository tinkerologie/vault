import { describe, expect, it } from 'vitest'
import { pageTitle, ROUTES } from './routes.js'

describe('pageTitle', () => {
  it('gives every dispatched route a unique title ending in Vault', () => {
    const titles = ROUTES.map(pageTitle)
    expect(ROUTES).toHaveLength(11)
    expect(new Set(titles).size).toBe(titles.length)
    for (const title of titles) expect(title).toMatch(/^.+ – Vault$/)
  })

  it('names the page', () => {
    expect(pageTitle('/jobs')).toBe('Backup Jobs – Vault')
    expect(pageTitle('/recovery')).toBe('Disaster Recovery – Vault')
    expect(pageTitle('/recover')).toBe('Recover Vault – Vault')
  })

  it('uses the Dashboard title for routes that render Dashboard', () => {
    expect(pageTitle('/nope')).toBe(pageTitle('/'))
    expect(pageTitle('main-content')).toBe(pageTitle('/'))
    expect(pageTitle('constructor')).toBe(pageTitle('/'))
  })
})
