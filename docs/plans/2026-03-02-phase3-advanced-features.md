# Phase 3: Advanced Features Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Add the restore wizard, history timeline with filtering and size chart, storage health indicators, logs improvements, global search command palette, job bulk operations, and quick restore from Dashboard.

**Architecture:** Mix of frontend and backend. New backend endpoints for storage space info and paginated history. Frontend adds several new components (RestoreWizard, SizeChart, CommandPalette). Uses existing WebSocket for real-time restore progress.

**Tech Stack:** Svelte 5, Tailwind CSS, Go, Chart rendering via inline SVG (no external chart library)

**Depends on:** Phase 1 + Phase 2 complete

---

### Task 1: Redesign Restore as guided wizard

**Files:**
- Create: `web/src/components/RestoreWizard.svelte`
- Modify: `web/src/pages/Restore.svelte`

**Step 1: Create RestoreWizard component**

3-step wizard replacing the dropdown-based approach:

**Step 1 — What to restore:** Searchable visual grid of all backed-up items across all jobs. Each item shows: icon (container/VM/folder), name, last backup date, restore point count. Click to select. Group by type with tab headers (Containers | VMs | Folders).

**Step 2 — Which version:** Timeline of restore points for the selected item. Each shows: date, size, backup type badge, verified badge (if integrity check passed), item count. Most recent successful backup highlighted as "Recommended" with a subtle vault-colored border.

**Step 3 — Restore options:** Destination override toggle with PathBrowser, encryption passphrase input (conditional), clear warning banner about overwrite, Restore button.

Component manages its own state and emits `onrestore` with the full payload.

**Step 2: Replace Restore page content**

In `Restore.svelte`, replace the dropdown approach with:

```svelte
<RestoreWizard
  jobs={jobs}
  onrestore={doRestore}
/>
```

Keep the existing `doRestore` function and toast/confirm patterns.

**Step 3: Add restore progress view**

After restore starts, show a dedicated progress card instead of just a toast:
- Item name and restore point info
- Progress bar (if WebSocket provides progress events)
- Status text: "Restoring..." → "Complete" or "Failed: reason"
- Collapsible log output section

**Step 4: Commit**

```bash
git add web/src/components/RestoreWizard.svelte web/src/pages/Restore.svelte
git commit -m "feat: redesign restore page as 3-step guided wizard"
```

---

### Task 2: Redesign History as date-grouped timeline with filtering

**Files:**
- Modify: `web/src/pages/History.svelte`
- Modify: `internal/api/handlers/jobs.go` (add pagination)

**Step 1: Add pagination to history endpoint**

In `internal/api/handlers/jobs.go`, modify `GetHistory` to support `offset` query param:

```go
offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
// Pass offset to DB query
```

Update `internal/db/job_repo.go` `GetJobHistory` to accept offset parameter and add `OFFSET ?` to the SQL query.

**Step 2: Add status filter buttons**

Add filter buttons above the history list: All | Completed | Failed | Running

```svelte
let statusFilter = $state('all')
const statusFilters = ['all', 'completed', 'failed', 'running']

let filteredRuns = $derived(
  allRuns.filter(r =>
    (statusFilter === 'all' || r.status === statusFilter) &&
    (selectedJob === 'all' || r.job_id === selectedJob)
  )
)
```

**Step 3: Replace table with date-grouped timeline**

Group runs by date (Today/Yesterday/date). Render as cards instead of table rows:

```svelte
{#each groupedByDate as [dateLabel, runs]}
  <h3 class="text-sm font-medium text-text-muted mt-6 mb-2">{dateLabel}</h3>
  {#each runs as run}
    <div class="bg-surface-2 border border-border rounded-xl p-4 mb-2 hover:border-vault/30 transition-colors">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class={statusBadge(run.status)}>{run.status}</span>
          <span class="font-medium">{run.job_name}</span>
        </div>
        <span class="text-sm text-text-dim">{relTime(run.started_at)}</span>
      </div>
      <div class="flex gap-4 mt-2 text-xs text-text-dim">
        <span>{run.items_done}/{run.items_total} items</span>
        <span>{formatBytes(run.size_bytes)}</span>
        <span>{formatDuration(run)}</span>
      </div>
      {#if getFailureReason(run)}
        <p class="text-xs text-danger mt-2">{getFailureReason(run)}</p>
      {/if}
      <!-- expandable per-item details (keep existing) -->
    </div>
  {/each}
{/each}
```

