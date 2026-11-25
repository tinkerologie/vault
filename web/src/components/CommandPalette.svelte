<script>
  import { api, isReplicaMode } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'
  import { formatDate } from '../lib/utils.js'
  import { tick, untrack } from 'svelte'
  import Modal from './Modal.svelte'
  import Toast from './Toast.svelte'
  import { shortcutModifier } from '../lib/shortcuts.js'

  /** @typedef {{ path: string, label: string, icon: string, external?: boolean, href?: string }} Page */
  /** @type {{ show?: boolean, onclose?: () => void, pages?: Page[] }} */
  let { show = $bindable(false), onclose = () => {}, pages = [] } = $props()

  const uid = $props.id()
  const listboxId = `${uid}-listbox`
  const optionId = (i) => `${uid}-option-${i}`
  const mod = shortcutModifier()

  let query = $state('')
  let selectedIndex = $state(0)
  let jobs = $state([])
  let storages = $state([])
  let containers = $state([])
  let vms = $state([])
  let folders = $state([])
  /** @type {Array<{ jobId: number, jobName: string, date: string }>} */
  let latestPoints = $state([])
  let loaded = $state(false)
  let toastMessage = $state('')
  let toastType = $state('info')
  let toastKey = $state(0)

  function showToast(message, type = 'info') {
    toastMessage = message
    toastType = type
    toastKey++
  }

  const ICON_CONTAINER = 'M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4'
  const ICON_VM = 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z'
  const ICON_FOLDER = 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z'
  const ICON_CLOCK = 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z'

  // Page commands come from the shell's own navigation (#691), so the palette
  // offers exactly the pages the sidebar shows, with the same icons, and
  // respects replica mode and feature flags the same way.
  let navCommands = $derived(pages.map((page) => ({
    id: `nav-${page.path.replace(/^\//, '') || 'dashboard'}`,
    label: page.external ? `Open ${page.label}` : `Go to ${page.label}`,
    category: 'Pages',
    icon: page.icon,
    action: page.external
      ? () => window.open(page.href, '_blank', 'noopener')
      : () => navigate(page.path),
  })))

  // Shown as written: the type is a word, and a time keeps the locale's own
  // "pm" or "PM" (#715).
  const TYPE_LABELS = { container: 'Container', vm: 'VM', folder: 'Folder' }

  // Load dynamic data when palette opens
  $effect(() => {
    if (show && !loaded) {
      loadData()
    }
  })

  // Apart from the load above: it reads `loaded`, and clearing the query when the
  // load finishes would wipe what was typed in the meantime.
  $effect(() => {
    if (show) {
      query = ''
      selectedIndex = 0
    }
  })

  async function loadData() {
    try {
      const [j, s, c, v, f] = await Promise.all([
        api.listJobs().catch((err) => { console.error('CommandPalette: listJobs failed', err); return [] }),
        api.listStorage().catch((err) => { console.error('CommandPalette: listStorage failed', err); return [] }),
        api.listContainers().catch(() => []),
        api.listVMs().catch(() => []),
        api.listFolders().catch(() => []),
      ])
      jobs = j || []
      storages = s || []
      // /containers, /vms, /folders return { available, items } — not bare arrays.
      containers = c?.items || []
      vms = v?.items || []
      folders = f?.items || []
      loaded = true
      // Restore-point index: the single most-recent point per job (run once and
      // cached). getRestorePoints returns points newest-first, so [0] is the
      // latest. Capped at a small concurrency so opening the palette on an
      // install with many jobs can't burst one request per job at once.
      const points = []
      const CONCURRENCY = 4
      for (let i = 0; i < jobs.length; i += CONCURRENCY) {
        const chunk = await Promise.all(
          jobs.slice(i, i + CONCURRENCY).map(job =>
            api.getRestorePoints(job.id)
              .then(rps => (rps && rps[0]) ? { jobId: job.id, jobName: job.name, date: rps[0].created_at } : null)
              .catch(() => null)
          )
        )
        points.push(...chunk)
      }
      latestPoints = points.filter(Boolean)
    } catch (err) { console.error('CommandPalette: loadData failed', err) }
  }

  let dynamicCommands = $derived.by(() => {
    const cmds = []
    // Backup items — jump straight to the item's restore view (issue #210 / E9).
    const items = [
      ...containers.map(c => ({ name: c.name, type: 'container', icon: ICON_CONTAINER })),
      ...vms.map(v => ({ name: v.name, type: 'vm', icon: ICON_VM })),
      ...folders.map(f => ({ name: f.name, type: 'folder', icon: ICON_FOLDER })),
    ]
    for (const it of items) {
      if (!it.name) continue
      cmds.push({
        id: `item-${it.type}-${it.name}`,
        label: it.name,
        meta: TYPE_LABELS[it.type] ?? it.type,
        icon: it.icon,
        category: 'Items',
        action: () => navigate(`/restore?type=${it.type}&name=${encodeURIComponent(it.name)}`),
      })
    }
    // Latest restore point per job — open the job's restore view.
    for (const p of latestPoints) {
      cmds.push({
        id: `rp-${p.jobId}`,
        label: `Restore: ${p.jobName}`,
        meta: p.date ? formatDate(p.date) : 'latest',
        icon: ICON_CLOCK,
        category: 'Restore Points',
        action: () => navigate(`/restore?job=${p.jobId}`),
      })
    }
    for (const job of jobs) {
      cmds.push({
        id: `job-${job.id}`,
        label: `Run Job: ${job.name}`,
        icon: 'M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z',
        category: 'Jobs',
        action: async () => {
          try {
            await api.runJob(job.id)
            showToast(`Started job: ${job.name}`, 'success')
          } catch (err) {
            console.error('CommandPalette: runJob failed', err)
            showToast(`Failed to start ${job.name}: ${err?.message || err}`, 'error')
          }
        },
      })
    }
    for (const s of storages) {
      cmds.push({
        id: `storage-${s.id}`,
        label: `Test Storage: ${s.name}`,
        icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z',
        category: 'Storage',
        action: async () => {
          try {
            const result = await api.testStorage(s.id)
            if (result?.success) {
              showToast(`${s.name}: connection successful`, 'success')
            } else {
              showToast(`${s.name}: ${result?.error || 'connection failed'}`, 'error')
            }
          } catch (err) {
            console.error('CommandPalette: testStorage failed', err)
            showToast(`${s.name}: ${err?.message || err}`, 'error')
          }
        },
      })
    }
    return cmds
  })

  let allCommands = $derived.by(() => {
    const cmds = [...navCommands, ...dynamicCommands]
    if (!isReplicaMode()) return cmds
    // Replica mode: the shell already leaves daemon-only pages out of `pages`.
    // Hide item and restore-point deep-links (they go to /restore) and write
    // actions (Run Job). Read-only entries (Test Storage, pages) stay.
    return cmds.filter(c =>
      c.category !== 'Items' && c.category !== 'Restore Points' && c.category !== 'Jobs'
    )
  })

  let filteredCommands = $derived.by(() => {
    if (!query.trim()) return allCommands
    const q = query.toLowerCase()
    return allCommands.filter(c =>
      c.label.toLowerCase().includes(q) || (c.meta && c.meta.toLowerCase().includes(q))
    )
  })

  // Contiguous runs of one category, each rendered as a labelled group.
  let groups = $derived.by(() => {
    /** @type {Array<{ category: string, items: Array<{ cmd: any, index: number }> }>} */
    const out = []
    filteredCommands.forEach((cmd, index) => {
      const last = out.at(-1)
      if (last && last.category === cmd.category) last.items.push({ cmd, index })
      else out.push({ category: cmd.category, items: [{ cmd, index }] })
    })
    return out
  })

  // A new search starts at the top. Items that finish loading are added after
  // the pages, so they leave the active option where it was; a shorter list
  // only pulls the selection back inside it.
  $effect(() => {
    query
    selectedIndex = 0
  })
  $effect(() => {
    const last = Math.max(0, filteredCommands.length - 1)
    if (untrack(() => selectedIndex) > last) selectedIndex = last
  })

  // The listbox scrolls on its own, so keep the active option in view.
  $effect(() => {
    if (!show || filteredCommands.length === 0) return
    const id = optionId(selectedIndex)
    tick().then(() => document.getElementById(id)?.scrollIntoView?.({ block: 'nearest' }))
  })

  // How many results match, for screen readers. Waits for typing to pause so
  // each keystroke doesn't interrupt the last announcement.
  let resultCount = $state('')
  $effect(() => {
    if (!show) {
      resultCount = ''
      return
    }
    const n = filteredCommands.length
    const text = n === 0 ? 'No results' : n === 1 ? '1 result' : `${n} results`
    const timer = setTimeout(() => { resultCount = text }, 400)
    return () => clearTimeout(timer)
  })

  function handleKeydown(e) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      selectedIndex = Math.min(selectedIndex + 1, filteredCommands.length - 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      selectedIndex = Math.max(selectedIndex - 1, 0)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (filteredCommands[selectedIndex]) {
        executeCommand(filteredCommands[selectedIndex])
      }
    }
  }

  function executeCommand(cmd) {
    onclose()
    cmd.action()
  }
