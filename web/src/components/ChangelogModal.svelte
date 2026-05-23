<script>
  import Modal from './Modal.svelte'
  import { SvelteSet } from 'svelte/reactivity'

  /** @type {{
   *   show: boolean,
   *   onclose: () => void,
   *   releases: Array<{ version: string, date?: string, sections: Record<string, string[]> }>,
   *   currentVersion?: string,
   *   latestTag?: string,
   * }} */
  let {
    show = false,
    onclose = () => {},
    releases = [],
    currentVersion = '',
    latestTag = '',
  } = $props()

  // Which release rows are expanded. The first (latest) release auto-expands
  // the first time we see a non-empty list. `autoExpandedFor` is a plain
  // (non-reactive) variable used only to gate the one-shot side effect.
  const expanded = new SvelteSet()
  let autoExpandedFor = ''

  $effect(() => {
    if (releases.length > 0 && releases[0].version !== autoExpandedFor) {
      autoExpandedFor = releases[0].version
      expanded.add(releases[0].version)
    }
  })

  /** @param {string} version */
  function toggle(version) {
    if (expanded.has(version)) expanded.delete(version)
    else expanded.add(version)
  }

  // Strip a leading "v" so a daemon-reported "2026.05.02" matches a
  // GitHub tag like "v2026.05.02" when picking the Current/Latest pill.
  /** @param {string} v */
  function norm(v) {
    if (!v) return ''
    return String(v).replace(/^v/i, '')
  }

  // Render the inline-markdown subset that actually appears in
  // CHANGELOG bullets: **bold**, `code`, and *italic*. Source is the
  // embedded CHANGELOG.md (trusted, ships in the binary) but we still
  // HTML-escape first to defend against any future authoring slip.
  /** @param {string} text */
  function renderInline(text) {
    const escaped = String(text)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
    return escaped
      .replace(/`([^`]+)`/g, '<code class="px-1 py-0.5 rounded bg-surface text-text font-mono text-xs">$1</code>')
      .replace(/\*\*([^*]+)\*\*/g, '<strong class="font-semibold text-text">$1</strong>')
      .replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, '$1<em>$2</em>')
  }

  /** @param {string | undefined} dateStr */
  function daysAgo(dateStr) {
    if (!dateStr) return ''
    const d = new Date(dateStr)
    if (Number.isNaN(d.getTime())) return ''
    const diff = Math.max(0, Math.round((Date.now() - d.getTime()) / 86400000))
    if (diff === 0) return 'today'
    if (diff === 1) return '1 day ago'
    return `${diff} days ago`
  }
</script>

<Modal {show} {onclose} title="Changelog" size="lg">
  <div class="max-h-[70vh] overflow-y-auto pr-2 space-y-2">
    {#if releases.length === 0}
      <p class="text-text-muted text-sm">No release history available.</p>
    {/if}
    {#each releases as r, i (r.version)}
      {@const isExpanded = expanded.has(r.version)}
      {@const isLatest = norm(r.version) === norm(latestTag) || i === 0}
      {@const isCurrent = norm(r.version) === norm(currentVersion)}
      <div class="border border-border rounded-lg overflow-hidden">
        <button
          type="button"
          onclick={() => toggle(r.version)}
          class="w-full flex items-center gap-3 px-3 py-2 text-left bg-surface hover:bg-surface-2 transition-colors"
        >
          <span class="text-text-muted text-xs w-5">{isExpanded ? '▾' : '▸'}</span>
          <span class="font-semibold text-text">{r.version}</span>
          {#if isLatest}
            <span class="text-xs px-2 py-0.5 rounded bg-emerald-500/15 text-emerald-400">Latest</span>
          {/if}
          {#if isCurrent}
            <span class="text-xs px-2 py-0.5 rounded bg-vault/15 text-vault">Current</span>
          {/if}
          <span class="ml-auto text-text-muted text-xs">{daysAgo(r.date)}</span>
        </button>
        {#if isExpanded}
          <div class="px-4 py-3 space-y-3 bg-surface-2">
            {#each Object.entries(r.sections || {}) as [section, bullets] (section)}
              <div>
                <h4 class="text-sm font-semibold text-text mb-1">{section}</h4>
                <ul class="text-sm text-text-muted space-y-1 list-disc pl-5">
                  {#each bullets as b, bi (bi)}
                    <li>{@html renderInline(b)}</li>
                  {/each}
                </ul>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>
</Modal>
