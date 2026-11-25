<script>
  import { announcementKey } from '../lib/backupStatus.js'
  import { ICONS } from '../lib/icons.js'

  let { status } = $props()

  const TONES = {
    success: { box: 'bg-success/10 border-success/30', icon: 'text-success', path: ICONS.shield },
    warning: { box: 'bg-warning/10 border-warning/30', icon: 'text-warning', path: ICONS.warning },
    danger: { box: 'bg-danger/10 border-danger/30', icon: 'text-danger', path: ICONS.error },
    info: { box: 'bg-info/10 border-info/30', icon: 'text-info', path: ICONS.info },
    neutral: { box: 'bg-surface-3 border-border', icon: 'text-text-muted', path: ICONS.clock },
  }
  const tone = $derived(TONES[status.tone])

  // The visible banner is not a live region, so background reloads can't make a
  // screen reader re-read it. Only a real change of verdict reaches this one; the
  // verdict present on arrival is left to be read in place, after the heading.
  let announcement = $state('')
  let lastKey = null
  $effect(() => {
    const key = announcementKey(status)
    if (key === null) return
    if (lastKey !== null && key !== lastKey) announcement = status.announcement
    lastKey = key
  })
</script>

<section aria-label="Backup status" data-state={status.state} class="border rounded-xl px-4 py-3 mb-4 flex flex-wrap items-center gap-x-4 gap-y-3 {tone.box}">
  <div class="flex items-start gap-3 min-w-0 flex-[1_1_16rem]">
    <svg aria-hidden="true" class="w-5 h-5 shrink-0 mt-0.5 {tone.icon}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={tone.path}/></svg>
    <div class="min-w-0 break-words">
      <h2 class="text-base font-semibold text-text">{status.title}</h2>
      {#if status.detail}<p class="text-sm text-text-muted mt-0.5">{status.detail}</p>{/if}
    </div>
  </div>
  {#if status.issues.length}
    <div class="flex flex-wrap gap-2">
      {#each status.issues as issue (issue.key)}
        <a href="#{issue.href}" class="inline-flex items-center min-h-[44px] sm:min-h-[36px] px-3 py-1.5 text-sm font-medium text-text bg-surface-2 border border-border hover:bg-surface-3 rounded-lg transition-colors">{issue.action}</a>
      {/each}
    </div>
  {/if}
  <p class="sr-only" role="status" aria-live="polite" aria-atomic="true" data-status-announcer>{announcement}</p>
</section>
