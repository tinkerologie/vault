// The tooltip trigger's name (#608): each default trigger names what it
// explains, so a page of them doesn't read as "More info" over and over.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { createRawSnippet, tick } from 'svelte'
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

// Tooltip behaviour (#691): described before it opens, click opens rather
// than toggles, and the pointer can move onto the bubble.
describe('Tooltip behaviour', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  const trigger = () => screen.getByRole('button', { name: 'More info: Max retries' })
  const tip = () => screen.queryByRole('tooltip')

  it('describes the trigger with its text before any hover or focus', () => {
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    const id = trigger().getAttribute('aria-describedby')
    expect(id).toBeTruthy()
    expect(document.getElementById(id).textContent).toBe('How many times to retry.')
    expect(tip()).toBeNull()
  })

  it('keeps the description out of the trigger name and the page text', () => {
    const { container } = render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    const desc = document.getElementById(trigger().getAttribute('aria-describedby'))
    expect(trigger().contains(desc)).toBe(false)
    expect(desc.hidden).toBe(true)
    expect(container.contains(desc)).toBe(true)
  })

  it('opens on click, and a second click leaves it open', async () => {
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.click(trigger())
    expect(tip().textContent).toContain('How many times to retry.')
    await fireEvent.click(trigger())
    expect(tip()).not.toBeNull()
  })

  it('stays open after hover then click, when the pointer leaves', async () => {
    vi.useFakeTimers()
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.mouseEnter(trigger())
    vi.advanceTimersByTime(250)
    await tick()
    expect(tip()).not.toBeNull()
    await fireEvent.click(trigger())
    await fireEvent.mouseLeave(trigger())
    vi.advanceTimersByTime(1000)
    await tick()
    expect(tip()).not.toBeNull()
  })

  it('lets the pointer move from the trigger onto the bubble', async () => {
    vi.useFakeTimers()
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.mouseEnter(trigger())
    vi.advanceTimersByTime(250)
    await tick()
    await fireEvent.mouseLeave(trigger())
    await fireEvent.mouseEnter(tip())
    vi.advanceTimersByTime(1000)
    await tick()
    expect(tip()).not.toBeNull()
    await fireEvent.mouseLeave(tip())
    vi.advanceTimersByTime(1000)
    await tick()
    expect(tip()).toBeNull()
  })

  it('hides after the pointer leaves the trigger for good', async () => {
    vi.useFakeTimers()
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.mouseEnter(trigger())
    vi.advanceTimersByTime(250)
    await tick()
    await fireEvent.mouseLeave(trigger())
    vi.advanceTimersByTime(1000)
    await tick()
    expect(tip()).toBeNull()
  })

  it('closes on Escape', async () => {
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.click(trigger())
    await fireEvent.keyDown(document, { key: 'Escape' })
    await tick()
    expect(tip()).toBeNull()
  })

  it('closes on a click outside', async () => {
    render(Tooltip, { text: 'How many times to retry.', label: 'Max retries' })
    await fireEvent.click(trigger())
    await fireEvent.click(document.body)
    await tick()
    expect(tip()).toBeNull()
  })
})