**Step 4: Add search by item name**

Add a search input that filters runs by checking if any item in the run log matches the search term.

**Step 5: Add "Load more" pagination**

```svelte
{#if hasMore}
  <button onclick={loadMore} class="btn btn-secondary w-full mt-4">
    Load more ({totalShown} of {totalCount} runs)
  </button>
{/if}
```

**Step 6: Commit**

```bash
git add web/src/pages/History.svelte internal/api/handlers/jobs.go internal/db/job_repo.go web/src/lib/api.js
git commit -m "feat: redesign history as date-grouped timeline with filtering and pagination"
```

---

### Task 3: Add backup size trends sparkline chart

**Files:**
- Create: `web/src/components/SizeChart.svelte`
- Modify: `web/src/pages/History.svelte`

**Step 1: Create SizeChart component**

Inline SVG sparkline (no chart library dependency). Takes an array of `{date, size}` points:

```svelte
<script>
  import { formatBytes } from '../lib/utils.js'

  let { data = [], height = 60 } = $props()

  let width = 400
  let points = $derived(() => {
    if (data.length < 2) return ''
    const maxSize = Math.max(...data.map(d => d.size))
    const minSize = Math.min(...data.map(d => d.size))
    const range = maxSize - minSize || 1
    return data.map((d, i) => {
      const x = (i / (data.length - 1)) * width
      const y = height - ((d.size - minSize) / range) * (height - 10) - 5
      return `${x},${y}`
    }).join(' ')
  })

  let hoveredPoint = $state(null)
</script>

<div class="bg-surface-2 border border-border rounded-xl p-4">
  <div class="flex items-center justify-between mb-3">
    <h3 class="text-sm font-medium text-text-muted">Backup Size Trend</h3>
    {#if hoveredPoint}
      <span class="text-xs text-text-dim">{hoveredPoint.date}: {formatBytes(hoveredPoint.size)}</span>
    {/if}
  </div>
  <svg viewBox="0 0 {width} {height}" class="w-full" style="height: {height}px">
    <polyline
      points={points()}
      fill="none"
      stroke="var(--color-vault)"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
    {#each data as d, i}
      <circle
        cx={(i / (data.length - 1)) * width}
        cy={/* calculate y */}
        r="4"
        fill="var(--color-vault)"
        class="opacity-0 hover:opacity-100 cursor-pointer"
        onmouseenter={() => hoveredPoint = d}
        onmouseleave={() => hoveredPoint = null}
      />
    {/each}
  </svg>
</div>
```

**Step 2: Integrate into History page**

Compute size data from runs and render above the timeline:

```svelte
let sizeData = $derived(
  allRuns
    .filter(r => r.status === 'completed' || r.status === 'success')
    .slice(0, 30)
    .reverse()
    .map(r => ({ date: new Date(r.started_at).toLocaleDateString(), size: r.size_bytes }))
)

{#if sizeData.length >= 2}
  <SizeChart data={sizeData} />
{/if}
```

**Step 3: Commit**

```bash
git add web/src/components/SizeChart.svelte web/src/pages/History.svelte
git commit -m "feat: add backup size trends sparkline chart to history"
```

---

### Task 4: Add storage health indicators

**Files:**
- Modify: `web/src/pages/Storage.svelte`
- Modify: `internal/api/handlers/storage.go` (optional: add space info endpoint)

**Step 1: Show dependent job count on storage cards**

Fetch dependent job count for each storage destination and display:

```svelte
<span class="text-xs text-text-dim">Used by {depCounts[dest.id] || 0} jobs</span>
```

**Step 2: Show inline test connection result**

After clicking "Test Connection", show result as a persistent badge on the card (not just during click):

```svelte
{#if testResults[dest.id] === true}
  <span class="badge badge-success">Connected</span>
{:else if testResults[dest.id] === false}
  <span class="badge badge-danger">Failed</span>
{/if}
```

