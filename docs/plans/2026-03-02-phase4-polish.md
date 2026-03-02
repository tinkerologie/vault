# Phase 4: Polish Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Final polish — mobile bottom navigation, pull-to-refresh, theme toggle shortcut, log export improvements, and job duplication. These are low-risk quality-of-life improvements.

**Architecture:** All frontend-only changes. No backend modifications needed.

**Tech Stack:** Svelte 5, Tailwind CSS

**Depends on:** Phase 1 + Phase 2 + Phase 3 complete

---

### Task 1: Add mobile bottom navigation bar

**Files:**
- Modify: `web/src/App.svelte`
- Modify: `web/src/app.css`

**Step 1: Create bottom nav for mobile**

Add a fixed bottom navigation bar that shows on mobile (`lg:hidden`). Show the 5 most important nav items with icons:

```svelte
<!-- Bottom nav for mobile -->
<nav class="fixed bottom-0 left-0 right-0 bg-surface border-t border-border flex justify-around py-2 z-40 lg:hidden">
  {#each mobileNav as item}
    <button
      onclick={() => { navigate(item.path); mobileMenuOpen = false }}
      class="flex flex-col items-center gap-0.5 px-3 py-1 text-xs
        {isActive(item.path) ? 'text-vault' : 'text-text-muted'}"
    >
      <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
        <path d={item.icon} />
      </svg>
      <span>{item.label}</span>
    </button>
  {/each}
</nav>
```

Define mobile nav items (top 5):

```js
const mobileNav = [
  { path: '/', label: 'Home', icon: navItems[0].icon },
  { path: '/jobs', label: 'Jobs', icon: navItems[1].icon },
  { path: '/history', label: 'History', icon: navItems[3].icon },
  { path: '/restore', label: 'Restore', icon: navItems[4].icon },
  { path: '/settings', label: 'More', icon: navItems[7].icon },
]
```

**Step 2: Add bottom padding to main content on mobile**

Prevent the bottom nav from overlapping content:

```svelte
<main class="... pb-16 lg:pb-0">
```

**Step 3: Keep hamburger menu for less-used pages**

The hamburger menu still provides access to all 8 pages. The bottom nav is a quick shortcut to the 5 most used.

**Step 4: Commit**

```bash
git add web/src/App.svelte
git commit -m "feat: add mobile bottom navigation bar for quick access"
```

---

### Task 2: Add pull-to-refresh on Dashboard and History

**Files:**
- Create: `web/src/components/PullToRefresh.svelte`
- Modify: `web/src/pages/Dashboard.svelte`
- Modify: `web/src/pages/History.svelte`

**Step 1: Create PullToRefresh wrapper component**

A touch-based pull-to-refresh component for mobile. Wraps content and calls `onrefresh` when the user pulls down:

```svelte
<script>
  let { onrefresh = async () => {}, children } = $props()

  let pulling = $state(false)
  let pullDistance = $state(0)
  let refreshing = $state(false)
  let startY = 0
  const threshold = 80

  function handleTouchStart(e) {
    if (window.scrollY === 0) {
      startY = e.touches[0].clientY
      pulling = true
    }
  }

  function handleTouchMove(e) {
    if (!pulling) return
    const diff = e.touches[0].clientY - startY
    if (diff > 0) {
      pullDistance = Math.min(diff * 0.5, threshold * 1.5)
      if (diff > 10) e.preventDefault()
    }
  }

  async function handleTouchEnd() {
    if (pullDistance >= threshold) {
      refreshing = true
      await onrefresh()
      refreshing = false
    }
    pulling = false
    pullDistance = 0
  }
</script>

<div
  ontouchstart={handleTouchStart}
  ontouchmove={handleTouchMove}
  ontouchend={handleTouchEnd}
  class="relative"
>
  {#if pullDistance > 0 || refreshing}
    <div class="flex justify-center py-3 transition-all" style="height: {pullDistance}px">
      {#if refreshing}
        <div class="w-5 h-5 border-2 border-vault/30 border-t-vault rounded-full animate-spin"></div>
      {:else}
        <div class="w-5 h-5 text-vault transition-transform"
          style="transform: rotate({(pullDistance / threshold) * 360}deg); opacity: {pullDistance / threshold}">
          ↓
        </div>
      {/if}
    </div>
  {/if}
  {@render children()}
</div>
```

**Step 2: Wrap Dashboard content**

```svelte
<PullToRefresh onrefresh={loadDashboard}>
  <!-- existing dashboard content -->
</PullToRefresh>
```

**Step 3: Wrap History content**

```svelte
<PullToRefresh onrefresh={loadData}>
  <!-- existing history content -->
</PullToRefresh>
```

**Step 4: Commit**

```bash
git add web/src/components/PullToRefresh.svelte web/src/pages/Dashboard.svelte web/src/pages/History.svelte
git commit -m "feat: add pull-to-refresh on mobile for Dashboard and History"
```

---

### Task 3: Add job duplication

**Files:**
- Modify: `web/src/pages/Jobs.svelte`

**Step 1: Add "Duplicate" action to job cards**

