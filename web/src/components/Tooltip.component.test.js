// The tooltip trigger's name (#608): each default trigger names what it
// explains, so a page of them doesn't read as "More info" over and over.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { createRawSnippet } from 'svelte'
import Tooltip from './Tooltip.svelte'

describe('Tooltip trigger name', () => {
  it('names the default trigger after its label', () => {
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    expect(screen.getByRole('button', { name: 'More info: Max retries' })).toBeTruthy()
  })

  it('falls back to "More info" without a label', () => {
    render(Tooltip, { text: 'How many times to retry.' })
    expect(screen.getByRole('button', { name: 'More info' })).toBeTruthy()
  })

  it('lets custom trigger content name itself', () => {
    const children = createRawSnippet(() => ({ render: () => '<span>2 warnings</span>' }))
    render(Tooltip, { text: '2 warning anomalies', label: 'ignored', children })
    expect(screen.getByRole('button', { name: '2 warnings' })).toBeTruthy()
  })
})
