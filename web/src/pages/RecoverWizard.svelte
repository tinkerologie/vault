<script>
  import { api } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'
  import StorageForm from '../components/StorageForm.svelte'
  import Toast from '../components/Toast.svelte'
  import InlineSpinner from '../components/InlineSpinner.svelte'

  const STEPS = [
    { n: 1, label: 'Connect storage' },
    { n: 2, label: 'Backup password' },
    { n: 3, label: 'Restore' },
    { n: 4, label: 'Check paths' },
    { n: 5, label: 'Done' },
  ]

  let step = $state(1)
  let dest = $state(null)
  let backups = $state([])
  let noBackups = $state(false)
  let selected = $state(null)
  let passphrase = $state('')
  let showPass = $state(false)
  let verifying = $state(false)
  let passError = $state('')
  let restoring = $state(false)
  let confirmRestore = $state(false)
  let audit = $state(null)
  let remapDraft = $state({})
  let remapping = $state(false)
  let remapResults = $state(null)
  let summary = $state({ jobs: 0, storage: 0 })
  let toast = $state({ message: '', type: 'info', key: 0 })
  const showToast = (message, type = 'info') => (toast = { message, type, key: toast.key + 1 })

  // What this Vault holds BEFORE the wizard touches anything (step 1 creates
  // a destination) — the restore confirmation must not claim the settings are
  // "(empty)" when the wizard was opened on an already-configured Vault.
  let current = $state({ jobs: 0, storage: 0 })
  Promise.all([api.listJobs().catch(() => []), api.listStorage().catch(() => [])])
    .then(([jobs, storage]) => (current = { jobs: jobs.length, storage: storage.length }))

  const latestEncrypted = $derived(backups.length > 0 && backups[0].encrypted)
  const anyEncrypted = $derived(backups.some((b) => b.encrypted))
  const brokenEntries = $derived(audit ? audit.entries.filter((e) => !e.exists) : [])
  const remapError = (e) =>
    remapResults?.find((r) => r.kind === e.kind && r.id === e.id && !r.applied)?.error

  async function onDestinationSaved(newDest) {
    dest = newDest
    noBackups = false
    try {
      backups = await api.listDBBackups(dest.id)
    } catch (e) {
      // Transport/server failure is not "no backups found" — don't show the
      // _vault guidance for a timeout or 500; let the user retry.
      showToast(e.message || 'Could not list backups on this storage.', 'error')
      return
    }
    if (backups.length === 0) {
      noBackups = true
      showToast("We couldn't find a Vault backup here. Look for a folder named _vault on your backup storage.", 'warning')
      return
    }
    selected = backups[0]
    step = latestEncrypted ? 2 : 3
  }

  async function verifyPassword() {
    passError = ''
    verifying = true
    try {
      await api.restoreDB(dest.id, selected.path, passphrase, true)
      step = 3
    } catch (e) {
      passError = e.message || "That password didn't match. Check for typos — this is the encryption password you chose in Settings → Encryption on your old server."
    } finally {
      verifying = false
    }
  }

  function startRestore() {
    // A snapshot picked by hand can be encrypted even when the latest one
    // wasn't — route through the password step instead of failing later.
    if (selected.encrypted && !passphrase) {
      step = 2
      return
    }
    confirmRestore = true
  }

  async function doRestore() {
    restoring = true
    try {
      await api.restoreDB(dest.id, selected.path, selected.encrypted ? passphrase : '')
      const [jobs, storage] = await Promise.all([
        api.listJobs().catch(() => []),
        api.listStorage().catch(() => []),
      ])
      summary = { jobs: jobs.length, storage: storage.length }
      audit = await api.pathAudit().catch(() => null)
      step = 4
      showToast('Your settings are back.', 'success')
    } catch (e) {
      // A client-side timeout doesn't mean the server stopped — don't claim
      // nothing changed.
      showToast(e.message || 'The restore did not finish — check the Dashboard before retrying.', 'error')
    } finally {
      restoring = false
      confirmRestore = false
    }
  }

  async function applyRemap() {
    const updates = brokenEntries
      .filter((e) => remapDraft[`${e.kind}-${e.id}`])
      .map((e) => ({ kind: e.kind, id: e.id, job_id: e.job_id || 0, new_path: remapDraft[`${e.kind}-${e.id}`] }))
    if (updates.length === 0) {
      step = 5
      return
    }
    remapping = true
    try {
      const res = await api.pathRemap(updates)
      remapResults = res.results
      audit = await api.pathAudit().catch(() => audit)
      const failed = res.results.filter((r) => !r.applied)
      if (failed.length === 0) showToast('Paths updated.', 'success')
      else showToast(`${failed.length} path(s) could not be updated — see the rows below.`, 'warning')
    } catch (e) {
      showToast(e.message || 'Updating paths failed.', 'error')
    } finally {
      remapping = false
    }
  }

  function goBack() {
    if (step === 2) step = 1
    // passphrase is non-empty iff the password step was actually passed —
    // accurate even when a hand-picked encrypted snapshot routed through it.
    else if (step === 3) step = passphrase ? 2 : 1
    // No Back from step 4: the restore already ran (point of no return), and
    // re-restoring would silently discard just-applied path remaps.
  }

  function fmtWhen(ts) {
    const d = new Date(ts)
    const days = Math.round((Date.now() - d.getTime()) / 86400000)
    const rel = days <= 0 ? 'today' : days === 1 ? 'yesterday' : `${days} days ago`
    return `${d.toLocaleString()} (${rel})`
  }
