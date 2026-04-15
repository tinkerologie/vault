<script>
  import { getRoute, navigate } from './lib/router.svelte.js'
  import { connectWs, getWsStatus } from './lib/ws.svelte.js'
  import { initTheme, getMode, setMode, getIsDark, getIsThemed } from './lib/theme.svelte.js'
  import { api, setReplicaMode } from './lib/api.js'
  import { getLiveMode, isProxyMode } from './lib/runtime-config.js'
  import { onMount } from 'svelte'

  import Dashboard from './pages/Dashboard.svelte'
  import Jobs from './pages/Jobs.svelte'
  import Storage from './pages/Storage.svelte'
  import History from './pages/History.svelte'
  import Restore from './pages/Restore.svelte'
  import Logs from './pages/Logs.svelte'
  import Settings from './pages/Settings.svelte'
  import Replication from './pages/Replication.svelte'
  import Recovery from './pages/Recovery.svelte'
  import Spinner from './components/Spinner.svelte'
  import CommandPalette from './components/CommandPalette.svelte'

  let mobileMenuOpen = $state(false)
  let showCommandPalette = $state(false)

  function handleGlobalKeydown(e) {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault()
      showCommandPalette = !showCommandPalette
    }
    if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'L') {
      e.preventDefault()
      cycleTheme()
    }
  }

  const allNav = [
    { path: '/', label: 'Dashboard', icon: 'M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6' },
    { path: '/jobs', label: 'Jobs', icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4', daemonOnly: true },
    { path: '/storage', label: 'Storage', icon: 'M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4' },
    { path: '/history', label: 'History', icon: 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z' },
    { path: '/restore', label: 'Restore', icon: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15', daemonOnly: true },
    { path: '/logs', label: 'Logs', icon: 'M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z' },
    { path: '/replication', label: 'Replication', icon: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15' },
    { path: '/recovery', label: 'Recovery', icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z', daemonOnly: true },
    { path: '/settings', label: 'Settings', icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z' },
  ]

  // Filter nav items based on mode — hide daemon-only pages in replica mode.
  let nav = $derived(allNav.filter(item => !replicaMode || !item.daemonOnly))

  let ready = $state(false)
  let replicaMode = $state(false)
  const liveMode = getLiveMode()
  const proxyMode = isProxyMode()

  onMount(async () => {
    initTheme()
    // Detect replica mode from health endpoint.
    try {
      const health = await api.health()
      if (health.mode === 'replica') {
        setReplicaMode(true)
        replicaMode = true
      }
    } catch { /* ignore — default to daemon mode */ }
    connectWs()
    ready = true
  })

  function isActive(path) {
    const route = getRoute()
    if (path === '/') return route === '/'
    return route.startsWith(path)
  }

  const iconFor = (path) => allNav.find(n => n.path === path)?.icon
  const moreIcon = 'M5 12h.01M12 12h.01M19 12h.01M6 12a1 1 0 11-2 0 1 1 0 012 0zm7 0a1 1 0 11-2 0 1 1 0 012 0zm7 0a1 1 0 11-2 0 1 1 0 012 0z'
  const daemonMobileNav = [
    { path: '/', label: 'Home', icon: iconFor('/') },
    { path: '/jobs', label: 'Jobs', icon: iconFor('/jobs') },
    { path: '/history', label: 'History', icon: iconFor('/history') },
    { path: '/restore', label: 'Restore', icon: iconFor('/restore') },
    { path: '/settings', label: 'More', icon: moreIcon },
  ]
  const replicaMobileNav = [
    { path: '/', label: 'Home', icon: iconFor('/') },
    { path: '/replication', label: 'Replication', icon: iconFor('/replication') },
    { path: '/history', label: 'History', icon: iconFor('/history') },
    { path: '/logs', label: 'Logs', icon: iconFor('/logs') },
    { path: '/settings', label: 'More', icon: moreIcon },
  ]
  let mobileNav = $derived(replicaMode ? replicaMobileNav : daemonMobileNav)

  function go(path) {
    navigate(path)
    mobileMenuOpen = false
  }

  function cycleTheme() {
    /** @type {Array<'light'|'system'|'dark'>} */
    const modes = ['light', 'system', 'dark']
    const current = getMode()
    const next = modes[(modes.indexOf(current) + 1) % modes.length]
    setMode(next)
  }
</script>

<svelte:window onkeydown={handleGlobalKeydown} />

<CommandPalette bind:show={showCommandPalette} onclose={() => showCommandPalette = false} />

<a href="#main-content" class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-vault focus:text-white focus:rounded-lg focus:text-sm focus:font-medium">Skip to main content</a>

<div class="flex h-screen bg-surface">
  {#if !ready}
    <div class="flex-1 flex items-center justify-center"><Spinner text="Connecting..." /></div>
  {:else}
  <!-- Sidebar -->
  <aside class="hidden lg:flex lg:flex-col w-64 bg-surface-2 border-r border-border shrink-0">
    <!-- Brand -->
    <div class="flex items-center gap-3 px-6 py-5 border-b border-border">
      {#if getIsThemed()}
        <span class="text-text font-mono text-sm tracking-widest">┌─ VAULT ─┐</span>
      {:else}
        <div class="w-8 h-8 bg-vault rounded-lg flex items-center justify-center shrink-0">
          <svg class="w-5 h-5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
        </div>
        <div>
          <span class="text-lg font-bold text-text tracking-tight">VAULT</span>
          <span class="text-xs text-text-dim block -mt-0.5">{replicaMode ? 'Replica' : 'Backup Manager'}</span>
        </div>
      {/if}
    </div>

    <!-- Nav links -->
    <nav class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
      {#each nav as item (item.path)}
        <button onclick={() => go(item.path)}
          aria-current={isActive(item.path) ? 'page' : undefined}
          class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all text-left
            {isActive(item.path) ? 'bg-vault/10 text-vault nav-active' : 'text-text-muted hover:text-text hover:bg-surface-3'}">
          <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={item.icon}/></svg>
          {item.label}
        </button>
      {/each}
    </nav>

    <!-- Theme toggle footer -->
    <div class="px-6 py-4 border-t border-border flex items-center justify-end">
      <button
        onclick={cycleTheme}
        class="p-1.5 rounded-lg text-text-dim hover:text-text hover:bg-surface-3 transition-colors"
        title="Mode: {getMode()} (Ctrl+Shift+L)"
        aria-label="Toggle theme"
      >
        {#if getIsThemed()}
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/></svg>
        {:else if getIsDark()}
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"/></svg>
        {:else}
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"/></svg>
        {/if}
      </button>
    </div>
  </aside>

  <!-- Mobile header -->
  <div class="lg:hidden fixed top-0 left-0 right-0 z-40 bg-surface-2 border-b border-border">
    <div class="flex items-center justify-between px-4 py-3">
      <div class="flex items-center gap-2">
        <div class="w-7 h-7 bg-vault rounded-lg flex items-center justify-center">
          <svg class="w-4 h-4 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
        </div>
        <span class="text-base font-bold text-text">VAULT</span>
      </div>
      <button onclick={() => mobileMenuOpen = !mobileMenuOpen} class="text-text-muted p-2 -mr-2 min-w-[44px] min-h-[44px] flex items-center justify-center" aria-label="Toggle menu">
        <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={mobileMenuOpen ? 'M6 18L18 6M6 6l12 12' : 'M4 6h16M4 12h16M4 18h16'}/></svg>
      </button>
    </div>
    {#if mobileMenuOpen}
      <nav class="px-3 pb-3 space-y-1 bg-surface-2 border-t border-border animate-fade-up">
        {#each nav as item (item.path)}
          <button onclick={() => go(item.path)}
            class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium text-left
              {isActive(item.path) ? 'bg-vault/10 text-vault nav-active' : 'text-text-muted'}">
            <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={item.icon}/></svg>
            {item.label}
          </button>
        {/each}
      </nav>
    {/if}
  </div>

  <!-- Main content -->
  <main id="main-content" class="flex-1 overflow-y-auto lg:pt-0 pt-14 pb-16 lg:pb-0">
    {#key getRoute()}
    <div class="max-w-6xl mx-auto px-4 sm:px-6 lg:px-8 py-6 animate-fade-up">
      {#if getRoute() === '/'}
        <Dashboard />
      {:else if getRoute() === '/jobs'}
        <Jobs />
      {:else if getRoute() === '/storage'}
        <Storage />
      {:else if getRoute() === '/history'}
        <History />
      {:else if getRoute() === '/restore'}
        <Restore />
      {:else if getRoute() === '/logs'}
        <Logs />
      {:else if getRoute() === '/replication'}
        <Replication />
      {:else if getRoute() === '/recovery'}
        <Recovery />
      {:else if getRoute() === '/settings'}
        <Settings />
      {:else}
        <Dashboard />
      {/if}
    </div>
    {/key}
  </main>

  <!-- Mobile bottom navigation -->
  <nav class="fixed bottom-0 left-0 right-0 bg-surface-2 border-t border-border flex justify-around py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] z-40 lg:hidden" aria-label="Mobile navigation">
    {#each mobileNav as item (item.path)}
      <button
        onclick={() => go(item.path)}
        aria-current={isActive(item.path) ? 'page' : undefined}
        class="flex flex-col items-center gap-0.5 px-3 py-2 min-w-[44px] min-h-[44px] text-xs transition-colors
          {isActive(item.path) ? 'text-vault' : 'text-text-muted'}"
        aria-label={item.label}
      >
        <div class="relative">
          <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" d={item.icon} />
          </svg>
          {#if isActive(item.path)}
            <span class="absolute -bottom-1.5 left-1/2 -translate-x-1/2 w-1 h-1 rounded-full bg-vault"></span>
          {/if}
        </div>
        <span>{item.label}</span>
      </button>
    {/each}
  </nav>

  {/if}
</div>