</script>

<Modal {show} label="Command palette" placement="top" {onclose}>
    <div role="presentation" onkeydown={handleKeydown}>
      <!-- Search input -->
      <div class="flex items-center gap-3 px-4 py-3 border-b border-border">
        <svg aria-hidden="true" class="w-5 h-5 text-text-dim shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
        </svg>
        <input
          data-autofocus
          aria-label="Search commands"
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls={listboxId}
          aria-autocomplete="list"
          aria-activedescendant={filteredCommands.length > 0 ? optionId(selectedIndex) : undefined}
          autocomplete="off"
          spellcheck="false"
          bind:value={query}
          placeholder="Type a command or search..."
          class="flex-1 bg-transparent text-sm text-text placeholder:text-text-muted focus:outline-none"
        />
        <kbd class="text-xs text-text-muted bg-surface-3 px-1.5 py-0.5 rounded border border-border font-mono">Esc</kbd>
      </div>
      <div role="status" class="sr-only">{resultCount}</div>

      <!-- Results -->
      <div id={listboxId} role="listbox" aria-label="Commands" class="max-h-80 overflow-y-auto py-2">
        {#if filteredCommands.length === 0}
          <div class="px-4 py-6 text-center">
            <p class="text-sm text-text-muted">No matching commands</p>
          </div>
        {:else}
          {#each groups as group, g (`${g}-${group.category}`)}
            <div role="group" aria-labelledby="{uid}-group-{g}">
              <div id="{uid}-group-{g}" role="presentation" class="px-4 pt-2 pb-1 text-xs font-semibold uppercase tracking-wider text-text-dim select-none">{group.category}</div>
              {#each group.items as { cmd, index } (cmd.id)}
                <!-- Focus stays in the input (aria-activedescendant); the pointer can still pick an option. -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div
                  id={optionId(index)}
                  role="option"
                  tabindex="-1"
                  aria-selected={index === selectedIndex}
                  class="w-full flex items-center gap-3 px-4 py-2.5 text-left cursor-pointer transition-colors
                    {index === selectedIndex ? 'bg-vault/10 text-text' : 'text-text-muted hover:bg-surface-3'}"
                  onmouseenter={() => selectedIndex = index}
                  onclick={() => executeCommand(cmd)}
                >
                  <svg aria-hidden="true" class="w-4 h-4 shrink-0 {index === selectedIndex ? 'text-vault-text' : 'text-text-dim'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={cmd.icon}/>
                  </svg>
                  <span class="text-sm flex-1 truncate">{cmd.label}</span>
                  {#if cmd.meta}
                    <span class="text-xs text-text-muted shrink-0">{cmd.meta}</span>
                  {/if}
                  {#if index === selectedIndex}
                    <kbd aria-hidden="true" class="text-xs text-text-muted bg-surface-3 px-1.5 py-0.5 rounded border border-border font-mono">↵</kbd>
                  {/if}
                </div>
              {/each}
            </div>
          {/each}
        {/if}
      </div>

      <!-- Footer -->
      <div class="px-4 py-2 border-t border-border flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-text-muted">
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">↑↓</kbd> Navigate</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">↵</kbd> Run</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">Esc</kbd> Close</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">{mod}K</kbd> Open or close</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">{mod}Shift+L</kbd> Colour mode</span>
      </div>
    </div>
</Modal>

<Toast message={toastMessage} type={toastType} key={toastKey} />
