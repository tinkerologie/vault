// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { createRouteChangeDetector, focusPageHeading } from './focus.js'

describe('focusPageHeading', () => {
  /** @type {HTMLElement} */
  let main
  beforeEach(() => {
    document.body.innerHTML = '<main id="m"></main>'
    main = /** @type {HTMLElement} */ (document.getElementById('m'))
  })

  it('focuses the first h1 and makes it focusable', () => {
    main.innerHTML = '<h1 id="a">Page</h1><h1 id="b">Welcome</h1>'
    focusPageHeading(main)
    expect(document.activeElement?.id).toBe('a')
    expect(document.getElementById('a')?.getAttribute('tabindex')).toBe('-1')
  })

  it('keeps a tabindex the heading already has', () => {
    main.innerHTML = '<h1 id="a" tabindex="0">Page</h1>'
    focusPageHeading(main)
    expect(document.activeElement?.id).toBe('a')
    expect(document.getElementById('a')?.getAttribute('tabindex')).toBe('0')
  })

  it('falls back to the container when there is no h1', () => {
    main.innerHTML = '<p>Loading</p>'
    focusPageHeading(main)
    expect(document.activeElement).toBe(main)
    expect(main.getAttribute('tabindex')).toBe('-1')
  })

  it('returns the container to the top', () => {
    main.innerHTML = '<h1>Page</h1>'
    main.scrollTop = 500
    focusPageHeading(main)
    expect(main.scrollTop).toBe(0)
  })

  it('does nothing without a container', () => {
    expect(() => focusPageHeading(null)).not.toThrow()
  })
})

describe('createRouteChangeDetector', () => {
  it('ignores the first route and repeats, and reports changes', () => {
    const changed = createRouteChangeDetector()
    expect(changed('/jobs')).toBe(false)
    expect(changed('/jobs')).toBe(false)
    expect(changed('/storage')).toBe(true)
    expect(changed('/jobs')).toBe(true)
  })
})
