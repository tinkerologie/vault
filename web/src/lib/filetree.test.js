import { describe, it, expect } from 'vitest'
import { splitPath, normalizePath, buildFileTree } from './utils.js'

describe('splitPath', () => {
  it('returns empty array for empty, null, or non-string input', () => {
    expect(splitPath('')).toEqual([])
    expect(splitPath(null)).toEqual([])
    expect(splitPath(undefined)).toEqual([])
    expect(splitPath(123)).toEqual([])
  })

  it('splits path by slash and removes empty segments', () => {
    expect(splitPath('a/b/c')).toEqual(['a', 'b', 'c'])
    expect(splitPath('/var/log/app/')).toEqual(['var', 'log', 'app'])
    expect(splitPath('foo//bar///baz')).toEqual(['foo', 'bar', 'baz'])
  })

  it('normalizes Windows backslashes', () => {
    expect(splitPath('a\\b\\c')).toEqual(['a', 'b', 'c'])
    expect(splitPath('\\var\\log\\app\\')).toEqual(['var', 'log', 'app'])
  })
})

describe('normalizePath', () => {
  it('strips leading and trailing slashes and backslashes', () => {
    expect(normalizePath('/etc/config/')).toBe('etc/config')
    expect(normalizePath('\\etc\\config\\')).toBe('etc/config')
    expect(normalizePath('///a///b///c///')).toBe('a/b/c')
    expect(normalizePath('foo/bar')).toBe('foo/bar')
    expect(normalizePath('')).toBe('')
    expect(normalizePath(null)).toBe('')
    expect(normalizePath(undefined)).toBe('')
  })
})

describe('buildFileTree', () => {
  it('returns empty array for empty or non-array input', () => {
    expect(buildFileTree([])).toEqual([])
    expect(buildFileTree(null)).toEqual([])
    expect(buildFileTree(undefined)).toEqual([])
  })

  it('builds a flat list of top-level files correctly', () => {
    const files = [
      { path: 'zebra.txt', size: 100 },
      { path: 'apple.txt', size: 200 },
    ]
    const tree = buildFileTree(files)
    expect(tree).toHaveLength(2)
    // Sorted alphabetically
    expect(tree[0].name).toBe('apple.txt')
    expect(tree[0].path).toBe('apple.txt')
    expect(tree[0].isDir).toBe(false)
    expect(tree[0].size).toBe(200)
    expect(tree[0].descendantLeafCount).toBe(1)
    expect(tree[0].descendantLeafPaths).toEqual(['apple.txt'])

    expect(tree[1].name).toBe('zebra.txt')
    expect(tree[1].path).toBe('zebra.txt')
    expect(tree[1].isDir).toBe(false)
  })

  it('builds nested hierarchy and sorts directories before files', () => {
    const files = [
      { path: 'root.txt', size: 10 },
      { path: 'data/config/settings.json', size: 100 },
      { path: 'data/config/app.conf', size: 50 },
      { path: 'data/media/photo.jpg', size: 500 },
      { path: 'data/readme.txt', size: 20 },
    ]
    const tree = buildFileTree(files)
    expect(tree).toHaveLength(2)

    // Directory 'data' comes before file 'root.txt'
    expect(tree[0].name).toBe('data')
    expect(tree[0].isDir).toBe(true)
    expect(tree[0].size).toBe(670) // 100 + 50 + 500 + 20
    expect(tree[0].descendantLeafCount).toBe(4)
    expect(tree[0].descendantLeafPaths).toEqual([
      'data/config/app.conf',
      'data/config/settings.json',
      'data/media/photo.jpg',
      'data/readme.txt',
    ])

    expect(tree[1].name).toBe('root.txt')
    expect(tree[1].isDir).toBe(false)
    expect(tree[1].size).toBe(10)

    // Inside 'data': directories 'config' and 'media' come before 'readme.txt'
    const dataChildren = tree[0].children
    expect(dataChildren).toHaveLength(3)
    expect(dataChildren[0].name).toBe('config')
    expect(dataChildren[0].isDir).toBe(true)
    expect(dataChildren[0].size).toBe(150)
    expect(dataChildren[0].descendantLeafCount).toBe(2)

    expect(dataChildren[1].name).toBe('media')
    expect(dataChildren[1].isDir).toBe(true)
    expect(dataChildren[1].size).toBe(500)

    expect(dataChildren[2].name).toBe('readme.txt')
    expect(dataChildren[2].isDir).toBe(false)
    expect(dataChildren[2].size).toBe(20)

    // Inside 'config': 'app.conf' comes before 'settings.json'
    const configChildren = dataChildren[0].children
    expect(configChildren).toHaveLength(2)
    expect(configChildren[0].name).toBe('app.conf')
    expect(configChildren[1].name).toBe('settings.json')
  })

  it('handles explicit directory entries and empty directories', () => {
    const files = [
      { path: 'empty_dir', is_dir: true, mode: '0755' },
      { path: 'parent/empty_subdir', is_dir: true },
      { path: 'parent/file.txt', size: 42 },
    ]
    const tree = buildFileTree(files)
    expect(tree).toHaveLength(2)

    // Both are directories, sorted alphabetically: empty_dir, parent
    expect(tree[0].name).toBe('empty_dir')
    expect(tree[0].isDir).toBe(true)
    expect(tree[0].children).toHaveLength(0)
    expect(tree[0].descendantLeafCount).toBe(1)
    expect(tree[0].descendantLeafPaths).toEqual(['empty_dir'])

    expect(tree[1].name).toBe('parent')
    expect(tree[1].isDir).toBe(true)
    expect(tree[1].children).toHaveLength(2)
    // Directory empty_subdir before file.txt
    expect(tree[1].children[0].name).toBe('empty_subdir')
    expect(tree[1].children[0].isDir).toBe(true)
    expect(tree[1].children[1].name).toBe('file.txt')
    expect(tree[1].children[1].isDir).toBe(false)
    expect(tree[1].descendantLeafCount).toBe(2)
  })

  it('handles paths with leading and trailing slashes correctly', () => {
    const files = [
      { path: '/app/config/', is_dir: true },
      { path: '/app/config/db.json', size: 123 },
    ]
    const tree = buildFileTree(files)
    expect(tree).toHaveLength(1)
    expect(tree[0].name).toBe('app')
    expect(tree[0].path).toBe('app')
    expect(tree[0].isDir).toBe(true)

    const appChildren = tree[0].children
    expect(appChildren).toHaveLength(1)
    expect(appChildren[0].name).toBe('config')
    expect(appChildren[0].path).toBe('app/config')

    const configChildren = appChildren[0].children
    expect(configChildren).toHaveLength(1)
    expect(configChildren[0].name).toBe('db.json')
    expect(configChildren[0].path).toBe('app/config/db.json')
  })
})
