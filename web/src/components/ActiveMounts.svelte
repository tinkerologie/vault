<script>
  import { getMounts } from '../lib/mounts.svelte.js'
  import { api } from '../lib/api.js'
  import { formatDate, relTime } from '../lib/utils.js'

  let { onunmount = null, onunmounterror = null } = $props()

  const mounts = getMounts()
  let unmountingId = $state(null)
  let copiedId = $state(null)
  let confirmUnmountId = $state(null)

  function copyPath(mount) {
    if (!mount?.mount_path) return
    navigator.clipboard?.writeText(mount.mount_path)
      .then(() => {
        copiedId = mount.id
        setTimeout(() => {
          if (copiedId === mount.id) copiedId = null
        }, 2000)
      })
      .catch(() => {})
  }

  async function performUnmount(mount) {
    unmountingId = mount.id
    try {
      await api.unmount(mount.id)
      confirmUnmountId = null
      if (onunmount) onunmount(mount)
    } catch (err) {
      if (onunmounterror) onunmounterror(err)
    } finally {
      unmountingId = null
    }
  }
</script>

{#if mounts.list.length > 0}
  <div class="bg-surface-2 border border-border rounded-xl p-5 mb-6" data-testid="active-mounts-card">
    <div class="flex items-center justify-between gap-3 mb-4">
      <div class="flex items-center gap-2.5">
        <div class="p-2 rounded-lg bg-vault/10 text-vault">
          <svg aria-hidden="true" class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
          </svg>
        </div>
        <div>
          <div class="flex items-center gap-2">
            <h2 class="text-base font-semibold text-text">Active Backup Mounts</h2>
            <span class="px-2 py-0.5 text-xs font-semibold rounded-full bg-vault/20 text-vault">{mounts.list.length}</span>
          </div>
          <p class="text-xs text-text-muted mt-0.5">Read-only FUSE mounts accessible on the Unraid host filesystem for inspection or file extraction.</p>
        </div>
      </div>
    </div>

    <div class="divide-y divide-border border border-border rounded-lg overflow-hidden bg-surface-1">
      {#each mounts.list as m (m.id)}
        <div class="p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-4">
          <div class="space-y-1 min-w-0 flex-1">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm font-semibold text-text">{m.job_name || `Job #${m.job_id}`}</span>
              <span class="text-xs text-text-dim">· Restore Point #{m.restore_point_id}</span>
              <span class="px-2 py-0.5 text-[11px] font-medium rounded capitalize {m.status === 'active' ? 'bg-emerald-500/15 text-emerald-400' : 'bg-amber-500/15 text-amber-400'}">
                {m.status}
              </span>
            </div>
            <div class="flex items-center gap-2 pt-1 flex-wrap">
              <span class="text-xs text-text-muted font-medium">Path:</span>
              <code class="text-xs font-mono bg-surface-3 border border-border px-2 py-0.5 rounded text-text select-all break-all">{m.mount_path}</code>
              <button type="button" onclick={() => copyPath(m)}
                class="text-xs px-2 py-0.5 rounded bg-surface-3 hover:bg-surface-4 text-text-muted hover:text-text transition-colors cursor-pointer inline-flex items-center gap-1">
                {#if copiedId === m.id}
                  <svg aria-hidden="true" class="w-3.5 h-3.5 text-emerald-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7"/></svg>
                  <span class="text-emerald-400 font-medium">Copied</span>
                {:else}
                  <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
                  <span>Copy</span>
                {/if}
              </button>
            </div>
            {#if m.started_at}
              <p class="text-[11px] text-text-dim pt-0.5">Mounted {formatDate(m.started_at)} ({relTime(m.started_at)})</p>
            {/if}
          </div>

          <div class="flex items-center gap-2 shrink-0">
            {#if confirmUnmountId === m.id}
              <button type="button" onclick={() => performUnmount(m)} disabled={unmountingId === m.id}
                class="px-3 py-1.5 text-xs font-medium text-white bg-danger hover:bg-danger/80 rounded-lg transition-colors disabled:opacity-50 cursor-pointer inline-flex items-center gap-1.5">
                {#if unmountingId === m.id}
                  <svg aria-hidden="true" class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
                  Unmounting…
                {:else}
                  Confirm Unmount
                {/if}
              </button>
              <button type="button" onclick={() => { confirmUnmountId = null }}
                class="px-2.5 py-1.5 text-xs text-text-muted hover:text-text rounded-lg border border-border bg-surface-3 transition-colors cursor-pointer">
                Cancel
              </button>
            {:else}
              <button type="button" onclick={() => { confirmUnmountId = m.id }}
                class="px-3 py-1.5 text-xs font-medium text-danger hover:text-white hover:bg-danger/80 border border-danger/30 rounded-lg transition-colors cursor-pointer inline-flex items-center gap-1.5">
                <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>
                Unmount
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  </div>
{/if}
