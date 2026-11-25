import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import FileTree from './FileTree.svelte'
import { createFileTreeModel } from '../lib/file-tree-model.js'

const files = () => createFileTreeModel([
  { path: 'config/a.txt', size: 1 },
  { path: 'config/b.txt', size: 1 },
  { path: 'notes.txt', size: 1 },
])

describe('FileTree (#703)', () => {
  it('is a named tree with one tab stop and tri-state rows', async () => {
    render(FileTree, { model: files(), label: 'Files in plex' })
    const tree = screen.getByRole('tree', { name: 'Files in plex' })
    const items = screen.getAllByRole('treeitem')
    expect(items.map((i) => i.title)).toEqual(['config', 'notes.txt'])
    expect(tree.querySelectorAll('[tabindex="0"]')).toHaveLength(1)
    expect(items[0].getAttribute('aria-checked')).toBe('true')
    expect(items[0].getAttribute('aria-level')).toBe('1')
    expect(items[0].getAttribute('aria-expanded')).toBe('false')
  })

  it('moves, expands and checks with the keyboard', async () => {
    const onchange = vi.fn()
    render(FileTree, { model: files(), onchange })
    const tree = screen.getByRole('tree')
    const config = screen.getByRole('treeitem', { name: /^config / })
    await fireEvent.keyDown(tree, { key: 'ArrowRight' })
    expect(config.getAttribute('aria-expanded')).toBe('true')
    await fireEvent.keyDown(tree, { key: 'ArrowRight' })
    await tick()
    const a = screen.getByRole('treeitem', { name: /^a\.txt / })
    expect(a.getAttribute('tabindex')).toBe('0')
    await fireEvent.keyDown(tree, { key: ' ' })
    expect(a.getAttribute('aria-checked')).toBe('false')
    expect(config.getAttribute('aria-checked')).toBe('mixed')
    expect(onchange).toHaveBeenCalledTimes(1)
    await fireEvent.keyDown(tree, { key: 'Enter' })
    expect(a.getAttribute('aria-checked')).toBe('true')
    await fireEvent.keyDown(tree, { key: 'End' })
    await tick()
    expect(screen.getByRole('treeitem', { name: /^notes\.txt / }).getAttribute('tabindex')).toBe('0')
    await fireEvent.keyDown(tree, { key: 'Home' })
    await fireEvent.keyDown(tree, { key: 'ArrowDown' })
    await tick()
    expect(screen.getByRole('treeitem', { name: /^a\.txt / }).getAttribute('tabindex')).toBe('0')
    await fireEvent.keyDown(tree, { key: 'ArrowUp' })
    await fireEvent.keyDown(tree, { key: 'ArrowLeft' })
    expect(config.getAttribute('aria-expanded')).toBe('false')
    await fireEvent.keyDown(tree, { key: 'x' })
  })

  it('clicks a folder to expand and its check box to select', async () => {
    render(FileTree, { model: files() })
    const config = screen.getByRole('treeitem', { name: /^config / })
    await fireEvent.click(config)
    expect(config.getAttribute('aria-expanded')).toBe('true')
    await fireEvent.click(config.querySelector('[data-part="check"]'))
    expect(config.getAttribute('aria-checked')).toBe('false')
    expect(config.getAttribute('aria-expanded')).toBe('true')
    await fireEvent.click(screen.getByRole('treeitem', { name: /^notes\.txt / }))
    expect(screen.getByRole('treeitem', { name: /^notes\.txt / }).getAttribute('aria-checked')).toBe('false')
  })

  it('filters and says when nothing matches', async () => {
    const { rerender } = render(FileTree, { model: files(), search: 'b.txt' })
    expect(screen.getAllByRole('treeitem').map((i) => i.title)).toEqual(['config', 'config/b.txt'])
    await rerender({ search: 'zzz' })
    expect(screen.getByText('No files match "zzz"')).toBeTruthy()
  })
})