Store test results in component state so they persist until page navigation.

**Step 3: Replace floppy disk icons with type-appropriate icons**

Replace generic icon with:
- Local: folder icon (SVG)
- SFTP: server/cloud icon (SVG)
- SMB: network share icon (SVG)
- S3: cloud icon (SVG)

Use inline SVGs matching the existing icon style (24x24 viewBox, 1.5px stroke).

**Step 4: Show last successful write timestamp**

If the test connection returns a timestamp or we can track last backup write time per storage, show it:

```svelte
<span class="text-xs text-text-dim">Last write: {relTime(dest.last_write_at)}</span>
```

This may require adding `last_write_at` to the storage destination model, updated when a backup completes.

**Step 5: Commit**

```bash
git add web/src/pages/Storage.svelte internal/api/handlers/storage.go
git commit -m "feat: add storage health indicators, type icons, and dependent job count"
```

---

### Task 5: Improve Logs page

**Files:**
- Modify: `web/src/pages/Logs.svelte`

**Step 1: Add log level filter**

Add level filter buttons alongside the existing category filter:

```svelte
let levelFilter = $state('all')
const levels = ['all', 'error', 'warning', 'info']
```

Filter entries by both category and level.

**Step 2: Priority error styling**

Error entries get a red left border and are expanded by default:

```svelte
<div class="... {entry.level === 'error' ? 'border-l-4 border-l-danger' : ''}">
```

Auto-expand error details:

```svelte
let expandedIds = $state(new Set(
  entries.filter(e => e.level === 'error').map(e => e.id)
))
```

**Step 3: Add copy button**

Add a copy-to-clipboard button per log entry:

```svelte
<button onclick={() => copyEntry(entry)} class="btn-icon" title="Copy">
  <!-- clipboard icon SVG -->
</button>
```

```js
function copyEntry(entry) {
  const text = `[${entry.level}] [${entry.category}] ${entry.message}\n${entry.details || ''}`
  navigator.clipboard.writeText(text)
  toast = { message: 'Copied to clipboard', type: 'success', key: Date.now() }
}
```

**Step 4: Add auto-scroll toggle**

For live log monitoring:

```svelte
let autoScroll = $state(true)

// In the WS message handler:
if (autoScroll) {
  const container = document.querySelector('.log-container')
  if (container) container.scrollTop = 0  // since entries prepend
}
```

**Step 5: Add export button**

```svelte
<button onclick={exportLogs} class="btn btn-sm btn-secondary">
  Export
</button>
```

```js
function exportLogs() {
  const text = entries.map(e =>
    `${e.created_at} [${e.level}] [${e.category}] ${e.message}`
  ).join('\n')
  const blob = new Blob([text], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `vault-logs-${new Date().toISOString().slice(0, 10)}.txt`
  a.click()
  URL.revokeObjectURL(url)
}
```

**Step 6: Commit**

```bash
git add web/src/pages/Logs.svelte
git commit -m "feat: add log level filter, error priority styling, copy, auto-scroll, and export"
```

---

### Task 6: Add global search / command palette

**Files:**
- Create: `web/src/components/CommandPalette.svelte`
- Modify: `web/src/App.svelte`

**Step 1: Create CommandPalette component**

A modal overlay triggered by `Ctrl+K` or `/` (when not in an input):

