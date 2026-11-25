/**
 * A flat, preorder model of a restore point's file listing for the partial
 * restore picker (#703).
 *
 * Nodes are stored in depth-first order in parallel arrays, so a folder's
 * subtree is the contiguous range [i, end[i]) and its leaves are the contiguous
 * leaf range [leafStart[i], leafEnd[i]). Selection is one byte per leaf, and
 * each node keeps a count of its selected leaves that is updated along the
 * ancestor chain on every toggle. A toggle therefore costs the size of the
 * toggled subtree plus its depth, never a recount of the whole tree, and the
 * visible rows are produced by skipping collapsed subtrees in one pass.
 *
 * Leaves follow the old buildFileTree rules: every file, plus any folder with
 * no children (it restores as itself). Paths are normalised without leading
 * or trailing slashes, the same strings the restore request sends.
 */

const collator = new Intl.Collator(undefined, { sensitivity: 'base' })

function splitSegments(path) {
  return String(path).replace(/\\/g, '/').split('/').filter(Boolean)
}

/**
 * @param {Array<{ path: string, size?: number, is_dir?: boolean }> | null | undefined} files
 */
export function createFileTreeModel(files) {
  // 1. Build a nested map of the listing.
  const root = { children: new Map(), isDir: true, size: 0, name: '', path: '' }
  for (const f of Array.isArray(files) ? files : []) {
    if (!f || !f.path) continue
    const segments = splitSegments(f.path)
    if (segments.length === 0) continue
    const explicitDir = Boolean(f.is_dir) || String(f.path).endsWith('/')
    let node = root
    let path = ''
    for (let s = 0; s < segments.length; s++) {
      const seg = segments[s]
      path = path ? `${path}/${seg}` : seg
      const last = s === segments.length - 1
      let child = node.children.get(seg)
      if (!child) {
        const dir = !last || explicitDir
        child = { name: seg, path, isDir: dir, size: dir ? 0 : Number(f.size) || 0, children: dir ? new Map() : null }
        node.children.set(seg, child)
      } else if (!last && !child.isDir) {
        // A path used both as a file and a folder: treat it as a folder.
        child.isDir = true
        child.children = new Map()
        child.size = 0
      }
      node = child
    }
  }

  // 2. Flatten depth-first with folders before files, both by name.
  const order = (a, b) => (a.isDir !== b.isDir ? (a.isDir ? -1 : 1) : collator.compare(a.name, b.name))
  const flat = []
  const stack = []
  const rootKids = Array.from(root.children.values()).sort(order)
  for (let k = rootKids.length - 1; k >= 0; k--) stack.push({ node: rootKids[k], depth: 0, parent: -1, pos: k + 1, setsize: rootKids.length })
  while (stack.length) {
    const entry = stack.pop()
    entry.index = flat.length
    flat.push(entry)
    if (entry.node.isDir) {
      const kids = Array.from(entry.node.children.values()).sort(order)
      for (let k = kids.length - 1; k >= 0; k--) stack.push({ node: kids[k], depth: entry.depth + 1, parent: entry.index, pos: k + 1, setsize: kids.length })
    }
  }

  const n = flat.length
  const name = new Array(n)
  const path = new Array(n)
  const lowerPath = new Array(n)
  const isDir = new Uint8Array(n)
  const size = new Float64Array(n)
  const depth = new Uint16Array(n)
  const parent = new Int32Array(n)
  const end = new Int32Array(n)
  const posinset = new Int32Array(n)
  const setsize = new Int32Array(n)
  const leafStart = new Int32Array(n)
  const leafEnd = new Int32Array(n)
  const leafNode = []
  for (let i = 0; i < n; i++) {
    const { node, depth: d, parent: p, pos, setsize: ss } = flat[i]
    name[i] = node.name
    path[i] = node.path
    lowerPath[i] = node.path.toLowerCase()
    isDir[i] = node.isDir ? 1 : 0
    size[i] = node.isDir ? 0 : node.size
    depth[i] = d
    parent[i] = p
    posinset[i] = pos
    setsize[i] = ss
    leafStart[i] = leafNode.length
    // A file or an empty folder is a leaf.
    if (!node.isDir || node.children.size === 0) leafNode.push(i)
  }
  const totalLeaves = leafNode.length
  // Subtree ends and folder sizes in reverse preorder; a subtree's leaves end
  // where the next node after the subtree starts counting.
  for (let i = n - 1; i >= 0; i--) {
    if (end[i] === 0) end[i] = i + 1
    const p = parent[i]
    if (p >= 0) {
      if (end[i] > end[p]) end[p] = end[i]
      size[p] += size[i]
    }
  }
  for (let i = 0; i < n; i++) leafEnd[i] = end[i] < n ? leafStart[end[i]] : totalLeaves
  // A file, or a folder with nothing below it.
  function isLeafNode(i) {
    return !isDir[i] || end[i] === i + 1
  }

  const selected = new Uint8Array(totalLeaves).fill(1)
  const selCount = new Int32Array(n)
  const expanded = new Set()
  let selectedTotal = totalLeaves

  const leafCount = (i) => leafEnd[i] - leafStart[i]

  function recount() {
    selCount.fill(0)
    for (let i = n - 1; i >= 0; i--) {
      if (isLeafNode(i)) selCount[i] += selected[leafStart[i]]
      const p = parent[i]
      if (p >= 0) selCount[p] += selCount[i]
    }
    selectedTotal = 0
    for (let l = 0; l < totalLeaves; l++) selectedTotal += selected[l]
  }
  recount()

  function addToAncestors(i, delta) {
    for (let p = parent[i]; p >= 0; p = parent[p]) selCount[p] += delta
    selectedTotal += delta
  }

  // Sets every leaf below (and including) node i, keeping counts exact.
  function setSubtree(i, value) {
    const before = selCount[i]
    const v = value ? 1 : 0
    selected.fill(v, leafStart[i], leafEnd[i])
    for (let j = i; j < end[i]; j++) selCount[j] = v ? leafCount(j) : 0
    addToAncestors(i, selCount[i] - before)
  }

  let matchQuery = null
  let selfMatch = null
  let anyMatch = null
  let descMatch = null
  function computeMatches(query) {
    const q = (query || '').trim().toLowerCase()
    if (q === matchQuery) return q
    matchQuery = q
    if (!q) {
      selfMatch = anyMatch = descMatch = null
      return q
    }
    selfMatch = new Uint8Array(n)
    anyMatch = new Uint8Array(n)
    descMatch = new Uint8Array(n)
    for (let i = 0; i < n; i++) selfMatch[i] = lowerPath[i].includes(q) ? 1 : 0
    for (let i = n - 1; i >= 0; i--) {
      if (selfMatch[i]) anyMatch[i] = 1
      const p = parent[i]
      if (p >= 0 && anyMatch[i]) {
        anyMatch[p] = 1
        descMatch[p] = 1
      }
    }
    return q
  }

  return {
    n,
    name,
    path,
    isDir,
    size,
    depth,
    parent,
    end,
    posinset,
    setsize,
    totalLeaves,
    leafCount,
    selectedCount: () => selectedTotal,
    /** 'true' | 'false' | 'mixed', for aria-checked. */
    checkState(i) {
      const c = selCount[i]
      if (c === 0) return 'false'
      return c === leafCount(i) ? 'true' : 'mixed'
    },
    /** A file flips; a folder selects all below it unless all are already selected. */
    toggle(i) {
      if (i < 0 || i >= n) return
      setSubtree(i, selCount[i] !== leafCount(i))
    },
    setAll(value) {
      selected.fill(value ? 1 : 0)
      recount()
    },
    /** Select or clear what the filter matches: a matching folder takes its whole subtree. */
    setMatching(query, value) {
      const q = computeMatches(query)
      if (!q) {
        this.setAll(value)
        return
      }
      const v = value ? 1 : 0
      for (let i = 0; i < n;) {
        if (selfMatch[i]) {
          selected.fill(v, leafStart[i], leafEnd[i])
          i = end[i]
        } else {
          i++
        }
      }
      recount()
    },
    selectedPaths() {
      const out = []
      for (let l = 0; l < totalLeaves; l++) if (selected[l]) out.push(path[leafNode[l]])
      return out
    },
    isExpanded(i, query = '') {
      const q = computeMatches(query)
      return expanded.has(i) || Boolean(q && descMatch[i])
    },
    setExpanded(i, open) {
      if (!isDir[i]) return
      if (open) expanded.add(i)
      else expanded.delete(i)
    },
    expandAll() {
      for (let i = 0; i < n; i++) if (isDir[i] && !isLeafNode(i)) expanded.add(i)
    },
    collapseAll() {
      expanded.clear()
    },
    hasChildren: (i) => Boolean(isDir[i]) && !isLeafNode(i),
    /**
     * The node indexes to render, in order: collapsed subtrees are skipped,
     * and with a filter only matching nodes and their ancestors remain.
     * @returns {Int32Array}
     */
    visibleRows(query = '') {
      const q = computeMatches(query)
      const rows = new Int32Array(n)
      let count = 0
      for (let i = 0; i < n;) {
        if (q && !anyMatch[i]) {
          i = end[i]
          continue
        }
        rows[count++] = i
        if (isDir[i] && (expanded.has(i) || (q && descMatch[i]))) i++
        else i = end[i]
      }
      return rows.subarray(0, count)
    },
  }
}
