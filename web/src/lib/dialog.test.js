import { describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { createStepFocus } from './dialog.js'

function root() {
  const heading = { focus: vi.fn() }
  return { heading, getRoot: () => ({ querySelector: (sel) => (sel === '[data-step-heading]' ? heading : null) }) }
}

describe('createStepFocus', () => {
  it('does not move focus for the first value', async () => {
    const { heading, getRoot } = root()
    createStepFocus()(1, getRoot)
    await tick()
    expect(heading.focus).not.toHaveBeenCalled()
  })

  it('moves focus on each change, forward and back, but not on a repeat', async () => {
    const { heading, getRoot } = root()
    const track = createStepFocus()
    track(1, getRoot)
    track(2, getRoot)
    await tick()
    expect(heading.focus).toHaveBeenCalledTimes(1)
    track(2, getRoot)
    await tick()
    expect(heading.focus).toHaveBeenCalledTimes(1)
    track(1, getRoot)
    await tick()
    expect(heading.focus).toHaveBeenCalledTimes(2)
  })

  it('treats the next value after a reset as a new start', async () => {
    const { heading, getRoot } = root()
    const track = createStepFocus()
    track(3, getRoot)
    track.reset()
    track(1, getRoot)
    await tick()
    expect(heading.focus).not.toHaveBeenCalled()
  })

  it('tolerates a missing root or heading', async () => {
    const track = createStepFocus()
    track(1, () => null)
    track(2, () => null)
    await expect(tick()).resolves.toBeUndefined()
  })
})