</script>

<div class="max-w-3xl mx-auto p-4 sm:p-6">
  <h1 class="text-2xl font-bold text-text">Recover Vault</h1>
  <p class="text-sm text-text-muted mt-1 mb-6">
    Bring back your jobs, storage and history from a backup. You'll need access to your backup storage
    and your backup password — nothing from the old server.
  </p>

  <!-- Step indicator -->
  <div class="flex items-center gap-2 mb-6">
    {#each STEPS as s (s.n)}
      <div class="flex items-center gap-2 {s.n <= step ? '' : 'opacity-40'}" aria-current={s.n === step ? 'step' : undefined}>
        <div class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors {s.n <= step ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted'}">
          {#if s.n < step}
            <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7"/></svg>
          {:else}
            {s.n}
          {/if}
        </div>
        <span class="text-xs font-medium {s.n === step ? 'text-text' : 'text-text-muted'} hidden sm:inline">{s.label}</span>
      </div>
      {#if s.n < 5}
        <div class="flex-1 h-px {s.n < step ? 'bg-vault' : 'bg-border'}"></div>
      {/if}
    {/each}
  </div>

  {#if step === 1}
    <div class="bg-surface-2 border border-border rounded-xl p-5">
      <h2 class="text-base font-semibold text-text mb-1">Connect the storage that holds your backups</h2>
      <p class="text-sm text-text-muted mb-4">
        Point Vault at the place your old server sent its backups — the same share, server or bucket
        you used before. Use "Test connection" to make sure it works before continuing.
      </p>
      {#if noBackups}
        <div class="bg-warning/10 border border-warning/30 rounded-lg p-3 mb-4 text-sm text-text">
          We couldn't find a Vault backup on this storage. Look for a folder named
          <code class="font-mono">_vault</code> on your backup storage — that's where your settings
          backups live. Check the connection details below (especially the path) and press Connect again.
        </div>
      {/if}
      <!-- Remount the form once a destination exists so re-submitting updates it
           instead of creating a duplicate (StorageForm reads `initial` at mount). -->
      {#key dest?.id}
        <StorageForm
          initial={dest}
          submitLabel="Connect"
          onsaved={onDestinationSaved}
          oncancel={() => navigate('/recovery')}
          ontoast={showToast}
        />
      {/key}
    </div>

  {:else if step === 2}
    <div class="bg-surface-2 border border-border rounded-xl p-5">
      <h2 class="text-base font-semibold text-text mb-1">Enter your backup password</h2>
      <p class="text-sm text-text-muted mb-4">
        This is the encryption password you chose when you set up encryption on your old server
        (Settings → Encryption). It's the only thing you need from before.
      </p>
      <form onsubmit={(e) => { e.preventDefault(); if (passphrase && !verifying) verifyPassword() }}>
        <label for="recover-pass" class="block text-sm font-medium text-text-muted mb-1.5">Your backup password</label>
        <div class="flex gap-2">
          <input id="recover-pass" type={showPass ? 'text' : 'password'} bind:value={passphrase} autocomplete="off"
            class="flex-1 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
          <button type="button" class="btn btn-secondary" onclick={() => (showPass = !showPass)}>
            {showPass ? 'Hide' : 'Show'}
          </button>
        </div>
        {#if passError}
          <p class="text-sm text-danger mt-2">{passError}</p>
        {/if}
        {#if verifying}
          <p class="text-sm text-text-muted mt-2">Checking your password — this takes a few seconds by design.</p>
        {/if}
        <div class="flex items-center justify-between mt-5 pt-4 border-t border-border">
          <button type="button" class="btn btn-ghost" onclick={goBack}>Back</button>
          <button type="submit" class="btn btn-primary" disabled={!passphrase || verifying}>
            {#if verifying}<InlineSpinner /> Checking…{:else}Continue{/if}
          </button>
        </div>
      </form>
    </div>

  {:else if step === 3}
    <div class="bg-surface-2 border border-border rounded-xl p-5">
      <h2 class="text-base font-semibold text-text mb-1">Choose which backup to restore</h2>
      <p class="text-sm text-text-muted mb-4">
        The most recent backup is already selected — that's the right choice for almost everyone.
      </p>
      {#if !anyEncrypted}
        <p class="text-xs text-text-dim mb-4">
          These backups aren't encrypted, so no password is needed. To encrypt future
          backups, set a backup password in Settings → Encryption after you finish.
        </p>
      {/if}
      <fieldset class="space-y-2">
        <legend class="sr-only">Backups found on your storage</legend>
        {#each backups as b (b.path)}
          <label class="flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors
            {selected === b ? 'border-vault bg-vault/10' : 'border-border bg-surface-3 hover:border-border-hover'}">
            <input type="radio" name="recover-snapshot" class="accent-vault mt-1" value={b} bind:group={selected} />
            <span class="min-w-0">
              <span class="block text-sm font-medium text-text">{b.is_latest ? 'Most recent backup' : fmtWhen(b.timestamp)}</span>
              <span class="block text-xs text-text-muted mt-0.5 truncate">{b.name} · {(b.size / 1024).toFixed(0)} KB{b.encrypted ? ' · encrypted' : ''}</span>
            </span>
          </label>
        {/each}
      </fieldset>

      {#if confirmRestore}
        <div class="bg-warning/10 border border-warning/30 rounded-lg p-4 mt-4">
          <p class="text-sm text-text mb-3">
            {#if current.jobs > 0 || current.storage > 0}
              This replaces this Vault's current settings —
              <strong>including its {current.jobs} job(s) and {current.storage} storage destination(s)</strong> —
            {:else}
              This replaces this Vault's current (empty) settings
            {/if}
            with the backup
            {selected.is_latest ? 'from your most recent backup' : `from ${new Date(selected.timestamp).toLocaleString()}`}.
            Your backed-up files on storage are not touched.
          </p>
          <div class="flex items-center gap-3">
            <button type="button" class="btn btn-primary" disabled={restoring} onclick={doRestore}>
              {#if restoring}<InlineSpinner /> Restoring…{:else}Yes, restore my settings{/if}
            </button>
            <button type="button" class="btn btn-ghost" disabled={restoring} onclick={() => (confirmRestore = false)}>Cancel</button>
          </div>
        </div>
      {/if}

      <div class="flex items-center justify-between mt-5 pt-4 border-t border-border">
        <button type="button" class="btn btn-ghost" onclick={goBack}>Back</button>
        {#if !confirmRestore}
          <button type="button" class="btn btn-primary" disabled={!selected} onclick={startRestore}>Restore this backup</button>
        {/if}
      </div>
    </div>

  {:else if step === 4}
    <div class="bg-surface-2 border border-border rounded-xl p-5">
      <h2 class="text-base font-semibold text-text mb-1">Check your folder paths</h2>
      {#if audit === null}
        <p class="text-sm text-text-muted mb-4">
          We couldn't check your paths automatically. If a backup fails later, review the paths in
          Jobs and Storage.
        </p>
        <div class="flex items-center justify-end pt-4 border-t border-border">
          <button type="button" class="btn btn-primary" onclick={() => (step = 5)}>Continue</button>
        </div>
      {:else if brokenEntries.length === 0}
        <p class="text-sm text-success mb-4">✓ All your configured paths exist on this server. Nothing to fix.</p>
        <div class="flex items-center justify-end pt-4 border-t border-border">
          <button type="button" class="btn btn-primary" onclick={() => (step = 5)}>Continue</button>
        </div>
      {:else}
        <p class="text-sm text-text-muted mb-4">
          Your old server had different drives or shares. These paths from your backup don't exist
          here — pick where they live now, or skip and fix them later in Jobs/Storage.
        </p>
        <datalist id="path-candidates">
          {#each audit.candidates as c (c)}
            <option value={c}></option>
          {/each}
        </datalist>
        <div class="space-y-3">
          {#each brokenEntries as e (`${e.kind}-${e.id}`)}
            <div class="bg-surface-3 border border-border rounded-lg p-4">
              <div class="text-xs font-medium text-text-muted">{e.kind === 'storage' ? 'Storage destination' : 'Backup folder'}</div>
              <div class="text-sm font-medium text-text">{e.name}</div>
              <div class="text-xs text-text-dim line-through font-mono mt-1">{e.path}</div>
              <label class="sr-only" for={`remap-${e.kind}-${e.id}`}>New path for {e.name}</label>
              <input id={`remap-${e.kind}-${e.id}`} type="text" list="path-candidates"
                bind:value={remapDraft[`${e.kind}-${e.id}`]}
                placeholder="New path, e.g. /mnt/user/…"
                class="w-full mt-2 px-3 py-2 bg-surface-2 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
              {#if remapError(e)}
                <p class="text-xs text-danger mt-1">{remapError(e)}</p>
              {/if}
            </div>
          {/each}
        </div>
        <div class="flex items-center justify-end gap-3 mt-5 pt-4 border-t border-border">
          <button type="button" class="btn btn-secondary" disabled={remapping} onclick={() => (step = 5)}>Skip for now</button>
          <button type="button" class="btn btn-primary" disabled={remapping} onclick={applyRemap}>
            {#if remapping}<InlineSpinner /> Updating…{:else}Update paths{/if}
          </button>
        </div>
      {/if}
    </div>

  {:else if step === 5}
    <div class="bg-surface-2 border border-border rounded-xl p-8 text-center">
      <div class="text-4xl mb-3" aria-hidden="true">🎉</div>
      <h2 class="text-xl font-bold text-text mb-2">Vault is back</h2>
      <p class="text-sm text-text-muted max-w-md mx-auto mb-6">
        Recovered {summary.jobs} job(s) and {summary.storage} storage destination(s).
        To bring files, containers or VMs back, use the normal Restore page. And store your backup
        password somewhere safe off this server — it's the one thing recovery always needs.
      </p>
      <div class="flex items-center justify-center gap-3">
        <button type="button" class="btn btn-primary" onclick={() => navigate('/restore')}>Go to Restore</button>
        <button type="button" class="btn btn-secondary" onclick={() => navigate('/')}>Go to Dashboard</button>
      </div>
    </div>
  {/if}
</div>

<Toast message={toast.message} type={toast.type} key={toast.key} />
