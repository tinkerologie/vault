import { describe, expect, it } from 'vitest'
import { createFileTreeModel } from './file-tree-model.js'

const FILES = [
  { path: '/config/settings.xml', size: 10 },
  { path: '/config/db/main.db', size: 100 },
  { path: '/config/db/wal.db', size: 5 },
  { path: '/logs/', is_dir: true },
  { path: 'readme.txt', size: 1 },
]

const names = (m, rows) => Array.from(rows, (i) => m.name[i])
const find = (m, path) => m.path.indexOf(path)

describe('createFileTreeModel', () => {
  it('orders folders before files and records depth, size and counts', () => {
    const m = createFileTreeModel(FILES)
    expect(names(m, m.visibleRows())).toEqual(['config', 'logs', 'readme.txt'])
    const config = find(m, 'config')
    expect(m.leafCount(config)).toBe(3)
    expect(m.size[config]).toBe(115)
    expect(m.depth[find(m, 'config/db/main.db')]).toBe(2)
    // An empty folder is its own restorable leaf, as before.
    expect(m.totalLeaves).toBe(5)
    expect(m.setsize[config]).toBe(3)
    expect(m.posinset[find(m, 'readme.txt')]).toBe(3)
  })

  it('starts with every file selected and reports tri-state checks', () => {
    const m = createFileTreeModel(FILES)
    const config = find(m, 'config')
    expect(m.selectedCount()).toBe(5)
    expect(m.checkState(config)).toBe('true')
    m.toggle(find(m, 'config/db/wal.db'))
    expect(m.checkState(find(m, 'config/db'))).toBe('mixed')
    expect(m.checkState(config)).toBe('mixed')
    expect(m.selectedCount()).toBe(4)
    m.toggle(config) // mixed → all
    expect(m.checkState(config)).toBe('true')
    m.toggle(config) // all → none
    expect(m.checkState(config)).toBe('false')
    expect(m.selectedCount()).toBe(2)
    expect(m.selectedPaths().sort()).toEqual(['logs', 'readme.txt'])
  })

  it('shows children of expanded folders only', () => {
    const m = createFileTreeModel(FILES)
    m.setExpanded(find(m, 'config'), true)
    expect(names(m, m.visibleRows())).toEqual(['config', 'db', 'settings.xml', 'logs', 'readme.txt'])
    m.expandAll()
    expect(m.visibleRows()).toHaveLength(7)
    m.collapseAll()
    expect(m.visibleRows()).toHaveLength(3)
  })

  it('filters by name or path and opens folders holding a match', () => {
    const m = createFileTreeModel(FILES)
    expect(names(m, m.visibleRows('WAL'))).toEqual(['config', 'db', 'wal.db'])
    expect(m.visibleRows('nothing-matches')).toHaveLength(0)
  })

  it('selects or clears only what the filter matches', () => {
    const m = createFileTreeModel(FILES)
    m.setAll(false)
    m.setMatching('db', true)
    expect(m.selectedPaths().sort()).toEqual(['config/db/main.db', 'config/db/wal.db'])
    m.setMatching('main', false)
    expect(m.selectedPaths()).toEqual(['config/db/wal.db'])
    m.setAll(true)
    expect(m.selectedCount()).toBe(5)
  })

  it('finds parents for keyboard navigation', () => {
    const m = createFileTreeModel(FILES)
    expect(m.parent[find(m, 'config/db')]).toBe(find(m, 'config'))
    expect(m.parent[find(m, 'config')]).toBe(-1)
    expect(m.isDir[find(m, 'logs')]).toBe(1)
  })

  it('ignores blank entries and treats a path used as a folder as one', () => {
    const m = createFileTreeModel([null, { path: '' }, { path: '///' }, { path: 'a', size: 3 }, { path: 'a/b.txt', size: 1 }])
    const a = find(m, 'a')
    expect(m.isDir[a]).toBe(1)
    expect(m.hasChildren(a)).toBe(true)
    expect(m.totalLeaves).toBe(1)
  })

  it('reports expansion, opening folders that hold a filter match', () => {
    const m = createFileTreeModel(FILES)
    const config = find(m, 'config')
    expect(m.isExpanded(config)).toBe(false)
    expect(m.isExpanded(config, 'wal')).toBe(true)
    m.setExpanded(find(m, 'readme.txt'), true) // a file never expands
    expect(m.isExpanded(find(m, 'readme.txt'))).toBe(false)
    expect(m.hasChildren(find(m, 'logs'))).toBe(false)
  })

  it('ignores out-of-range toggles and treats an empty filter as all', () => {
    const m = createFileTreeModel(FILES)
    m.toggle(-1)
    m.toggle(99)
    expect(m.selectedCount()).toBe(5)
    m.setMatching('  ', false)
    expect(m.selectedCount()).toBe(0)
  })

  it('handles an empty listing', () => {
    const m = createFileTreeModel([])
    expect(m.totalLeaves).toBe(0)
    expect(m.visibleRows()).toHaveLength(0)
    expect(createFileTreeModel(null).n).toBe(0)
  })

  it('toggles and filters 100k files quickly', () => {
    const files = []
    for (let i = 0; i < 100000; i++) files.push({ path: `appdata/d${i % 50}/s${i % 7}/file-${i}.dat`, size: 1 })
    const m = createFileTreeModel(files)
    let t = performance.now()
    m.toggle(find(m, 'appdata'))
    m.toggle(find(m, 'appdata/d3/s2/file-1053.dat') >= 0 ? find(m, 'appdata/d3/s2/file-1053.dat') : 5)
    expect(performance.now() - t).toBeLessThan(100)
    t = performance.now()
    m.visibleRows('file-9999')
    expect(performance.now() - t).toBeLessThan(150)
  })
})
