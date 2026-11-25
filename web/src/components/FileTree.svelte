<script>
  import { tick } from 'svelte'
  import { formatBytes } from '../lib/utils.js'
  import { ICONS } from '../lib/icons.js'

  // The WAI-ARIA tree pattern over a file-tree model (#703): one Tab stop
  // (roving tabindex), arrow/Home/End navigation, aria-level/setsize/posinset,
  // tri-state aria-checked, and windowed rendering so the number of rows in
  // the DOM stays bounded however many folders are open.
  let {
    /** A createFileTreeModel() instance. */
    model,
    /** Bumped by the parent after it changes the model (select all, expand all…). */
    rev = 0,
    /** The applied (debounced) filter text. */
    search = '',
    /** Accessible name of the tree. */
    label = 'Files',
    /** Called after the selection changes. */
    onchange = () => {},
  } = $props()

  const ROW_HEIGHT = 32
  const VIEWPORT = 288
  const OVERSCAN = 8

  let version = $state(0)
  let scrollTop = $state(0)
  let scroller = $state(null)
  let active = $state(-1)

  let rows = $derived.by(() => {
    void version
    void rev
    return model ? model.visibleRows(search) : new Int32Array(0)
  })
  // Row number of each visible node, -1 when hidden.
  let rowOf = $derived.by(() => {
    const pos = new Int32Array(model?.n || 0).fill(-1)
    for (let r = 0; r < rows.length; r++) pos[rows[r]] = r
    return pos
  })
  function rowIndex(i) {
    return i >= 0 && i < rowOf.length && rowOf[i] >= 0 ? rowOf[i] : null
  }

  // A new search starts at the top: the old offset can point past the end of
  // the shorter list, or past a list that was empty and had no scroller.
  $effect(() => {
    void search
    scrollTop = 0
    if (scroller) scroller.scrollTop = 0
  })

  // Keep the roving tab stop on a visible row.
  $effect(() => {
    if (rows.length === 0) active = -1
    else if (rowIndex(active) == null) active = rows[0]
  })

  let windowRows = $derived.by(() => {
    const first = Math.max(0, Math.floor(scrollTop / ROW_HEIGHT) - OVERSCAN)
    const last = Math.min(rows.length, Math.ceil((scrollTop + VIEWPORT) / ROW_HEIGHT) + OVERSCAN)
    const out = []
    for (let r = first; r < last; r++) out.push({ node: rows[r], row: r })
    // The focusable row is always in the DOM, even when scrolled away.
    const activeRow = rowIndex(active)
    if (activeRow != null && (activeRow < first || activeRow >= last)) out.push({ node: active, row: activeRow })
    return out
  })

  function changed() {
    version += 1
  }

  // The model is plain data; passing the version makes each row re-read it.
  function checkStateOf(i, _version, _rev) {
    return model.checkState(i)
  }
  function isOpen(i, _version, _rev) {
    return model.hasChildren(i) && model.isExpanded(i, search)
  }

  function toggleChecked(i) {
    model.toggle(i)
    changed()
    onchange()
  }

  function toggleExpanded(i) {
    model.setExpanded(i, !model.isExpanded(i, search))
    changed()
  }

  async function focusNode(i) {
    if (i == null || i < 0) return
    active = i
    const row = rowIndex(i)
    if (row != null && scroller) {
      const top = row * ROW_HEIGHT
      if (top < scroller.scrollTop) scroller.scrollTop = top
      else if (top + ROW_HEIGHT > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = top + ROW_HEIGHT - scroller.clientHeight
      scrollTop = scroller.scrollTop
    }
    await tick()
    scroller?.querySelector(`[data-node="${i}"]`)?.focus()
  }

  function onKeydown(e) {
    const i = active
    if (i < 0) return
    const row = rowIndex(i) ?? 0
    const expandable = model.hasChildren(i)
    const open = expandable && model.isExpanded(i, search)
    switch (e.key) {
      case 'ArrowDown':
        focusNode(rows[Math.min(rows.length - 1, row + 1)])
        break
      case 'ArrowUp':
        focusNode(rows[Math.max(0, row - 1)])
        break
      case 'Home':
        focusNode(rows[0])
        break
      case 'End':
        focusNode(rows[rows.length - 1])
        break
      case 'ArrowRight':
        if (expandable && !open) toggleExpanded(i)
        else if (open && rows[row + 1] != null && model.parent[rows[row + 1]] === i) focusNode(rows[row + 1])
        break
      case 'ArrowLeft':
        if (open) toggleExpanded(i)
        else if (model.parent[i] >= 0) focusNode(model.parent[i])
        break
      case ' ':
        toggleChecked(i)
        break
      case 'Enter':
        if (expandable) toggleExpanded(i)
        else toggleChecked(i)
        break
      default:
        return
    }
    e.preventDefault()
  }

  function onRowClick(e, i) {
    active = i
    const part = e.target.closest?.('[data-part]')?.dataset.part
    if (part === 'check' || !model.hasChildren(i)) toggleChecked(i)
    else toggleExpanded(i)
  }
</script>

{#if model && model.n > 0}
  {#if rows.length === 0}
    <p class="px-3 py-4 text-xs text-text-dim text-center">No files match "{search}"</p>
  {:else}
    <div
      bind:this={scroller}
      role="tree"
      aria-label={label}
      tabindex="-1"
      onscroll={() => { scrollTop = scroller.scrollTop }}
      onkeydown={onKeydown}
      class="relative max-h-72 overflow-y-auto border border-border rounded-lg bg-surface-3/30 outline-none"
      data-testid="file-tree"
    >
      <div role="none" class="relative" style="height: {rows.length * ROW_HEIGHT}px">
        {#each windowRows as { node: i, row } (i)}
          {@const state = checkStateOf(i, version, rev)}
          {@const expandable = model.hasChildren(i)}
          {@const open = isOpen(i, version, rev)}
          <div
            role="treeitem"
            data-node={i}
            tabindex={i === active ? 0 : -1}
            aria-level={model.depth[i] + 1}
            aria-setsize={model.setsize[i]}
            aria-posinset={model.posinset[i]}
            aria-expanded={expandable ? open : undefined}
            aria-checked={state}
            title={model.path[i]}
            onclick={(e) => onRowClick(e, i)}
            onkeydown={() => {}}
            class="absolute inset-x-0 flex items-center gap-1.5 px-2 text-xs rounded select-none cursor-pointer hover:bg-surface-3/60 focus-visible:bg-surface-3/60"
            style="top: {row * ROW_HEIGHT}px; height: {ROW_HEIGHT}px; padding-left: {model.depth[i] * 16 + 8}px"
          >
            {#if expandable}
              <span data-part="expand" aria-hidden="true" class="w-6 h-6 shrink-0 flex items-center justify-center rounded text-text-dim hover:bg-surface-4 hover:text-text">
                <svg aria-hidden="true" class="w-3.5 h-3.5 transition-transform {open ? 'rotate-90 text-text' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.chevronRight} /></svg>
              </span>
            {:else}
              <span class="w-6 shrink-0" aria-hidden="true"></span>
            {/if}

            <!-- A visual checkbox: the row itself carries aria-checked. -->
            <span data-part="check" aria-hidden="true"
              class="w-6 h-6 shrink-0 flex items-center justify-center">
              <span class="w-4 h-4 rounded border-2 flex items-center justify-center {state === 'false' ? 'border-text-dim bg-surface' : 'border-vault bg-vault text-on-vault'}">
                {#if state === 'true'}
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d={ICONS.check} /></svg>
                {:else if state === 'mixed'}
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-width="3" d="M6 12h12" /></svg>
                {/if}
              </span>
            </span>

            {#if model.isDir[i]}
              <svg aria-hidden="true" class="w-4 h-4 text-text-muted shrink-0" fill="currentColor" viewBox="0 0 20 20"><path d="M2 6a2 2 0 012-2h5l2 2h5a2 2 0 012 2v6a2 2 0 01-2 2H4a2 2 0 01-2-2V6z"/></svg>
            {:else}
              <svg aria-hidden="true" class="w-4 h-4 text-text-muted shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 21h10a2 2 0 002-2V9.414a1 1 0 00-.293-.707l-5.414-5.414A1 1 0 0012.586 3H7a2 2 0 00-2 2v14a2 2 0 002 2z" /></svg>
            {/if}

            <span class="font-mono text-text flex-1 truncate {model.isDir[i] ? 'font-medium' : ''}">{model.name[i]}</span>
            <span class="text-text-dim shrink-0 tabular-nums">
              {#if expandable}
                {model.leafCount(i).toLocaleString()} file{model.leafCount(i) === 1 ? '' : 's'}{#if model.size[i] > 0} · {formatBytes(model.size[i])}{/if}
              {:else}
                {formatBytes(model.size[i])}
              {/if}
            </span>
          </div>
        {/each}
      </div>
    </div>
  {/if}
{/if}
