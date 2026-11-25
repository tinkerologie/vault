// Loading indicators (#698): the section spinner announces itself, and the
// inline one stays out of the accessible name of the button it sits in.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import Spinner from './Spinner.svelte'
import InlineSpinner from './InlineSpinner.svelte'

describe('Spinner', () => {
  it('is a status region that reads its text', () => {
    render(Spinner, { text: 'Connecting...' })
    expect(screen.getByRole('status').textContent.trim()).toBe('Connecting...')
  })

  it('hides the ring itself', () => {
    const { container } = render(Spinner)
    expect(container.querySelector('.animate-spin').getAttribute('aria-hidden')).toBe('true')
    expect(screen.getByRole('status').textContent.trim()).toBe('Loading...')
  })
})

describe('InlineSpinner', () => {
  it('is decorative, with no role or name of its own', () => {
    const { container } = render(InlineSpinner)
    const ring = container.querySelector('.animate-spin')
    expect(ring.getAttribute('aria-hidden')).toBe('true')
    expect(ring.hasAttribute('role')).toBe(false)
    expect(ring.hasAttribute('aria-label')).toBe(false)
  })

  it('adds nothing to the name of the button it sits in', () => {
    const button = document.createElement('button')
    document.body.append(button)
    render(InlineSpinner, { target: button })
    button.append(' Checking…')
    expect(screen.getByRole('button', { name: 'Checking…' })).toBe(button)
    button.remove()
  })
})
