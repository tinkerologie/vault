<script>
  import { getRoute, navigate } from './lib/router.svelte.js'
  import { pageTitle } from './lib/routes.js'
  import { createRouteChangeDetector, focusPageHeading } from './lib/focus.js'
  import { connectWs, getWsStatus } from './lib/ws.svelte.js'
  import { initTheme, getMode, setMode, getIsThemed, getIsModeLocked } from './lib/theme.svelte.js'
  import { api, setReplicaMode } from './lib/api.js'
  import { shouldRedirectRoute } from './lib/route-guard.js'
  import { groupBySection, isMobileNavActive, isMoreActive, moreNavItems } from './lib/mobile-nav.js'
  import { loadFeatureFlags, getAnomalyEnabled, getReplicationEnabled, getFeatureFlagsLoaded } from './lib/settings.svelte.js'
  import { getHostThemeMode, isEmbedded } from './lib/runtime-config.js'
  import { isAuthRequired } from './lib/auth.svelte.js'
  import { revealFrameTop } from './lib/embed.js'
  import { createConnectionBanner, isConnectionLost } from './lib/connection-banner.js'
  import { shortcutModifier } from './lib/shortcuts.js'
  import { onMount, tick } from 'svelte'

  import Dashboard from './pages/Dashboard.svelte'
  import Jobs from './pages/Jobs.svelte'
  import Storage from './pages/Storage.svelte'
  import History from './pages/History.svelte'
  import Restore from './pages/Restore.svelte'
  import Logs from './pages/Logs.svelte'
  import Settings from './pages/Settings.svelte'
  import Replication from './pages/Replication.svelte'
  import Recovery from './pages/Recovery.svelte'
  import RecoverWizard from './pages/RecoverWizard.svelte'
  import Anomalies from './pages/Anomalies.svelte'
  import Spinner from './components/Spinner.svelte'
  import ApiKeyPrompt from './components/ApiKeyPrompt.svelte'
  import CommandPalette from './components/CommandPalette.svelte'
  import MoreSheet from './components/MoreSheet.svelte'
  import EmbeddedNav from './components/EmbeddedNav.svelte'
  import { ICONS, PAGE_ICONS } from './lib/icons.js'

  // Framed by Unraid's Backups page (#526): no fixed chrome, and the document
  // takes its natural height so the frame can size to it.
  const embedded = isEmbedded()

  let moreOpen = $state(false)
  let showCommandPalette = $state(false)
  const shortcutMod = shortcutModifier()

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

  // Ordered so each `section` runs contiguously — the sidebar renders a muted
  // header before each group (Dashboard stays header-less). Reordering only
  // affects the desktop sidebar; icon lookup / isActive / mobile navs are
  // path-based and order-independent.
  const allNav = [
    { path: '/', label: 'Dashboard', icon: PAGE_ICONS['/'] },
    { path: '/jobs', label: 'Jobs', section: 'Protect', icon: PAGE_ICONS['/jobs'], daemonOnly: true },
    { path: '/storage', label: 'Storage', section: 'Protect', icon: PAGE_ICONS['/storage'] },
    { path: '/replication', label: 'Replication', section: 'Protect', icon: PAGE_ICONS['/replication'] },
    { path: '/restore', label: 'Restore', section: 'Recover', icon: PAGE_ICONS['/restore'], daemonOnly: true },
    { path: '/history', label: 'History', section: 'Recover', icon: PAGE_ICONS['/history'] },
    { path: '/recovery', label: 'Disaster Recovery', section: 'Recover', icon: PAGE_ICONS['/recovery'], daemonOnly: true },
    { path: '/logs', label: 'Logs', section: 'System', icon: PAGE_ICONS['/logs'] },
    { path: '/anomalies', label: 'Anomalies', section: 'System', icon: PAGE_ICONS['/anomalies'] },
    { path: '/settings', label: 'Settings', section: 'System', icon: PAGE_ICONS['/settings'] },
    { path: '/docs-external', label: 'Documentation', section: 'System', external: true, href: 'https://tinkerologie.github.io/vault/', icon: PAGE_ICONS['/docs-external'] },
  ]

  // Filter nav items based on mode – hide daemon-only pages in replica mode –
  // and by feature flags.
  function featureVisible(path) {
    if (path === '/anomalies') return getAnomalyEnabled()
    // Replication is core in replica mode – never hide it there.
    if (path === '/replication') return replicaMode || getReplicationEnabled()
    return true
  }
  let nav = $derived(
    allNav.filter(item => (!replicaMode || !item.daemonOnly) && featureVisible(item.path))
  )

  // Group the visible nav into contiguous sections for the desktop sidebar. A
  // header only appears when its section has ≥1 visible child, so hidden /
  // feature-flagged items never leave an empty section behind.
  let navSections = $derived(groupBySection(nav))

  // Guard direct deep-links to a hidden route – redirect to Dashboard. Wait
  // until flags are actually loaded so we don't redirect on the optimistic
  // defaults during first paint.
  $effect(() => {
    if (!getFeatureFlagsLoaded()) return
    const route = getRoute()
    if (shouldRedirectRoute({
      route,
      replicaMode,
      anomalyEnabled: getAnomalyEnabled(),
      replicationEnabled: getReplicationEnabled(),
    })) navigate('/')
  })

  let ready = $state(false)
  let replicaMode = $state(false)

  const mainContent = () => document.getElementById('main-content')

  $effect(() => { document.title = pageTitle(getRoute()) })

  // One place for every way a route can change: links, the command palette,
  // navigate() calls, back/forward and guard redirects. Initial entry and
  // query-only changes don't count, so a page load never steals focus.
  const routeChanged = createRouteChangeDetector()
  $effect(() => {
    if (!routeChanged(getRoute())) return
    moreOpen = false
    tick().then(() => {
      focusPageHeading(mainContent())
      if (embedded) revealFrameTop()
    })
  })

  // Running plugin version shown in the sidebar footer. Sourced from the same
  // /health response used for replica-mode detection; 'dev' fallback matches
  // the convention in Settings.svelte.
  let version = $state('')

  // Latest published GitHub release tag (normalized, no leading 'v'). Powers
  // the update-available indicator next to the version in the sidebar footer.
  let latestTag = $state('')

  // Watchdog threshold: how long any boot step may stall before we force the
  // shell to render anyway. Brave's blocked storage APIs and an unreachable
  // daemon (the LAN-IP workaround) could otherwise hang the SPA forever.
  const BOOT_WATCHDOG_MS = 8000

  onMount(() => {
    let watchdog = setTimeout(() => {
      ready = true
    }, BOOT_WATCHDOG_MS)

    ;(async () => {
      try {
        // initTheme is guarded internally, but keep a belt-and-braces catch so a
        // future unguarded line here can never abort the boot sequence.
        try { initTheme(embedded ? getHostThemeMode() : null) } catch { /* theme failures must never block boot */ }

        // Detect replica mode from health endpoint.
        try {
          const health = await api.health()
          version = health.version || 'dev'
          if (health.mode === 'replica') {
            setReplicaMode(true)
            replicaMode = true
          }
        } catch { /* ignore – default to daemon mode */ }

        // Update-available indicator: best-effort latest-release lookup.
        // Fire-and-forget on purpose — it must never delay or break boot.
        api.getLatestRelease()
          .then((r) => { if (r && r.tag) latestTag = String(r.tag).replace(/^v/i, '') })
          .catch(() => { /* no indicator when the release lookup fails */ })

        // connectWs is synchronous; guard it so a throwing WebSocket constructor
        // can't reject this IIFE. The ws layer owns its own retry/backoff.
        try { connectWs() } catch { /* ignore – ws layer reconnects on its own */ }

        try {
          await loadFeatureFlags()
        } catch { /* ignore – store fails open; never block the app from becoming ready */ }
      } finally {
        clearTimeout(watchdog)
        ready = true
      }
    })()

    // Clear the watchdog on unmount so a pending timeout can't fire against a
    // destroyed component (e.g. during dev HMR).
    return () => clearTimeout(watchdog)
  })

  // Surface persistent connection trouble in a small, non-blocking banner so a
  // failing daemon/WebSocket is diagnosable instead of appearing as a frozen
  // page. Driven purely by the live-connection status: 'polling', 'connecting',
  // and 'connected' are healthy/transient and stay silent. Deliberately NOT tied
  // to boot-degraded state — a browser that merely blocks storage must not be
  // reported as a daemon outage. A drop shorter than the grace period shows
  // nothing, and once shown it stays through every retry (#686).
  let connectionLost = $state(false)
  const connectionBanner = createConnectionBanner((shown) => { connectionLost = shown })
  $effect(() => {
    connectionBanner.update(ready && isConnectionLost(getWsStatus()))
  })
  $effect(() => () => connectionBanner.dispose())

  // The banner is fixed to the top, so the layout and the toasts move down by
  // its height rather than sit under it (#686). The embedded banner is in the
  // page's flow and reserves its own space.
  let bannerHeight = $state(0)
  $effect(() => {
    const offset = connectionLost && !embedded ? `${bannerHeight}px` : '0px'
    document.documentElement.style.setProperty('--vault-banner-offset', offset)
  })

  // True when the daemon version predates the latest published release.
  // Mirrors the comparison in Settings.svelte: strip a leading 'v', then plain
  // string compare (YYYY.MM.PATCH is zero-padded, so it sorts
  // chronologically). Equal or newer daemon versions show no indicator —
  // same reasoning as the 'Pre-release' state in Settings — and dev builds
  // or a failed release lookup never show one.
  const updateAvailable = $derived.by(() => {
    if (!version || !latestTag) return false
    const cur = version.replace(/^v/i, '')
    if (cur === 'dev') return false
    return cur < latestTag
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
  ]
  const replicaMobileNav = [
    { path: '/', label: 'Home', icon: iconFor('/') },
    { path: '/replication', label: 'Replication', icon: iconFor('/replication') },
    { path: '/history', label: 'History', icon: iconFor('/history') },
    { path: '/logs', label: 'Logs', icon: iconFor('/logs') },
  ]
  let mobileNav = $derived(replicaMode ? replicaMobileNav : daemonMobileNav)
  let moreItems = $derived(moreNavItems(nav, mobileNav.map((item) => item.path)))
  let moreActive = $derived(isMoreActive(moreItems, getRoute()))

  // `#main-content` would otherwise be parsed as a route and render Dashboard.
  function skipToMain(e) {
    e.preventDefault()
    focusPageHeading(mainContent())
    // The frame has no scroller of its own: the parent page has to come back.
    if (embedded) revealFrameTop()
  }

  function cycleTheme() {
    // Dracula is always dark: a hidden mode change would surprise the user later.
    if (getIsModeLocked()) return
    /** @type {Array<'light'|'system'|'dark'>} */
    const modes = ['light', 'system', 'dark']
    const current = getMode()
    const next = modes[(modes.indexOf(current) + 1) % modes.length]
    setMode(next)
  }
</script>

<svelte:window onkeydown={handleGlobalKeydown} />

{#if isAuthRequired()}
  <ApiKeyPrompt />
{/if}

<CommandPalette bind:show={showCommandPalette} pages={nav} onclose={() => showCommandPalette = false} />

<a href="#main-content" onclick={skipToMain} class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-vault focus:text-on-vault focus:rounded-lg focus:text-sm focus:font-medium">Skip to main content</a>

<!-- One live region that stays in the page, so the banner is announced once
     when it appears rather than each time it is re-inserted. -->
<div role="status" data-connection-banner class={embedded ? '' : 'fixed top-0 left-0 right-0 z-[150]'} bind:clientHeight={bannerHeight}>
  {#if connectionLost}
    <div class="bg-surface-2 border-b border-warning/40">
      <div class="flex items-center justify-center gap-3 px-4 py-2 text-sm text-text bg-warning/15">
        <svg class="w-4 h-4 shrink-0 text-warning" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.warning}/></svg>
        <span>Live connection to the Vault daemon is unavailable. Data may be out of date.</span>
        <button type="button" onclick={() => location.reload()} class="underline font-medium hover:no-underline">Reload</button>
      </div>
    </div>
  {/if}
</div>

<div class={embedded ? 'bg-surface' : 'flex h-screen bg-surface'} style:padding-top={embedded ? undefined : 'var(--vault-banner-offset, 0px)'}>
  {#if !ready}
    <div class="flex-1 flex items-center justify-center"><Spinner text="Connecting..." /></div>
  {:else}
  {#if embedded}
    <EmbeddedNav items={nav} route={getRoute()} />
  {:else}
  <!-- Sidebar -->
  <aside aria-label="Sidebar" class="hidden lg:flex lg:flex-col w-64 bg-surface-2 border-r border-border shrink-0">
    <!-- Brand -->
    <div class="flex items-center gap-3 px-6 py-5 border-b border-border">
      {#if getIsThemed()}
        <span class="text-text font-mono text-sm tracking-widest">┌─ VAULT ─┐</span>
      {:else}
        <div class="w-8 h-8 bg-vault rounded-lg flex items-center justify-center shrink-0">
          <svg class="w-5 h-5 text-on-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.shield}/></svg>
        </div>
        <div>
          <span class="text-lg font-bold text-text tracking-tight">VAULT</span>
          <span class="text-xs text-text-dim block -mt-0.5">{replicaMode ? 'Replica' : 'Backup Manager'}</span>
        </div>
      {/if}
    </div>

    <!-- Opens the command palette, and shows its shortcut (#691). -->
    <div class="px-3 pt-4">
      <button type="button" onclick={() => showCommandPalette = true} aria-keyshortcuts="Meta+K Control+K"
        class="w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-text-muted bg-surface-3 border border-border hover:text-text hover:bg-surface-4 transition-colors">
        <svg class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.search}/></svg>
        <span class="flex-1 text-left">Search</span>
        <kbd aria-hidden="true" class="text-xs font-mono px-1.5 py-0.5 rounded border border-border bg-surface-2">{shortcutMod}K</kbd>
      </button>
    </div>

    <!-- Nav links -->
    <nav aria-label="Main navigation" class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
      {#each navSections as group (group.section ?? '_top')}
        {#if group.section}
          <div class="px-3 pt-4 pb-1 text-xs font-semibold uppercase tracking-wider text-text-muted select-none">{group.section}</div>
        {/if}
        {#each group.items as item (item.path)}
          {#if item.external}
            <a href={item.href} target="_blank" rel="noopener"
              aria-label="{item.label} (opens in a new tab)"
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all text-left text-text-muted hover:text-text hover:bg-surface-3">
              <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={item.icon}/></svg>
              {item.label}
            </a>
          {:else}
            <a href="#{item.path}"
              aria-current={isActive(item.path) ? 'page' : undefined}
              class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all text-left
                {isActive(item.path) ? 'bg-vault/10 text-vault-text nav-active' : 'text-text-muted hover:text-text hover:bg-surface-3'}">
              <svg class="w-5 h-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={item.icon}/></svg>
              {item.label}
            </a>
          {/if}
        {/each}
      {/each}
    </nav>
    {#if version}
      <footer class="px-3 py-3 border-t border-border text-xs text-text-muted flex items-center justify-between gap-2">
        <span>v{version}</span>
        {#if updateAvailable}
          <a href="#/settings"
            title="Update available — latest release v{latestTag}"
            aria-label="Update available, latest release v{latestTag}. Open settings."
            class="inline-flex items-center gap-1.5 text-hue-orange hover:underline transition-colors">
            <span class="w-1.5 h-1.5 rounded-full bg-hue-orange-bg animate-pulse" aria-hidden="true"></span>
            Update available
          </a>
        {/if}
      </footer>
    {/if}
  </aside>

  <!-- Mobile header -->
  <header class="lg:hidden fixed top-0 left-0 right-0 z-40 bg-surface-2 border-b border-border" style:top="var(--vault-banner-offset, 0px)">
    <div class="flex items-center justify-between px-4 py-3">
      <div class="flex items-center gap-2">
        <div class="w-7 h-7 bg-vault rounded-lg flex items-center justify-center">
          <svg class="w-4 h-4 text-on-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.shield}/></svg>
        </div>
        <span class="text-base font-bold text-text">VAULT</span>
      </div>
    </div>
  </header>
  {/if}

  <!-- Main content -->
  <main id="main-content" tabindex="-1" class={embedded ? 'outline-none pb-20' : 'outline-none flex-1 overflow-y-auto lg:pt-0 pt-14 pb-16 lg:pb-0'}>
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
      {:else if getRoute() === '/recover'}
        <RecoverWizard />
      {:else if getRoute() === '/anomalies'}
        <Anomalies />
      {:else if getRoute() === '/settings'}
        <Settings />
      {:else}
        <Dashboard />
      {/if}
    </div>
    {/key}
  </main>

  {#if !embedded}
  <!-- Mobile bottom navigation -->
  <nav class="fixed bottom-0 left-0 right-0 bg-surface-2 border-t border-border flex justify-around py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] z-40 lg:hidden" aria-label="Mobile navigation">
    {#snippet barItem(/** @type {string} */ icon, /** @type {string} */ label, /** @type {boolean} */ active)}
      <div class="relative">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" d={icon} />
        </svg>
        {#if active}
          <span class="absolute -bottom-1.5 left-1/2 -translate-x-1/2 w-1 h-1 rounded-full bg-vault"></span>
        {/if}
      </div>
      <span>{label}</span>
    {/snippet}
    {#each mobileNav as item (item.path)}
      {@const active = isMobileNavActive(item.path, getRoute())}
      <a
        href="#{item.path}"
        aria-current={active ? 'page' : undefined}
        class="flex flex-col items-center gap-0.5 px-3 py-2 min-w-[44px] min-h-[44px] text-xs transition-colors
          {active ? 'text-vault-text' : 'text-text-muted'}"
        aria-label={item.label}
      >
        {@render barItem(item.icon, item.label, active)}
      </a>
    {/each}
    <button
      type="button"
      onclick={() => moreOpen = true}
      aria-haspopup="dialog"
      aria-expanded={moreOpen}
      aria-controls={moreOpen ? 'more-sheet' : undefined}
      aria-current={moreActive ? 'page' : undefined}
      class="flex flex-col items-center gap-0.5 px-3 py-2 min-w-[44px] min-h-[44px] text-xs transition-colors
        {moreActive ? 'text-vault-text' : 'text-text-muted'}"
    >
      {@render barItem(moreIcon, 'More', moreActive)}
    </button>
  </nav>

  <MoreSheet show={moreOpen} id="more-sheet" items={moreItems} route={getRoute()} onclose={() => moreOpen = false} />
  {/if}
  {/if}
</div>