Add a copy icon button alongside Edit and Delete:

```svelte
<button onclick={() => duplicateJob(job)} class="btn-icon" title="Duplicate">
  <!-- copy/duplicate icon SVG -->
</button>
```

**Step 2: Implement duplicate handler**

```js
async function duplicateJob(job) {
  const { job: fullJob, items } = await api.getJob(job.id)
  // Pre-fill form with cloned data
  form = {
    ...defaultForm(),
    name: `${fullJob.name} (copy)`,
    description: fullJob.description,
    schedule: fullJob.schedule,
    storage_dest_id: fullJob.storage_dest_id,
    compression: fullJob.compression,
    encryption: fullJob.encryption,
    container_mode: fullJob.container_mode,
    vm_mode: fullJob.vm_mode,
    backup_type_chain: fullJob.backup_type_chain,
    retention_count: fullJob.retention_count,
    retention_days: fullJob.retention_days,
    pre_script: fullJob.pre_script,
    post_script: fullJob.post_script,
    notify_on: fullJob.notify_on,
    verify_backup: fullJob.verify_backup,
    enabled: false, // disabled by default for safety
    items: items.map(i => ({ ...i, id: undefined, job_id: undefined })),
  }
  editing = null // create mode, not edit
  showModal = true
  step = 3 // jump to review step so user can verify before creating
}
```

**Step 3: Commit**

```bash
git add web/src/pages/Jobs.svelte
git commit -m "feat: add job duplication action"
```

---

### Task 4: Enhance theme toggle accessibility

**Files:**
- Modify: `web/src/App.svelte`

The theme toggle was moved to the sidebar footer in Phase 2. This task polishes it.

**Step 1: Add keyboard shortcut for theme cycling**

Add to the global keydown handler in App.svelte:

```js
// In handleGlobalKeydown:
if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'L') {
  e.preventDefault()
  cycleTheme()
}
```

```js
function cycleTheme() {
  const themes = ['light', 'system', 'dark']
  const current = getTheme()
  const next = themes[(themes.indexOf(current) + 1) % themes.length]
  setTheme(next)
}
```

**Step 2: Add tooltip showing current theme and shortcut**

```svelte
<button onclick={cycleTheme} class="btn-icon" title="Theme: {getTheme()} (Ctrl+Shift+L)">
  <!-- sun/moon/monitor icon based on current theme -->
</button>
```

**Step 3: Commit**

```bash
git add web/src/App.svelte
git commit -m "feat: add Ctrl+Shift+L keyboard shortcut for theme cycling"
```

---

### Task 5: Improve action button tooltips on mobile

**Files:**
- Modify: `web/src/pages/Jobs.svelte`
- Modify: `web/src/pages/Storage.svelte`
- Modify: `web/src/pages/Replication.svelte`

**Step 1: Add aria-labels and title attributes to all icon-only buttons**

Audit all icon-only buttons across Jobs, Storage, and Replication pages. Ensure each has:
- `title="Action name"` for hover tooltip
- `aria-label="Action name"` for screen readers

Example in Jobs.svelte:

```svelte
<button onclick={() => runNow(job)} class="btn-icon" title="Run backup now" aria-label="Run backup now">
```

**Step 2: On mobile, show text labels for primary actions**

Use responsive classes to show text on small screens for critical actions:

```svelte
<button class="btn btn-sm btn-primary">
  <svg class="w-4 h-4"><!-- icon --></svg>
  <span class="hidden sm:inline">Run Now</span>
</button>
```

Actually, on mobile icon-only is better for space. Just ensure tooltips are accessible.

**Step 3: Commit**

```bash
git add web/src/pages/Jobs.svelte web/src/pages/Storage.svelte web/src/pages/Replication.svelte
git commit -m "fix: add aria-labels and tooltips to all icon-only action buttons"
```

---

### Task 6: Phase 4 final verification

**Files:** None (verification only)

**Verification checklist:**
- [ ] Mobile bottom nav: 5 items show on mobile, hidden on desktop
- [ ] Bottom nav: active state highlights correctly
- [ ] Bottom nav: doesn't overlap page content (padding applied)
- [ ] Pull-to-refresh: works on mobile Dashboard
- [ ] Pull-to-refresh: works on mobile History
- [ ] Pull-to-refresh: spinner shows during refresh
- [ ] Job duplication: opens wizard at Review step with cloned data
- [ ] Job duplication: name shows "(copy)" suffix
- [ ] Job duplication: new job is disabled by default
- [ ] Theme shortcut: Ctrl+Shift+L cycles through themes
- [ ] Theme toggle: tooltip shows current theme
- [ ] Accessibility: all icon buttons have aria-labels
- [ ] Dark mode: all changes render correctly
- [ ] Full build passes: `cd web && npm run build`
- [ ] Go tests pass: `go test ./... -short`
- [ ] Lint clean: `make lint`

```bash
cd web && npm run build && cd .. && go test ./... -short && make lint
```

**Final commit:**

```bash
git add -A && git commit -m "fix: phase 4 final polish adjustments"
```
