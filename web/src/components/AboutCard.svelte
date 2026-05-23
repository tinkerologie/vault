<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import ChangelogModal from './ChangelogModal.svelte'

  /** @type {Array<{ version: string, date?: string, sections: Record<string, string[]> }>} */
  let releases = $state([])
  /** @type {{ tag: string, published_at: string, url: string } | null} */
  let latest = $state(null)
  let currentVersion = $state('dev')
  let modalOpen = $state(false)
  let loading = $state(true)

  const githubReleasesURL = 'https://github.com/ruaan-deysel/vault/releases'

  onMount(async () => {
    try {
      const [health, c, l] = await Promise.all([
        api.health().catch(() => ({})),
        api.getChangelog().catch(() => []),
        api.getLatestRelease().catch(() => null),
      ])
      currentVersion = (health && health.version) || 'dev'
      releases = c
      latest = l
    } finally {
      loading = false
    }
  })

  const status = $derived.by(() => {
    if (currentVersion === 'dev') {
      return { kind: 'dev', label: 'Development build', note: '' }
    }
    if (latest === null) {
      return { kind: 'unknown', label: '', note: 'Update status unknown.' }
    }
    if (latest.tag === currentVersion) {
      return { kind: 'ok', label: 'Up to date', note: '' }
    }
    return { kind: 'update', label: 'Update available', note: `Latest: ${latest.tag}` }
  })

  const releasedNote = $derived.by(() => {
    if (!latest) return ''
    const d = new Date(latest.published_at)
    if (Number.isNaN(d.getTime())) return ''
    const diff = Math.max(0, Math.round((Date.now() - d.getTime()) / 86400000))
    if (diff === 0) return 'Released today.'
    if (diff === 1) return 'Released 1 day ago.'
    return `Released ${diff} days ago.`
  })

  const badgeClass = $derived(
    status.kind === 'ok'
      ? 'bg-emerald-500/15 text-emerald-400'
      : status.kind === 'update'
        ? 'bg-orange-500/15 text-orange-400'
        : 'bg-surface text-text-muted',
  )
</script>

<div class="bg-surface-2 border border-border rounded-xl p-5">
  <h3 class="text-base font-semibold text-text mb-1">About</h3>
  <div class="flex items-center gap-3 flex-wrap">
    <span class="text-sm font-medium text-text">Vault <span class="font-mono">{currentVersion}</span></span>
    {#if status.label}
      <span class="text-xs px-2 py-0.5 rounded {badgeClass}">{status.label}</span>
    {/if}
    {#if status.note}
      <span class="text-xs text-text-muted">{status.note}</span>
    {/if}
  </div>
  {#if releasedNote}
    <p class="text-xs text-text-muted mt-1">{releasedNote}</p>
  {/if}
  <div class="flex items-center gap-2 mt-3">
    <button
      type="button"
      onclick={() => (modalOpen = true)}
      disabled={loading || releases.length === 0}
      class="px-3 py-1.5 text-sm font-medium text-white bg-vault rounded-lg hover:bg-vault-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
    >
      View Changelog
    </button>
    <a
      href={githubReleasesURL}
      target="_blank"
      rel="noopener noreferrer"
      class="px-3 py-1.5 text-sm font-medium text-text border border-border rounded-lg hover:bg-surface transition-colors"
    >
      View on GitHub ↗
    </a>
  </div>
</div>

<ChangelogModal
  show={modalOpen}
  onclose={() => (modalOpen = false)}
  {releases}
  {currentVersion}
  latestTag={latest?.tag ?? ''}
/>