```svelte
<script>
  import { navigate } from '../lib/router.svelte.js'
  import api from '../lib/api.js'

  let { show = $bindable(false) } = $props()
  let query = $state('')
  let results = $state([])
  let selectedIndex = $state(0)

  const staticActions = [
    { label: 'Go to Dashboard', action: () => navigate('/'), category: 'Navigation' },
    { label: 'Go to Jobs', action: () => navigate('/jobs'), category: 'Navigation' },
    { label: 'Go to Storage', action: () => navigate('/storage'), category: 'Navigation' },
    { label: 'Go to History', action: () => navigate('/history'), category: 'Navigation' },
    { label: 'Go to Restore', action: () => navigate('/restore'), category: 'Navigation' },
    { label: 'Go to Logs', action: () => navigate('/logs'), category: 'Navigation' },
    { label: 'Go to Settings', action: () => navigate('/settings'), category: 'Navigation' },
    { label: 'Go to Replication', action: () => navigate('/replication'), category: 'Navigation' },
  ]

  let filtered = $derived(() => {
    if (!query.trim()) return staticActions.slice(0, 8)
    const q = query.toLowerCase()
    return [...staticActions, ...results]
      .filter(r => r.label.toLowerCase().includes(q))
      .slice(0, 10)
  })

  $effect(() => {
    if (query.length >= 2) {
      searchAsync(query)
    }
  })

  async function searchAsync(q) {
    // Search jobs and storage for matches
    const [jobs, storage] = await Promise.all([
      api.listJobs().catch(() => []),
      api.listStorage().catch(() => []),
    ])
    const dynamic = [
      ...jobs.filter(j => j.name.toLowerCase().includes(q.toLowerCase()))
        .map(j => ({ label: `Job: ${j.name}`, action: () => navigate('/jobs'), category: 'Jobs' })),
      ...storage.filter(s => s.name.toLowerCase().includes(q.toLowerCase()))
        .map(s => ({ label: `Storage: ${s.name}`, action: () => navigate('/storage'), category: 'Storage' })),
    ]
    results = dynamic
  }

  function execute(item) {
    item.action()
    show = false
    query = ''
  }

  function handleKeydown(e) {
    if (e.key === 'ArrowDown') { selectedIndex = Math.min(selectedIndex + 1, filtered().length - 1); e.preventDefault() }
    if (e.key === 'ArrowUp') { selectedIndex = Math.max(selectedIndex - 1, 0); e.preventDefault() }
    if (e.key === 'Enter' && filtered()[selectedIndex]) { execute(filtered()[selectedIndex]) }
    if (e.key === 'Escape') { show = false }
  }
</script>

{#if show}
  <div class="fixed inset-0 bg-black/60 backdrop-blur-sm z-[60] flex items-start justify-center pt-[15vh]"
    onclick={() => show = false}>
    <div class="bg-surface w-full max-w-lg mx-4 rounded-xl border border-border shadow-2xl overflow-hidden"
      onclick|stopPropagation>
      <div class="p-3 border-b border-border">
        <input
          type="text" bind:value={query} onkeydown={handleKeydown}
          placeholder="Search or jump to..."
          class="w-full bg-transparent outline-none text-base placeholder:text-text-dim"
          autofocus
        />
      </div>
      <div class="max-h-80 overflow-y-auto py-2">
        {#each filtered() as item, i}
          <button
            onclick={() => execute(item)}
            class="w-full px-4 py-2.5 text-left flex items-center justify-between hover:bg-surface-3 transition-colors
              {i === selectedIndex ? 'bg-surface-3' : ''}"
          >
            <span>{item.label}</span>
            <span class="text-xs text-text-dim">{item.category}</span>
          </button>
        {/each}
      </div>
      <div class="px-4 py-2 border-t border-border text-xs text-text-dim flex gap-4">
        <span><kbd class="px-1 py-0.5 bg-surface-3 rounded text-[10px]">↑↓</kbd> navigate</span>
        <span><kbd class="px-1 py-0.5 bg-surface-3 rounded text-[10px]">↵</kbd> select</span>
        <span><kbd class="px-1 py-0.5 bg-surface-3 rounded text-[10px]">esc</kbd> close</span>
      </div>
    </div>
  </div>
{/if}
```

**Step 2: Wire into App.svelte**

Add global keyboard listener and CommandPalette import:

```js
let showPalette = $state(false)

function handleGlobalKeydown(e) {
  if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
    e.preventDefault()
    showPalette = !showPalette
  }
}
```

```svelte
<svelte:window onkeydown={handleGlobalKeydown} />
<CommandPalette bind:show={showPalette} />
```

**Step 3: Commit**

```bash
git add web/src/components/CommandPalette.svelte web/src/App.svelte
git commit -m "feat: add global search command palette (Ctrl+K)"
```

---

### Task 7: Add bulk operations to Jobs page

**Files:**
- Modify: `web/src/pages/Jobs.svelte`

**Step 1: Add selection state**

