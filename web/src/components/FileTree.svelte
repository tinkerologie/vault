<script>
  import { SvelteMap } from 'svelte/reactivity'
  import { formatBytes } from '../lib/utils.js'

  let {
    nodes = [],
    selected,
    expandedPaths,
    search = '',
    ontoggle = () => {},
    ontoggleexpand = () => {},
  } = $props()

  // Precompute search match results in O(N) single-pass traversal per query change
  let matchMap = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return null
    const map = new SvelteMap()

    function computeMatch(node) {
      const selfMatch = node.name.toLowerCase().includes(q) || node.path.toLowerCase().includes(q)
      let childMatch = false
      if (node.isDir && node.children) {
        for (const c of node.children) {
          if (computeMatch(c)) {
            childMatch = true
          }
        }
      }
      const matches = selfMatch || childMatch
      map.set(node.path, { matches, childMatch })
      return matches
    }

    for (const node of nodes) {
      computeMatch(node)
    }
    return map
  })

  function matchesSearch(node) {
    if (!matchMap) return true
    return matchMap.get(node.path)?.matches ?? false
  }

  function getFolderState(node) {
    if (!node.isDir) {
      const isChecked = selected.has(node.path)
      return { checked: isChecked, indeterminate: false }
    }
    const total = node.descendantLeafCount || 0
    if (total === 0) {
      const isChecked = selected.has(node.path)
      return { checked: isChecked, indeterminate: false }
    }
    let count = 0
    for (const p of node.descendantLeafPaths) {
      if (selected.has(p)) count++
    }
    if (count === total) {
      return { checked: true, indeterminate: false }
    }
    if (count === 0) {
      return { checked: false, indeterminate: false }
    }
    return { checked: false, indeterminate: true }
  }

  function isExpanded(node) {
    if (matchMap) {
      // In search mode, auto-expand if any descendant matches search
      return expandedPaths.has(node.path) || (matchMap.get(node.path)?.childMatch ?? false)
    }
    return expandedPaths.has(node.path)
  }

  function handleRowClick(e, node) {
    if (node.isDir) {
      ontoggleexpand(node.path)
    } else {
      ontoggle(node)
    }
  }

  function handleCheckboxChange(e, node) {
    e.stopPropagation()
    ontoggle(node)
  }
</script>

{#snippet renderNode(node, depth)}
  {#if matchesSearch(node)}
    {@const state = getFolderState(node)}
    {@const expanded = isExpanded(node)}
    <div
      class="group flex items-center gap-1.5 py-1 px-2 text-xs hover:bg-surface-3/60 rounded select-none cursor-pointer transition-colors"
      style="padding-left: {depth * 16 + 8}px"
      onclick={(e) => handleRowClick(e, node)}
      role="treeitem"
      tabindex="0"
      aria-expanded={node.isDir ? expanded : undefined}
      aria-selected={state.checked}
      title={node.path}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          handleRowClick(e, node)
        }
      }}
    >
      {#if node.isDir}
        <button
          type="button"
          class="p-0.5 hover:bg-surface-4 rounded text-text-dim hover:text-text shrink-0 focus:outline-none"
          onclick={(e) => { e.stopPropagation(); ontoggleexpand(node.path) }}
          aria-label={expanded ? `Collapse ${node.name}` : `Expand ${node.name}`}
        >
          <svg
            class="w-3.5 h-3.5 transition-transform {expanded ? 'rotate-90 text-text' : 'text-text-dim'}"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
          </svg>
        </button>
      {:else}
        <span class="w-4.5 shrink-0"></span>
      {/if}

      <input
        type="checkbox"
        checked={state.checked}
        indeterminate={state.indeterminate}
        onchange={(e) => handleCheckboxChange(e, node)}
        onclick={(e) => e.stopPropagation()}
        class="accent-vault rounded cursor-pointer shrink-0"
        aria-label={`Select ${node.name}`}
      />

      {#if node.isDir}
        {#if expanded}
          <svg class="w-4 h-4 text-amber-400 shrink-0" fill="currentColor" viewBox="0 0 20 20">
            <path fill-rule="evenodd" d="M2 6a2 2 0 012-2h4l2 2h4a2 2 0 012 2v1H8a3 3 0 00-3 3v1.5a1.5 1.5 0 01-3-1.5V6z" clip-rule="evenodd"/>
            <path d="M6 12a2 2 0 012-2h8a2 2 0 012 2v2a2 2 0 01-2 2H8a2 2 0 01-2-2v-2z"/>
          </svg>
        {:else}
          <svg class="w-4 h-4 text-amber-400 shrink-0" fill="currentColor" viewBox="0 0 20 20">
            <path d="M2 6a2 2 0 012-2h5l2 2h5a2 2 0 012 2v6a2 2 0 01-2 2H4a2 2 0 01-2-2V6z"/>
          </svg>
        {/if}
      {:else}
        <svg class="w-4 h-4 text-text-muted shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 21h10a2 2 0 002-2V9.414a1 1 0 00-.293-.707l-5.414-5.414A1 1 0 0012.586 3H7a2 2 0 00-2 2v14a2 2 0 002 2z" />
        </svg>
      {/if}

      <span class="font-mono text-text flex-1 truncate {node.isDir ? 'font-medium' : ''}">{node.name}</span>

      {#if node.isDir}
        <span class="text-text-dim text-[11px] shrink-0">
          {node.descendantLeafCount} file{node.descendantLeafCount === 1 ? '' : 's'}
          {#if node.size > 0}
            · {formatBytes(node.size)}
          {/if}
        </span>
      {:else}
        <span class="text-text-dim text-[11px] shrink-0 font-mono">
          {formatBytes(node.size)}
        </span>
      {/if}
    </div>

    {#if node.isDir && expanded && node.children && node.children.length > 0}
      {#each node.children as child (child.path)}
        {@render renderNode(child, depth + 1)}
      {/each}
    {/if}
  {/if}
{/snippet}

{#if nodes && nodes.length > 0}
  {@const hasMatches = !matchMap || nodes.some(n => matchesSearch(n))}
  {#if hasMatches}
    <div class="py-1" role="tree">
      {#each nodes as node (node.path)}
        {@render renderNode(node, 0)}
      {/each}
    </div>
  {:else}
    <p class="px-3 py-4 text-xs text-text-dim text-center">No files match "{search}"</p>
  {/if}
{/if}
