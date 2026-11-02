// 3-2-1 header (#529): the goal and dismiss buttons are siblings of the expand
// toggle, so activating them can't also toggle, and nothing nests inside a control.
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { tick } from 'svelte'
import ComplianceBadge from './ComplianceBadge.svelte'

const props = () => ({
  storage: [{ id: 1, type: 'local' }],
  jobs: [{ id: 1, enabled: true, storage_dest_id: 1 }],
  ondismiss: vi.fn(),
  onGoalChange: vi.fn(),
})

const toggle = () => screen.getByRole('button', { name: /3-2-1 Backup Rule/, expanded: false })

describe('ComplianceBadge header', () => {
  it('toggles the details from a native button with aria-expanded', async () => {
    render(ComplianceBadge, props())
    const button = toggle()
    expect(button.tagName).toBe('BUTTON')
    await fireEvent.click(button)
    await tick()
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText(/^Goal:/)).toBeTruthy()
  })

  it('keeps the title in a heading', () => {
    render(ComplianceBadge, props())
    expect(screen.getByRole('heading', { name: /3-2-1 Backup Rule/ })).toBeTruthy()
  })

  it('nests no control inside the toggle', () => {
    render(ComplianceBadge, props())
    expect(toggle().querySelector('button, a, input, select, [role="button"]')).toBeNull()
  })

  it('choosing a goal changes the goal without toggling', async () => {
    const p = props()
    render(ComplianceBadge, p)
    const button = toggle()
    await fireEvent.click(screen.getByRole('button', { name: 'Full 3-2-1', hidden: true }))
    expect(p.onGoalChange).toHaveBeenCalledWith('full')
    expect(button.getAttribute('aria-expanded')).toBe('false')
  })

  it('dismissing does not toggle', async () => {
    const p = props()
    render(ComplianceBadge, p)
    const button = toggle()
    await fireEvent.click(screen.getByRole('button', { name: 'Hide 3-2-1 Backup Rule' }))
    expect(p.ondismiss).toHaveBeenCalledOnce()
    expect(button.getAttribute('aria-expanded')).toBe('false')
  })

  it('omits goal and dismiss controls without their callbacks', () => {
    render(ComplianceBadge, { storage: [], jobs: [] })
    expect(screen.queryByRole('group', { name: 'Backup goal', hidden: true })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Hide 3-2-1 Backup Rule' })).toBeNull()
  })
})
