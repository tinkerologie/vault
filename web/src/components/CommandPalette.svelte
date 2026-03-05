<script>
  import { api } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'

  let { show = $bindable(false), onclose = () => {} } = $props()

  let query = $state('')
  let selectedIndex = $state(0)
  let inputEl = $state(null)
  let jobs = $state([])
  let storages = $state([])
  let loaded = $state(false)

  // Static navigation commands
  const navCommands = [
    { id: 'nav-dashboard', label: 'Go to Dashboard', icon: 'M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6', action: () => navigate('/') },
    { id: 'nav-jobs', label: 'Go to Jobs', icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2', action: () => navigate('/jobs') },
    { id: 'nav-storage', label: 'Go to Storage', icon: 'M4 7v10c0 2.21 3.582 4 8 4s8-1.79 8-4V7M4 7c0 2.21 3.582 4 8 4s8-1.79 8-4M4 7c0-2.21 3.582-4 8-4s8 1.79 8 4', action: () => navigate('/storage') },
    { id: 'nav-history', label: 'Go to History', icon: 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z', action: () => navigate('/history') },
    { id: 'nav-restore', label: 'Go to Restore', icon: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15', action: () => navigate('/restore') },
    { id: 'nav-logs', label: 'Go to Logs', icon: 'M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z', action: () => navigate('/logs') },
    { id: 'nav-settings', label: 'Go to Settings', icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z', action: () => navigate('/settings') },
  ]

  // Load dynamic data when palette opens
  $effect(() => {
    if (show && !loaded) {
      loadData()
    }
    if (show) {
      query = ''
      selectedIndex = 0
      requestAnimationFrame(() => inputEl?.focus())
    }
  })

  async function loadData() {
    try {
      const [j, s] = await Promise.all([
        api.listJobs().catch(() => []),
        api.listStorage().catch(() => []),
      ])
      jobs = j || []
      storages = s || []
      loaded = true
    } catch { /* ignore */ }
  }

  let dynamicCommands = $derived.by(() => {
    const cmds = []
    for (const job of jobs) {
      cmds.push({
        id: `job-${job.id}`,
        label: `Run Job: ${job.name}`,
        icon: 'M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z',
        category: 'Jobs',
        action: async () => {
          try { await api.runJob(job.id) } catch { /* ignore */ }
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
          try { await api.testStorage(s.id) } catch { /* ignore */ }
        },
      })
    }
    return cmds
  })

  let allCommands = $derived([...navCommands, ...dynamicCommands])

  let filteredCommands = $derived.by(() => {
    if (!query.trim()) return allCommands
    const q = query.toLowerCase()
    return allCommands.filter(c => c.label.toLowerCase().includes(q))
  })

  // Reset selection when results change
  $effect(() => {
    filteredCommands.length
    selectedIndex = 0
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
    } else if (e.key === 'Escape') {
      onclose()
    }
  }

  function executeCommand(cmd) {
    onclose()
    cmd.action()
  }
</script>

{#if show}
  <div
    class="fixed inset-0 z-[60] flex items-start justify-center pt-[15vh] bg-black/60 backdrop-blur-sm"
    onclick={(e) => { if (e.target === e.currentTarget) onclose() }}
    onkeydown={handleKeydown}
    role="dialog"
    aria-modal="true"
    aria-label="Command palette"
    tabindex="-1"
  >
    <div class="bg-surface-2 border border-border rounded-xl shadow-2xl w-full max-w-lg mx-4 overflow-hidden">
      <!-- Search input -->
      <div class="flex items-center gap-3 px-4 py-3 border-b border-border">
        <svg class="w-5 h-5 text-text-dim shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
        </svg>
        <input
          bind:this={inputEl}
          type="text"
          bind:value={query}
          placeholder="Type a command or search..."
          class="flex-1 bg-transparent text-sm text-text placeholder:text-text-dim focus:outline-none"
        />
        <kbd class="text-xs text-text-dim bg-surface-3 px-1.5 py-0.5 rounded border border-border font-mono">Esc</kbd>
      </div>

      <!-- Results -->
      <div class="max-h-80 overflow-y-auto py-2">
        {#if filteredCommands.length === 0}
          <div class="px-4 py-6 text-center">
            <p class="text-sm text-text-muted">No matching commands</p>
          </div>
        {:else}
          {#each filteredCommands as cmd, i (cmd.id)}
            <button
              type="button"
              class="w-full flex items-center gap-3 px-4 py-2.5 text-left transition-colors
                {i === selectedIndex ? 'bg-vault/10 text-text' : 'text-text-muted hover:bg-surface-3'}"
              onmouseenter={() => selectedIndex = i}
              onclick={() => executeCommand(cmd)}
            >
              <svg class="w-4 h-4 shrink-0 {i === selectedIndex ? 'text-vault' : 'text-text-dim'}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={cmd.icon}/>
              </svg>
              <span class="text-sm flex-1 truncate">{cmd.label}</span>
              {#if cmd.category}
                <span class="text-xs text-text-dim">{cmd.category}</span>
              {/if}
              {#if i === selectedIndex}
                <kbd class="text-xs text-text-dim bg-surface-3 px-1.5 py-0.5 rounded border border-border font-mono">↵</kbd>
              {/if}
            </button>
          {/each}
        {/if}
      </div>

      <!-- Footer -->
      <div class="px-4 py-2 border-t border-border flex items-center gap-4 text-xs text-text-dim">
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">↑↓</kbd> Navigate</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">↵</kbd> Execute</span>
        <span class="flex items-center gap-1"><kbd class="bg-surface-3 px-1 py-0.5 rounded border border-border font-mono">Esc</kbd> Close</span>
      </div>
    </div>
  </div>
{/if}
