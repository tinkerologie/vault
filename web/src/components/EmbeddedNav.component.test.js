import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import EmbeddedNav from './EmbeddedNav.svelte'

const items = [
  { path: '/', label: 'Dashboard' },
  { path: '/jobs', label: 'Jobs' },
  { path: '/docs-external', label: 'Documentation', external: true, href: 'https://example.test/docs' },
]

describe('EmbeddedNav', () => {
  it('links every item as a real hash link and marks only the active one', () => {
    render(EmbeddedNav, { items, route: '/jobs' })
    const jobs = screen.getByRole('link', { name: 'Jobs' })
    expect(jobs.getAttribute('href')).toBe('#/jobs')
    expect(jobs.getAttribute('aria-current')).toBe('page')
    expect(screen.getByRole('link', { name: 'Dashboard' }).getAttribute('aria-current')).toBeNull()
  })

  it('opens an external item in a new tab and says so', () => {
    render(EmbeddedNav, { items, route: '/' })
    const docs = screen.getByRole('link', { name: 'Documentation (opens in a new tab)' })
    expect(docs.getAttribute('href')).toBe('https://example.test/docs')
    expect(docs.getAttribute('target')).toBe('_blank')
    expect(docs.getAttribute('aria-current')).toBeNull()
  })

  it('renders only what it is given, so mode filtering carries through', () => {
    render(EmbeddedNav, { items: items.slice(0, 1), route: '/' })
    expect(screen.getAllByRole('link')).toHaveLength(1)
  })
})
