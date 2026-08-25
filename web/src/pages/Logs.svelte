<script>
  import { onMount, tick } from 'svelte'
  import { api, isReplicaMode } from '../lib/api.js'
  import { formatDate } from '../lib/utils.js'
  import { copyText } from '../lib/clipboard.js'
  import { createFollowMarker, countNewEntries } from '../lib/followmarker.js'
  import { anchoredScrollTop } from '../lib/scrollanchor.js'
  import { nearBottom, nearOldestEdge, atTop as isAtTop } from '../lib/scrollflags.js'
  import { getLiveMode } from '../lib/runtime-config.js'
  import { rowTime } from '../lib/tsformat.js'
  import { buildRows, rowEntries } from '../lib/logrows.js'
  import { levelStyle, levelLabel } from '../lib/loglevel.js'
  import { formatLine, logLine } from '../lib/logline.js'
  import { createUnifiedLogStore } from '../lib/unifiedlog.svelte.js'
  import Spinner from '../components/Spinner.svelte'
  import EmptyState from '../components/EmptyState.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'

  const store = createUnifiedLogStore()
  // The console renders NEWEST FIRST, so the scroll axis is inverted relative
  // to a classic tail: the live edge is the TOP, and scrolling DOWN reaches
  // further back in history. `follow` therefore pins to scrollTop 0.
  let follow = $state(true)
  let atOldest = $state(false) // scrolled to the bottom edge, where the oldest logs are
  let collapse = $state(true) // fold consecutive identical lines into one row with a count
  let showMeta = $state(false) // render the `key=value` tail; off by default to cut noise
  let suppressFollowPin = false // auto-follow must not fight an in-flight smooth scroll (#328)
  let box = $state(null)
  let copiedId = $state(null)
  let confirmPurge = $state(false)
  let purging = $state(false)
  let newCount = $state(0)
  let followOffMarker = null
  let wrap = $state(false) // console-wide line wrapping for message text (#328 round 2)
  let focusedId = $state(null) // row highlighted to mark the user's focus (#328 r3 #7)
  let copiedAll = $state(false) // transient confirmation for copy-all (#328 r3 #14)
  let suppressNextScroll = false // one-shot: swallow the anchor write's own scroll event (#328 r8 #4)
  let appendingOlder = false // an older batch is landing BELOW the viewport; leave scrollTop alone

  // Display rows: newest-first, with date separators and consecutive
  // duplicates folded. Pure, so the ordering and collapsing rules are tested
  // in logrows.test.js rather than through the DOM.
  // Collapsing folds over the `key=value` tail, so it is suppressed while
  // Details is on: that tail is precisely what distinguishes the folded lines,
  // and hiding it behind a count while the user has asked to see it would be
  // the wrong trade.
  const rows = $derived(buildRows(store.filtered, { collapse: collapse && !showMeta }))

  const liveMode = getLiveMode()

  const categories = [
    { value: '', label: 'All' },
    { value: 'backup', label: 'Backup' },
    { value: 'restore', label: 'Restore' },
    { value: 'health', label: 'Health' },
    { value: 'system', label: 'System' },
  ]

  const levels = [
    { value: '', label: 'All' },
    { value: 'error', label: 'Error' },
    { value: 'warn', label: 'Warn' },
    { value: 'info', label: 'Info' },
  ]

  onMount(async () => {
    await store.load()
    await fillViewport()
    // Seed the scroll flags from the real geometry. Without this, a history
    // short enough to fit the viewport (no scrollbar at all) leaves atOldest
    // false, so the "jump to oldest" overlay hovers over a console that is
    // already showing its oldest line.
    handleScroll()
    // Background full-history load: the console spans ALL logs (uniform with
    // the search view), so scrolling reaches the true end and the
    // "— End of logs —" marker shows there (#328).
    store.loadAll()
    const unsub = store.setupWs()
    // Poll safety net: in poll mode the timer is the primary path (10s); in
    // live/WS mode it runs as a slower catch-up (30s) so a missed WS event
    // (e.g. a terminal summary lost across a reconnect) is still surfaced
    // instead of waiting for a reload (#328 r9 #5).
    const pollTimer = setInterval(() => store.loadNewer(), liveMode === 'poll' ? 10000 : 30000)
    return () => { unsub(); if (pollTimer) clearInterval(pollTimer) }
  })

  // Track new entries arriving while follow is off. The marker snapshots the
  // buffer (newest ts + full id set) the moment follow turns off; newCount
  // counts only entries that arrived strictly AFTER that snapshot, so
  // pruning/batch merges can't skew it and same-second entries already on
  // screen are never recounted (#328 round 2).
  $effect(() => {
    if (follow) {
      followOffMarker = null
      newCount = 0
    } else if (store.entries.length) {
      if (!followOffMarker) followOffMarker = createFollowMarker(store.entries)
      newCount = countNewEntries(store.entries, followOffMarker)
    } else {
      newCount = 0
    }
  })

  // Geometry captured BEFORE the DOM updates, so the effect below can tell how
  // much content was inserted above the viewport.
  let preScrollHeight = 0
  let preScrollTop = 0
  $effect.pre(() => {
    store.filtered
    collapse
    if (box) {
      preScrollHeight = box.scrollHeight
      preScrollTop = box.scrollTop
    }
  })

  // Auto-follow, inverted: the newest line is at the TOP, so following the
  // live tail means holding scrollTop at 0.
  //
  // When follow is OFF, newer entries still arrive and are inserted ABOVE the
  // user's reading position, which would push it down the screen. Nothing in
  // the browser compensates for that — `overflow-anchor` is disabled on this
  // container — so the growth above the viewport is added back to scrollTop by
  // hand, keeping the line the user is reading exactly where it was.
  //
  // Appending OLDER entries at the bottom needs no compensation at all:
  // content added below the viewport does not move anything above it. That is
  // the one real simplification the inversion buys, and it is why
  // `loadOlder` no longer needs the content-identity anchoring dance.
  $effect(() => {
    store.filtered
    if (!box) return
    if (follow && !store.loadingOlder && !suppressFollowPin) {
      box.scrollTop = 0
      return
    }
    if (appendingOlder) return
    const grew = box.scrollHeight - preScrollHeight
    if (grew > 0 && preScrollTop > 0) {
      suppressNextScroll = true
      box.scrollTop = preScrollTop + grew
    }
  })

  // Scroll detection: auto-load older, toggle follow, reset new count.
  // While a load is in flight (store.loadingOlder), ignore scroll events
  // entirely — loadOlderAnchored is about to adjust scrollTop by the prepend
  // delta, and a scroll event fired mid-adjustment would otherwise re-trigger
  // loadOlder or toggle follow, fighting the adjustment (scroll teleport).
  function handleScroll() {
    if (!box) return
    // Swallow the programmatic anchor write's own scroll event so it can't
    // re-trigger loadOlder or toggle follow (#328 r8 #4).
    if (suppressNextScroll) {
      suppressNextScroll = false
      return
    }
    atOldest = nearBottom(box.scrollTop, box.scrollHeight, box.clientHeight)
    if (store.loadingOlder) return
    // Live edge is the top now: being there means following.
    const atLiveEdge = isAtTop(box.scrollTop)

    if (atLiveEdge && !follow) {
      follow = true
      newCount = 0
      followOffMarker = null
    } else if (!atLiveEdge && follow) {
      follow = false
    }

    // Auto-load older entries as the user approaches the BOTTOM (not only at
    // the exact edge), so the next batch is already streaming in by the time
    // they reach it. Loading older during a search is allowed: the
    // client-side filter re-applies to newly loaded entries, so deeper
    // matches surface as older batches arrive (#328 r9 #4).
    if (nearOldestEdge(box.scrollTop, box.scrollHeight, box.clientHeight) && store.hasMore && !store.loadingOlder) {
      loadOlderRows()
    }
  }

  // Older-batch load. With the console inverted, older entries append BELOW
  // the viewport, so there is nothing to compensate for: the browser keeps
  // scrollTop as content grows underneath. The previous oldest-first console
  // needed content-identity anchoring here because every older batch was
  // *prepended*, shifting the user's reading position down by the height of
  // the batch. That whole dance is gone — `appendingOlder` only tells the
  // follow effect to leave scrollTop alone while the append lands.
  async function loadOlderRows() {
    if (!box || store.loadingOlder || !store.hasMore) return
    appendingOlder = true
    try {
      await store.loadOlder()
      await tick()
    } finally {
      appendingOlder = false
    }
  }

  function jumpToPresent() {
    follow = true
    newCount = 0
    followOffMarker = null
    if (!box) return
    // The auto-follow effect pins scrollTop instantly on every entries
    // change — it would snap the viewport to the bottom and cancel the
    // smooth animation the moment `follow` flips true above. Suppress the
    // pin until the scroll settles (scrollend), then re-arm it (#328).
    suppressFollowPin = true
    box.scrollTo({ top: 0, behavior: 'smooth' })
    const rearm = () => { suppressFollowPin = false }
    if ('onscrollend' in window) {
      box.addEventListener('scrollend', rearm, { once: true })
    } else {
      setTimeout(rearm, 700)
    }
  }

  // With the full history loaded, jump straight to the oldest log — now the
  // BOTTOM edge, where the "— End of logs —" marker sits.
  function jumpToOldest() {
    if (box) box.scrollTo({ top: box.scrollHeight, behavior: 'smooth' })
  }

  // Filter switches reset the view to live-tail: follow back on, new-entry
  // marker cleared, viewport pinned to the newest line — the top.
  async function snapToPresent() {
    follow = true
    newCount = 0
    followOffMarker = null
    await tick()
    if (box) box.scrollTop = 0
  }

  // Return the data-entry-id of the topmost visible row, or null if none.
  function topmostEntryId() {
    if (!box) return null
    const boxTop = box.getBoundingClientRect().top
    for (const row of box.querySelectorAll('[data-entry-id]')) {
      if (row.getBoundingClientRect().top >= boxTop - 1) return row.dataset.entryId
    }
    return null
  }

  // Offset (px) of a row's top from the scroll container's top edge.
  function entryTopOffset(id) {
    const el = box?.querySelector(`[data-entry-id="${id}"]`)
    if (!el || !box) return null
    return el.getBoundingClientRect().top - box.getBoundingClientRect().top
  }

  // Every display toggle — wrap, collapse, details — reflows the rows and so
  // moves whatever the user was reading. They all re-anchor the same way:
  // remember which log line was where, apply the change, then put that line
  // back at the same screen position.
  //
  // Anchor on the HIGHLIGHTED row (the user's explicit focus) so the reflow
  // keeps that exact line pinned; fall back to the topmost visible row when
  // nothing is highlighted (#328 r4 #10). Anchoring on content identity rather
  // than on the total-height delta matters because these toggles reflow
  // content above AND below the viewport (#328 r3 #8).
  //
  // Collapsing can remove the anchor row outright — a folded-away duplicate
  // has no element left to measure — in which case the scroll position is
  // simply left alone rather than corrected against a row that no longer
  // exists.
  async function anchoredReflow(apply) {
    const anchorId = (focusedId && entryTopOffset(focusedId) != null) ? focusedId : topmostEntryId()
    const prevScrollTop = box ? box.scrollTop : 0
    const elTopBefore = anchorId ? entryTopOffset(anchorId) : null
    apply()
    if (!box) return
    await tick()
    if (anchorId && elTopBefore != null) {
      const elTopAfter = entryTopOffset(anchorId)
      if (elTopAfter != null) {
        suppressNextScroll = true
        box.scrollTop = anchoredScrollTop(prevScrollTop, elTopBefore, elTopAfter)
      }
    }
  }

  const toggleWrap = () => anchoredReflow(() => { wrap = !wrap })
  const toggleCollapse = () => anchoredReflow(() => { collapse = !collapse })
  const toggleMeta = () => anchoredReflow(() => { showMeta = !showMeta })

  async function fillViewport() {
    await tick()
    let guard = 0
    while (box && box.scrollHeight <= box.clientHeight && store.hasMore && !store.loadingOlder && guard < 20) {
      guard++
      await store.loadOlder()
      await tick()
    }
  }

  async function handleCategory(v) {
    await store.setCategory(v)
    await snapToPresent()
  }

  function handleLevel(v) {
    store.setLevelFilter(v)
    snapToPresent()
  }

  function handleJob(v) {
    store.setJobFilter(v)
    snapToPresent()
  }

  // Reset every filter back to its "All" default. Category is server-side,
  // so only reload when it was actually non-default (#328 r5 #7).
  async function resetFilters() {
    store.setLevelFilter('')
    store.setJobFilter('')
    store.setSearchFilter('')
    if (store.category !== '') {
      await store.setCategory('')
    }
    await snapToPresent()
  }

  // ---- formatting ----

  function fullTs(ts) {
    const d = new Date(ts)
    return Number.isNaN(d.getTime()) ? '' : formatDate(ts)
  }

  // ---- actions ----

  // Single-line log text lives in the shared logline module (message and
  // metadata split at the first `, key=` boundary and joined with ` | `),
  // so the console rows, copy button, copy-all, and export all render
  // every log line identically (#328).

  // What a folded row hides. Lines fold on their human-readable part, so the
  // `key=value` tails of the folded entries are the part that varied — a row
  // reading "Health check ×2" answers "which two?" on hover instead of forcing
  // the Details toggle. Capped, so a forty-deep fold does not produce a
  // tooltip taller than the screen.
  function repeatTitle(row) {
    const tails = rowEntries(row).map(e => formatLine(e).meta).filter(Boolean)
    const head = `${row.repeat} consecutive repeats — copy the row to get all of them`
    if (tails.length === 0) return head
    const shown = tails.slice(0, 10)
    const more = tails.length - shown.length
    return [head, '', ...shown, ...(more > 0 ? [`…and ${more} more`] : [])].join('\n')
  }

  // Copy a display row. A collapsed row stands for several entries, so copying
  // it yields every line it folded — the count badge is a display convenience,
  // never a way to lose log text.
  async function copyRow(row) {
    const lines = rowEntries(row).map(e => logLine(e))
    if (await copyText(lines.join('\n'))) {
      copiedId = row.entry.id
      setTimeout(() => { copiedId = null }, 2000)
    }
  }

  // Highlight the row immediately on pointer-down (not on click, which waits
  // for mouse-up) so the focus marker appears without delay (#328 r5 #2).
  function handleRowPointerDown(entry) {
    focusedId = entry.id
  }

  // Triple-click selects the whole line. Single-click highlighting is handled
  // by onpointerdown (above).
  function handleRowClick(e) {
    if (e.detail === 3) {
      // Capture the row synchronously: `e.currentTarget` is nulled once the
      // event finishes dispatching, so reading it inside the rAF callback was
      // always null and triple-click never selected anything (#328 r4 #8).
      const row = e.currentTarget
      if (!row) return
      // Defer the selection so it overrides the browser's native triple-click
      // fragment selection after it settles.
      requestAnimationFrame(() => selectRowText(row))
    }
  }

  function selectRowText(el) {
    const range = document.createRange()
    range.selectNodeContents(el)
    const sel = window.getSelection()
    sel.removeAllRanges()
    sel.addRange(range)
  }

  // A flex row of many <span>s serializes to clipboard with a newline between
  // each span, so a manual selection copies multi-line. Intercept the copy
  // event and write a single clean line instead (#328 #3). Only override when
  // the whole selection lives inside one row; multi-row selections fall
  // through to the browser's native copy.
  function handleCopy(e) {
    const sel = window.getSelection()
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return
    const row = closestRow(sel.anchorNode)
    if (!row || row !== closestRow(sel.focusNode)) return
    // dataset.entryId is always a string; activity ids are numbers and
    // run-log ids are the "rl-<n>" strings — compare on the string form so
    // the single-line copy interception works for both row kinds (#328).
    const entry = store.filtered.find(en => String(en.id) === row.dataset.entryId)
    if (!entry) return
    e.preventDefault()
    e.clipboardData.setData('text/plain', logLine(entry))
  }

  function closestRow(node) {
    let el = node && node.nodeType === 3 ? node.parentElement : node
    while (el) {
      if (el.dataset && el.dataset.entryId) return el
      el = el.parentElement
    }
    return null
  }

  // Shared line format for Export and Copy-all — keep the two in lockstep
  // with the single-row copy (logLine from the shared logline module).
  function formatLogLines() {
    return store.filtered.map(e => logLine(e))
  }

  async function copyAllLogs() {
    if (await copyText(formatLogLines().join('\n'))) {
      copiedAll = true
      setTimeout(() => { copiedAll = false }, 2000)
    }
  }

  function exportLogs() {
    const lines = formatLogLines()
    const blob = new Blob([lines.join('\n')], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `vault-logs-${new Date().toISOString().slice(0, 10)}.log`
    a.click()
    URL.revokeObjectURL(url)
  }

  async function handlePurge() {
    purging = true
    try {
      await api.purgeActivity()
      confirmPurge = false
      await store.load()
    } catch (e) {
      confirmPurge = false
      store.setError(e.message || 'Failed to purge logs')
    } finally {
      purging = false
    }
  }
</script>

<div>
  <!-- Header -->
  <div class="flex items-center justify-between mb-5">
    <div>
      <h1 class="text-2xl font-bold text-text">Logs</h1>
      <p class="text-sm text-text-muted mt-1">Unified activity and run-log output</p>
    </div>
    <div class="flex items-center gap-2">
      <button onclick={toggleWrap} aria-pressed={wrap}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm transition-colors flex items-center gap-1.5 {wrap ? 'text-text border-vault/50' : 'text-text-muted hover:text-text'}" title="Toggle line wrapping">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h10m-10 6h10M17 15l3-3-3-3"/></svg>
        Wrap: {wrap ? 'on' : 'off'}
      </button>
      <button onclick={toggleCollapse} aria-pressed={collapse}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm transition-colors flex items-center gap-1.5 {collapse ? 'text-text border-vault/50' : 'text-text-muted hover:text-text'}" title="Fold consecutive repeats of the same line into one row (off while Details is on)">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16"/></svg>
        Collapse: {collapse ? 'on' : 'off'}
      </button>
      <button onclick={toggleMeta} aria-pressed={showMeta}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm transition-colors flex items-center gap-1.5 {showMeta ? 'text-text border-vault/50' : 'text-text-muted hover:text-text'}" title="Show the key=value metadata tail on each line">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
        Details: {showMeta ? 'on' : 'off'}
      </button>
      <button onclick={copyAllLogs} disabled={store.filtered.length === 0}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text transition-colors flex items-center gap-1.5 disabled:opacity-40" title="Copy all filtered logs">
        {#if copiedAll}
          <svg aria-hidden="true" class="w-4 h-4 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
          Copied
        {:else}
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
          Copy
        {/if}
      </button>
      <button onclick={exportLogs} disabled={store.filtered.length === 0}
        class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-text transition-colors flex items-center gap-1.5 disabled:opacity-40" title="Export logs">
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
        Export
      </button>
      {#if !isReplicaMode()}
        <button onclick={() => confirmPurge = true} disabled={store.entries.length === 0}
          class="px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text-muted hover:text-danger hover:bg-danger/10 transition-colors flex items-center gap-1.5 disabled:opacity-40" title="Purge all logs">
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
          Purge
        </button>
      {/if}
    </div>
  </div>

  {#if isReplicaMode()}
    <div class="flex items-center gap-2.5 bg-surface-3 border border-border rounded-xl px-4 py-2.5 mb-4 text-sm text-text-muted">
      <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/></svg>
      <span>Read-only replica — write actions are disabled.</span>
    </div>
  {/if}

  <!-- Filter bar -->
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 mb-4">
    <label class="flex items-center gap-1.5">
      <span class="text-[11px] font-semibold uppercase tracking-wide text-text-dim">Level</span>
      <select
        value={store.levelFilter}
        onchange={(e) => handleLevel(e.target.value)}
        class="px-2 py-1 text-xs rounded-md bg-surface-3 border border-border text-text-muted focus:outline-none focus:ring-1 focus:ring-vault/50 {store.levelFilter ? 'border-vault text-text' : ''}"
      >
        {#each levels as lev (lev.value)}
          <option value={lev.value}>{lev.label}</option>
        {/each}
      </select>
    </label>
    <div class="w-px h-4 bg-border" aria-hidden="true"></div>
    <label class="flex items-center gap-1.5">
      <span class="text-[11px] font-semibold uppercase tracking-wide text-text-dim">Category</span>
      <select
        value={store.category}
        onchange={(e) => handleCategory(e.target.value)}
        class="px-2 py-1 text-xs rounded-md bg-surface-3 border border-border text-text-muted focus:outline-none focus:ring-1 focus:ring-vault/50 {store.category ? 'border-vault text-text' : ''}"
      >
        {#each categories as cat (cat.value)}
          <option value={cat.value}>{cat.label}</option>
        {/each}
      </select>
    </label>
    <div class="w-px h-4 bg-border" aria-hidden="true"></div>
    <label class="flex items-center gap-1.5">
      <span class="text-[11px] font-semibold uppercase tracking-wide text-text-dim">Job</span>
      <select
        value={store.jobFilter}
        onchange={(e) => handleJob(e.target.value)}
        class="px-2 py-1 text-xs rounded-md bg-surface-3 border border-border text-text-muted focus:outline-none focus:ring-1 focus:ring-vault/50 {store.jobFilter ? 'border-vault text-text' : ''}"
      >
        <option value="">All</option>
        {#each store.jobs as jobName (jobName)}
          <option value={jobName}>{jobName}</option>
        {/each}
      </select>
    </label>
    <div class="w-px h-4 bg-border" aria-hidden="true"></div>
    <label class="flex items-center gap-1.5">
      <span class="text-[11px] font-semibold uppercase tracking-wide text-text-dim">Search</span>
      <div class="relative">
        <input
          type="text"
          value={store.search}
          oninput={(e) => { store.setSearchFilter(e.target.value); if (!e.target.value) snapToPresent() }}
          placeholder="Filter logs…"
          class="px-2 py-1 pr-6 text-xs rounded-md bg-surface-3 border border-border text-text-muted focus:outline-none focus:ring-1 focus:ring-vault/50 w-44 placeholder:text-text-dim/50 {store.search ? 'border-vault text-text' : ''}"
        />
        {#if store.search}
          <button type="button" onclick={() => { store.setSearchFilter(''); snapToPresent() }}
            class="absolute right-1.5 top-1/2 -translate-y-1/2 text-text-dim hover:text-text text-xs leading-none" title="Clear search" aria-label="Clear search">×</button>
        {/if}
      </div>
    </label>
    {#if store.category || store.levelFilter || store.jobFilter || store.search}
      <div class="w-px h-4 bg-border" aria-hidden="true"></div>
      <button type="button" onclick={resetFilters}
        class="px-2 py-1 text-xs rounded-md bg-surface-3 border border-border text-text-muted hover:text-text transition-colors" title="Reset all filters">
        Reset filters
      </button>
    {/if}
  </div>

  <!-- Console -->
  {#if store.loading && !store.loaded}
    <Spinner text="Loading logs..." />
  {:else if store.error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4 flex items-center gap-3">
      <svg aria-hidden="true" class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      <span class="text-sm">{store.error}</span>
    </div>
  {:else if store.search && store.filtered.length === 0 && (store.searching || store.loadingOlder || store.loading)}
    <div class="py-4 text-center text-sm text-text-dim flex items-center justify-center gap-2" aria-live="polite">
      <span aria-hidden="true" class="inline-block w-4 h-4 border border-vault/30 border-t-vault rounded-full animate-spin"></span>
      <span>searching older logs…</span>
    </div>
  {:else if store.filtered.length === 0}
    <EmptyState title="No logs" subtitle="Events appear here as they happen" description="Backup and restore operations will stream their output into this console.">
      {#snippet iconSlot()}
        <svg class="w-12 h-12 text-text-dim" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/></svg>
      {/snippet}
    </EmptyState>
  {:else}
    <!-- Console wrapper: scroll container + fixed overlay buttons -->
    <div class="relative" style="max-height: 72vh">
      <!-- Jump to present / new-entries indicator (fixed, bottom-right whenever follow is off) -->
      {#if !follow}
        <button onclick={jumpToPresent}
          class="absolute bottom-3 right-3 z-10 px-3 py-1.5 bg-vault/90 hover:bg-vault text-white text-xs font-medium rounded-lg shadow-lg transition-all flex items-center gap-1.5"
          title="Jump to present">
          <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 10l7-7m0 0l7 7m-7-7v18"/></svg>
          {#if newCount > 0}{newCount} new {newCount === 1 ? 'entry' : 'entries'}{:else}Jump to present{/if}
        </button>
      {/if}

      {#if !atOldest}
        <button onclick={jumpToOldest}
          class="absolute top-3 right-3 z-10 p-2 bg-surface-3 border border-border rounded-lg text-text-muted hover:text-text shadow-lg transition-colors"
          title="Jump to oldest log" aria-label="Jump to oldest log">
          <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 14l7 7m0 0l7-7m-7 7V3"/></svg>
        </button>
      {/if}

      <!-- Older-batch loading spinner removed: the console now loads the
           ENTIRE history in the background (loadAll), so there is no
           user-facing "reaching further back" phase to signal — the fill is
           fast and silent, and scroll-triggered loads no longer happen once
           the buffer spans the true end (#328). -->

      <!-- Scroll container -->
      <div bind:this={box} onscroll={handleScroll} oncopy={handleCopy}
        class="console-scroll bg-surface-1 border border-border rounded-xl overflow-y-auto font-mono text-xs leading-tight h-[72vh]"
        style="overflow-anchor: none;"
        role="log"
      >
        {#each rows as row (row.key)}
          {#if row.kind === 'date'}
            <!-- Day separator. Sticky, so the day you are reading stays named
                 while you scroll through it; every row below can then drop the
                 date and show a bare time. -->
            <div class="sticky top-0 z-[1] flex items-center gap-2 pl-3 pr-12 py-1 bg-surface-2/95 backdrop-blur-sm border-y border-border select-none">
              <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{row.label}</span>
              <span class="h-px flex-1 bg-border" aria-hidden="true"></span>
              <span class="text-[10px] text-text-dim tabular-nums">{row.count}</span>
            </div>
          {:else}
            {@const entry = row.entry}
            {@const st = levelStyle(entry.level)}
            {@const line = formatLine(entry)}
            <div class="flex items-baseline gap-2 px-2 py-px border-l-2 {st.border} {focusedId === entry.id ? 'bg-vault/15' : st.row} group hover:bg-surface-2/50" data-entry-id={entry.id} onclick={(e) => handleRowClick(e)} onpointerdown={() => handleRowPointerDown(entry)}>
              <!-- Time only; the full date is on the day separator above and in
                   the tooltip, which is most of the width this row used to spend. -->
              <span class="shrink-0 text-text-dim tabular-nums" title={fullTs(entry.ts)}>{rowTime(entry.ts)}</span>
              <!-- Level pill. Colour AND text, so the console still reads in
                   the monochrome themes where every hue collapses to one ink. -->
              <span class="shrink-0 w-[3.5rem] text-center rounded px-1 text-[10px] uppercase tracking-wide {st.pill}" title={entry.level}>{levelLabel(entry.level)}</span>
              <span class="shrink-0 w-[7ch] truncate text-text-dim" title={entry.category}>{entry.category}</span>
              <span class="min-w-0 flex-1 flex items-baseline gap-1.5">
                <span class="{wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-nowrap overflow-hidden text-ellipsis'} shrink-0 max-w-full {st.text} {entry.type === 'summary' ? 'font-medium' : ''}" title={line.message}>
                  {line.message}
                </span>
                {#if row.repeat > 1}
                  <span class="shrink-0 rounded px-1 text-[10px] tabular-nums bg-surface-3 text-text-muted" title={repeatTitle(row)}>×{row.repeat}</span>
                {/if}
                {#if showMeta && line.meta}<span class="{st.meta} min-w-0 overflow-hidden text-ellipsis whitespace-nowrap" title={line.meta}>{line.meta}</span>{/if}
              </span>
              <button type="button" onclick={() => copyRow(row)}
                class="{copiedId === entry.id ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'} p-0.5 text-text-dim/40 hover:text-text shrink-0 transition-all self-center cursor-pointer" title="Copy">
                {#if copiedId === entry.id}
                  <svg aria-hidden="true" class="w-3 h-3 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
                {:else}
                  <svg aria-hidden="true" class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
                {/if}
              </button>
            </div>
          {/if}
        {/each}
        <!-- End-of-history marker now sits at the BOTTOM: with newest first,
             the oldest log is the last row on the page. -->
        {#if !store.hasMore && !store.pruned}
          <div data-end-marker class="h-8 flex items-center justify-center text-center text-[11px] text-text-dim select-none" aria-live="polite">
            — End of logs —
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<ConfirmDialog
  show={confirmPurge}
  title="Purge All Logs"
  message="This will permanently delete all activity log entries. This action cannot be undone."
  confirmLabel={purging ? 'Purging...' : 'Purge All'}
  variant="danger"
  onconfirm={handlePurge}
  oncancel={() => confirmPurge = false}
/>
