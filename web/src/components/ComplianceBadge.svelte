<script>
  import { SvelteSet } from 'svelte/reactivity'

  let { storage = [], jobs = [], replicationSources = [] } = $props()

  // --- 3 Copies ---
  // Count distinct storage destinations with at least one enabled job.
  let activeDestIds = $derived(new SvelteSet(
    jobs.filter(j => j.enabled).map(j => j.storage_dest_id)
  ))
  let activeDests = $derived(storage.filter(s => activeDestIds.has(s.id)))
  let copies = $derived(Math.min(activeDests.length + replicationSources.length, 3))
  let copiesMax = 3

  // --- 2 Media Types ---
  let mediaTypes = $derived.by(() => {
    const types = new SvelteSet()
    for (const s of activeDests) {
      if (s.type === 'local') types.add('disk')
      else types.add('network')
    }
    if (replicationSources.length > 0) types.add('network')
    return types
  })
  let media = $derived(Math.min(mediaTypes.size, 2))
  let mediaMax = 2

  // --- 1 Offsite ---
  let hasOffsite = $derived(
    activeDests.some(s => s.type !== 'local') || replicationSources.length > 0
  )
  let offsite = $derived(hasOffsite ? 1 : 0)
  let offsiteMax = 1

  // --- Overall ---
  let totalScore = $derived(copies + media + offsite)
  let maxScore = copiesMax + mediaMax + offsiteMax
  let color = $derived(
    totalScore >= 5 ? 'text-success' :
    totalScore >= 3 ? 'text-warning' :
    'text-danger'
  )
  let bgColor = $derived(
    totalScore >= 5 ? 'bg-success/10 border-success/30' :
    totalScore >= 3 ? 'bg-warning/10 border-warning/30' :
    'bg-danger/10 border-danger/30'
  )

  let expanded = $state(false)
</script>

<div class="border rounded-xl mb-8 {bgColor}">
  <div class="px-5 py-4 flex items-center justify-between cursor-pointer" role="button" tabindex="0" onclick={() => expanded = !expanded} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); expanded = !expanded } }}>
    <div class="flex items-center gap-3">
      <h3 class="text-sm font-semibold text-text">3-2-1 Backup Rule</h3>
      <span class="text-xs px-2.5 py-1 rounded-full font-medium {color} {bgColor}">
        {totalScore}/{maxScore}
      </span>
    </div>
    <div class="flex items-center gap-3">
      <div class="flex gap-2">
        <span class="text-xs {copies >= 3 ? 'text-success' : 'text-text-dim'}">
          {copies >= 3 ? '✓' : '✗'} 3 copies
        </span>
        <span class="text-xs {media >= 2 ? 'text-success' : 'text-text-dim'}">
          {media >= 2 ? '✓' : '✗'} 2 media
        </span>
        <span class="text-xs {offsite >= 1 ? 'text-success' : 'text-text-dim'}">
          {offsite >= 1 ? '✓' : '✗'} 1 offsite
        </span>
      </div>
      <svg class="w-4 h-4 text-text-muted transition-transform {expanded ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
    </div>
  </div>
  {#if expanded}
    <div class="px-5 pb-4 space-y-3 border-t border-border/50 pt-3">
      <div class="text-xs text-text-muted space-y-1.5">
        <p><strong class="{copies >= 3 ? 'text-success' : 'text-warning'}">Copies ({copies}/{copiesMax}):</strong> {activeDests.length} storage destination{activeDests.length !== 1 ? 's' : ''}{replicationSources.length > 0 ? ` + ${replicationSources.length} replication` : ''}</p>
        <p><strong class="{media >= 2 ? 'text-success' : 'text-warning'}">Media Types ({media}/{mediaMax}):</strong> {[...mediaTypes].join(', ') || 'none'}</p>
        <p><strong class="{offsite >= 1 ? 'text-success' : 'text-warning'}">Offsite ({offsite}/{offsiteMax}):</strong> {hasOffsite ? 'Remote storage configured' : 'No remote storage'}</p>
      </div>
      {#if totalScore < maxScore}
        <div class="text-xs text-text-muted bg-surface-3 rounded-lg p-3">
          <p class="font-medium text-text mb-1">Suggestions:</p>
          {#if copies < 3}
            <p>• Add more storage destinations to increase your backup copies</p>
          {/if}
          {#if media < 2}
            <p>• Add a remote storage (SFTP, SMB, NFS) for media type diversity</p>
          {/if}
          {#if offsite < 1}
            <p>• Configure an offsite destination or set up replication for off-site protection</p>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>