```js
let selectedJobs = $state(new Set())
let selectMode = $state(false)

function toggleSelect(id) {
  if (selectedJobs.has(id)) selectedJobs.delete(id)
  else selectedJobs.add(id)
  selectedJobs = new Set(selectedJobs) // trigger reactivity
}

function selectAll() {
  if (selectedJobs.size === jobs.length) selectedJobs = new Set()
  else selectedJobs = new Set(jobs.map(j => j.id))
}
```

**Step 2: Add bulk action bar**

Show a floating bar when items are selected:

```svelte
{#if selectedJobs.size > 0}
  <div class="sticky top-0 z-10 bg-surface-2 border border-vault/30 rounded-xl p-3 mb-4 flex items-center justify-between shadow-lg">
    <span class="text-sm font-medium">{selectedJobs.size} job(s) selected</span>
    <div class="flex gap-2">
      <button onclick={bulkEnable} class="btn btn-sm btn-secondary">Enable All</button>
      <button onclick={bulkDisable} class="btn btn-sm btn-secondary">Disable All</button>
      <button onclick={bulkRun} class="btn btn-sm btn-primary">Run Selected</button>
      <button onclick={bulkDelete} class="btn btn-sm btn-danger">Delete</button>
      <button onclick={() => selectedJobs = new Set()} class="btn btn-sm btn-ghost">Cancel</button>
    </div>
  </div>
{/if}
```

**Step 3: Add checkboxes to job cards**

Add a checkbox at the left of each card:

```svelte
<input type="checkbox" checked={selectedJobs.has(job.id)} onchange={() => toggleSelect(job.id)}
  class="w-4 h-4 rounded border-border" />
```

**Step 4: Implement bulk handlers**

```js
async function bulkEnable() {
  await Promise.all([...selectedJobs].map(id => {
    const job = jobs.find(j => j.id === id)
    return api.updateJob(id, { ...job, enabled: true })
  }))
  selectedJobs = new Set()
  await loadData()
}
// Similar for bulkDisable, bulkRun, bulkDelete
```

**Step 5: Commit**

```bash
git add web/src/pages/Jobs.svelte
git commit -m "feat: add bulk operations (enable/disable/run/delete) to jobs page"
```

---

### Task 8: Add quick restore from Dashboard

**Files:**
- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Add restore button per protected item**

In the Protection Status section, add a small "Restore" button next to each protected item:

```svelte
<button onclick={() => quickRestore(item)} class="btn-icon text-xs" title="Restore">
  <!-- restore icon SVG -->
</button>
```

**Step 2: Implement quick restore navigation**

Navigate to the Restore page with the item pre-selected via URL hash:

```js
function quickRestore(item) {
  navigate(`/restore?item=${encodeURIComponent(item.name)}&type=${item.type}`)
}
```

Update `Restore.svelte` to read URL params on mount and pre-select the matching item.

**Step 3: Commit**

```bash
git add web/src/pages/Dashboard.svelte web/src/pages/Restore.svelte
git commit -m "feat: add quick restore action from dashboard protection status"
```

---

### Task 9: Phase 3 final verification

**Files:** None (verification only)

**Verification checklist:**
- [ ] Restore wizard: 3-step flow works end to end
- [ ] Restore progress view shows during active restore
- [ ] History: date-grouped timeline renders correctly
- [ ] History: status filters work (All/Completed/Failed/Running)
- [ ] History: search by item name works
- [ ] History: "Load more" pagination works
- [ ] Size chart: sparkline renders with hover tooltips
- [ ] Storage: type-specific icons display correctly
- [ ] Storage: test connection result persists on card
- [ ] Storage: dependent job count shown
- [ ] Logs: level filter works
- [ ] Logs: error entries have red left border and auto-expand
- [ ] Logs: copy and export buttons work
- [ ] Command palette: Ctrl+K opens, search works, keyboard nav works
- [ ] Bulk operations: select, enable/disable/run/delete all work
- [ ] Quick restore from Dashboard navigates correctly
- [ ] Dark mode: all new components look correct
- [ ] Mobile: all new components are responsive

```bash
cd web && npm run build
go test ./... -short
make lint
```
