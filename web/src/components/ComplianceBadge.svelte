<script>
  import { SvelteSet } from 'svelte/reactivity'

  let {
    storage = [],
    jobs = [],
    replicationSources = [],
    ondismiss,
    goalSetting = '',
    onGoalChange,
  } = $props()

  // --- Inputs: copies, media diversity, offsite presence ---
  // A "copy" is a distinct storage destination with at least one enabled job
  // (plus any replication sources).
  let activeDestIds = $derived(new SvelteSet(
    jobs.filter(j => j.enabled).map(j => j.storage_dest_id)
  ))
  let activeDests = $derived(storage.filter(s => activeDestIds.has(s.id)))
  let copies = $derived(Math.min(activeDests.length + replicationSources.length, 3))
  let hasLocal = $derived(activeDests.some(s => s.type === 'local'))
  let hasOffsite = $derived(
    activeDests.some(s => s.type !== 'local') || replicationSources.length > 0
  )

  let mediaTypes = $derived.by(() => {
    const types = new SvelteSet()
    for (const s of activeDests) {
      if (s.type === 'local') types.add('disk')
      else types.add('network')
    }
    if (replicationSources.length > 0) types.add('network')
    return types
  })
  let media = $derived(mediaTypes.size)

  // --- Goals ---
  // The widget scores against the user's chosen goal rather than always the
  // full rule, so a deliberately simple setup reads as done, not failing.
  const GOAL_ORDER = ['local', 'offsite', 'full']
  const GOAL_LABELS = { local: 'Local only', offsite: 'Local + offsite', full: 'Full 3-2-1' }

  // Auto-detect default = the highest tier the current setup already meets.
  let metFull = $derived(copies >= 3 && media >= 2 && hasOffsite)
  let metOffsite = $derived(hasLocal && hasOffsite)
  let autoGoal = $derived(metFull ? 'full' : metOffsite ? 'offsite' : 'local')

  // Effective goal: the user's explicit choice, else the auto-detected default.
  let isExplicit = $derived(GOAL_ORDER.includes(goalSetting))
  let goal = $derived(isExplicit ? goalSetting : autoGoal)

  // Criteria for the selected goal (label, met, and a hint shown when unmet).
  let criteria = $derived.by(() => {
    switch (goal) {
      case 'local':
        return [
          { label: 'At least one backup copy', met: copies >= 1, hint: 'Create a backup job that writes to a storage destination.' },
        ]
      case 'offsite':
        return [
          { label: 'A local copy', met: hasLocal, hint: 'Add a local storage destination and assign it to a job.' },
          { label: 'An offsite copy', met: hasOffsite, hint: 'Add a remote destination (SFTP, SMB, NFS, WebDAV, S3) or set up replication.' },
        ]
      default: // full
        return [
          { label: '3 copies', met: copies >= 3, hint: 'Add more storage destinations to reach three copies.' },
          { label: '2 media types', met: media >= 2, hint: 'Add a remote storage so backups span more than one medium.' },
          { label: '1 offsite', met: hasOffsite, hint: 'Configure an offsite destination or set up replication.' },
        ]
    }
  })

  let metCount = $derived(criteria.filter(c => c.met).length)
  let goalMet = $derived(metCount === criteria.length)

  // Tone: green when met, red when nothing is met, amber in between.
  let tone = $derived(goalMet ? 'success' : metCount === 0 ? 'danger' : 'warning')
  let bgColor = $derived(
    tone === 'success' ? 'bg-success/10 border-success/30' :
    tone === 'warning' ? 'bg-warning/10 border-warning/30' :
    'bg-danger/10 border-danger/30'
  )
  let chipColor = $derived(
    tone === 'success' ? 'text-success' :
    tone === 'warning' ? 'text-warning' :
    'text-danger'
  )
  let statusLabel = $derived(goalMet ? 'Met' : metCount === 0 ? 'At risk' : `${metCount}/${criteria.length}`)

  let expanded = $state(false)

  function selectGoal(g) {
    if (g !== goal && onGoalChange) onGoalChange(g)
  }
</script>

{#snippet checkIcon()}<svg aria-hidden="true" class="w-3.5 h-3.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>{/snippet}
{#snippet xMark()}<svg aria-hidden="true" class="w-3.5 h-3.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>{/snippet}

{#snippet goalPicker()}
  <div class="inline-flex items-center gap-1 bg-surface-3 rounded-lg p-0.5" role="group" aria-label="Backup goal">
    {#each GOAL_ORDER as g (g)}
      <button
        type="button"
        onclick={(e) => { e.stopPropagation(); selectGoal(g) }}
        class="text-xs px-2.5 py-1 rounded-md transition-colors cursor-pointer whitespace-nowrap {goal === g ? 'bg-surface-1 text-text font-medium shadow-sm' : 'text-text-muted hover:text-text'}"
        aria-pressed={goal === g}
      >{GOAL_LABELS[g]}</button>
    {/each}
  </div>
{/snippet}

<div class="border rounded-xl mb-8 {bgColor}">
  <div class="px-5 py-4 flex items-center justify-between gap-3 cursor-pointer" role="button" tabindex="0" onclick={() => expanded = !expanded} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); expanded = !expanded } }}>
    <div class="flex items-center gap-3 min-w-0">
      <h3 class="text-sm font-semibold text-text whitespace-nowrap">3-2-1 Backup Rule</h3>
      <span class="text-xs px-2.5 py-1 rounded-full font-medium inline-flex items-center gap-1 whitespace-nowrap {chipColor} {bgColor}">
        {#if goalMet}{@render checkIcon()}{/if}{statusLabel}
      </span>
    </div>
    <div class="flex items-center gap-3">
      <div class="hidden md:block">{@render goalPicker()}</div>
      {#if ondismiss}
        <button
          type="button"
          onclick={(e) => { e.stopPropagation(); ondismiss() }}
          class="text-text-muted hover:text-text p-1 -m-1 rounded transition-colors cursor-pointer"
          title="Hide – re-enable in Settings"
          aria-label="Hide 3-2-1 Backup Rule"
        >
          {@render xMark()}
        </button>
      {/if}
      <svg aria-hidden="true" class="w-4 h-4 text-text-muted transition-transform {expanded ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
    </div>
  </div>
  {#if expanded}
    <div class="px-5 pb-4 space-y-3 border-t border-border/50 pt-3">
      <!-- On small screens the picker lives here instead of the header. -->
      <div class="md:hidden">{@render goalPicker()}</div>

      <p class="text-xs text-text-muted">
        Goal: <strong class="text-text">{GOAL_LABELS[goal]}</strong>{isExplicit ? '' : ' (auto-selected for your setup)'}
      </p>

      <div class="text-xs space-y-1.5">
        {#each criteria as c (c.label)}
          <p class="inline-flex items-center gap-1.5 {c.met ? 'text-success' : 'text-text-muted'}">
            {#if c.met}{@render checkIcon()}{:else}{@render xMark()}{/if}{c.label}
          </p>
        {/each}
      </div>

      {#if goalMet}
        <div class="text-xs text-success bg-success/10 rounded-lg p-3 inline-flex items-center gap-1.5">
          {@render checkIcon()} Your {GOAL_LABELS[goal]} goal is met.
        </div>
      {:else}
        <div class="text-xs text-text-muted bg-surface-3 rounded-lg p-3 space-y-1">
          <p class="font-medium text-text mb-1">To reach this goal:</p>
          {#each criteria.filter(c => !c.met) as c (c.label)}
            <p>• {c.hint}</p>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>
