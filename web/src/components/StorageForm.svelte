<script>
  import { api } from '../lib/api.js'
  import { parseConfig } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import { localStoragePathProblem } from '../lib/storagePath.js'
  import Tooltip from './Tooltip.svelte'
  import PasswordInput from './PasswordInput.svelte'
  import { tick } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import { nameFieldError } from '../lib/jobForm.js'
  import { snapshot } from '../lib/unsaved.js'
  import InlineSpinner from './InlineSpinner.svelte'
  import { ICONS, STORAGE_TYPE_COLORS, STORAGE_TYPE_ICONS } from '../lib/icons.js'
  import { cardClass } from '../lib/selectable.js'

  // The value the API returns in place of a stored secret
  // (internal/credentials RedactionMarker). Sending it back keeps the secret.
  const REDACTED = '••••••••'
  const SECRET_KEYS = ['password', 'secret_key']

  let {
    initial = null, // existing destination object when editing, null when adding
    submitLabel = 'Add Storage',
    onsaved = () => {}, // called with the created/updated destination
    oncancel = () => {},
    ontoast = () => {}, // (message, type) — routed to the host page's Toast
    dirty = $bindable(false), // true once any field differs from how it opened (#687)
  } = $props()

  // Instance-safe ids for the checkbox title/description pairs: the form can be
  // mounted more than once (Storage modal, Recover wizard).
  const uid = $props.id()

  function defaultForm() {
    return {
      name: '',
      type: 'local',
      config: { path: '' },
      dedup_enabled: false,
      stage_beside_destination: false,
    }
  }

  function initForm(dest) {
    if (!dest) return defaultForm()
    // Deep-clone: parseConfig returns object inputs as-is, and form bindings
    // must never mutate the caller's destination (cancel would leak edits).
    const cfg = JSON.parse(JSON.stringify(parseConfig(dest.config)))
    // Migrate legacy "path" → "base_path" for SFTP/SMB configs.
    if ((dest.type === 'sftp' || dest.type === 'smb') && cfg.base_path === undefined) {
      cfg.base_path = cfg.path ?? ''
    }
    // A saved secret arrives as the redaction marker. Show the field empty
    // with a "leave blank to keep" hint instead (#706): typing then replaces
    // the secret rather than appending to the marker.
    for (const key of SECRET_KEYS) {
      if (cfg[key] === REDACTED) {
        savedSecrets.add(key)
        cfg[key] = ''
      }
    }
    return {
      name: dest.name,
      type: dest.type,
      config: cfg,
      dedup_enabled: !!dest.dedup_enabled,
      // Carry the DB-backup fan-out flag through the modal save so editing
      // an unrelated field doesn't reset it. The dedicated row toggle on
      // the card is still the primary control.
      backup_database_enabled: !!dest.backup_database_enabled,
      stage_beside_destination: !!dest.stage_beside_destination,
    }
  }

  // `initial` is deliberately read once at mount: the host remounts this
  // component each time the modal opens, so the form never needs to track
  // later changes to the prop. Consumers that can change `initial` while the
  // component stays mounted must remount it instead (wrap in {#key}).
  /** Secret fields that hold a stored value the user hasn't replaced. */
  const savedSecrets = new SvelteSet()
  // svelte-ignore state_referenced_locally
  const init = initial
  let form = $state(initForm(init))
  const opened = snapshot(form)
  $effect(() => {
    dirty = snapshot(form) !== opened
  })

  // A secret left blank in edit mode keeps the stored one: send the marker
  // back, which the server swaps for the saved value.
  function configForServer() {
    const cfg = { ...form.config }
    for (const key of savedSecrets) {
      if (key in cfg && (cfg[key] ?? '') === '') cfg[key] = REDACTED
    }
    return cfg
  }

  const keepsSecret = (key) => savedSecrets.has(key) && !(form.config[key] ?? '')

  // Field errors shown under their inputs (#688).
  let nameError = $state('')
  let pathError = $state('')
  let bandwidthLabel = $derived(form.type === 'local' ? 'Disk write speed limit (Mbps)' : 'Bandwidth limit (Mbps)')
  let saving = $state(false)
  // Early feedback for unsafe local roots (#514); the server re-validates.
  let localPathProblem = $derived(form.type === 'local' ? localStoragePathProblem(form.config.path) : '')

  async function saveStorage() {
    if (saving) return
    nameError = ''
    pathError = ''
    if (!form.name.trim()) {
      nameError = 'Enter a name for this destination.'
      await tick()
      document.getElementById('sname')?.focus()
      return
    }
    if (localPathProblem) {
      pathError = localPathProblem
      ontoast(localPathProblem, 'error')
      await tick()
      document.getElementById(`${uid}-path`)?.focus()
      return
    }
    saving = true
    try {
      const payload = {
        name: form.name,
        type: form.type,
        config: JSON.stringify(configForServer()),
        // Top-level: stored as its own column on storage_destinations.
        // Immutable after creation (UI gates the toggle when editing).
        dedup_enabled: !!form.dedup_enabled,
        // Preserve the current DB fan-out value through modal saves so
        // editing other fields doesn't accidentally disable it. New
        // destinations default to false.
        backup_database_enabled: !!form.backup_database_enabled,
        stage_beside_destination: form.type === 'local' ? !!form.stage_beside_destination : false,
      }
      let result
      if (init) {
        result = await api.updateStorage(init.id, payload)
      } else {
        result = await api.createStorage(payload)
      }
      dirty = false
      onsaved(result)
    } catch (e) {
      const fieldMsg = nameFieldError(e.message, 'destination')
      if (fieldMsg) {
        nameError = fieldMsg
        await tick()
        document.getElementById('sname')?.focus()
      } else {
        ontoast(e.message, 'error')
      }
    } finally {
      saving = false
    }
  }

  function applyType(nextType) {
    // Re-clicking the selected type must not wipe entered credentials.
    if (nextType === form.type) return
    const defaults = {
      local: { path: '', bandwidth_limit_mbps: 0 },
      sftp: { host: '', port: 22, user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      smb: { host: '', share: '', user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      nfs: { host: '', export: '', base_path: '', version: '4', options: '', bandwidth_limit_mbps: 0 },
      webdav: { url: '', username: '', password: '', base_path: '', insecure_skip_verify: false, timeout_seconds: 0, stall_timeout_seconds: 300, chunk_size_mb: 0, bandwidth_limit_mbps: 0 },
      s3: { bucket: '', region: '', access_key: '', secret_key: '', endpoint: '', base_path: '', force_path_style: false, upload_timeout_minutes: 0, part_size_mb: 0, bandwidth_limit_mbps: 0 },
    }
    // Reassign the full form object so Svelte always re-renders the keyed
    // config block when switching destination type.
    savedSecrets.clear()
    form = {
      ...form,
      type: nextType,
      config: defaults[nextType] || {},
      stage_beside_destination: nextType === 'local' ? form.stage_beside_destination : false,
    }
    formTestResult = null
  }

  // Backend types offered in the add/edit picker (issue #206 / E8).
  const storageTypes = [
    { value: 'local', label: 'Local Path' },
    { value: 'sftp', label: 'SFTP' },
    { value: 'smb', label: 'SMB / CIFS' },
    { value: 'nfs', label: 'NFS' },
    { value: 'webdav', label: 'WebDAV' },
    { value: 's3', label: 'S3', srLabel: ' / S3-Compatible' },
  ]

  // Quick-select S3 providers — prefill endpoint/region hints. Placeholders with
  // <angle-brackets> mark values the user must fill in (account-specific hosts).
  const s3Presets = [
    { label: 'AWS S3', endpoint: '', region: 'us-east-1', forcePathStyle: false },
    { label: 'Backblaze B2', endpoint: 'https://s3.us-west-002.backblazeb2.com', region: 'us-west-002', forcePathStyle: false },
    { label: 'Cloudflare R2', endpoint: 'https://<accountid>.r2.cloudflarestorage.com', region: 'auto', forcePathStyle: false },
    { label: 'Wasabi', endpoint: 'https://s3.wasabisys.com', region: 'us-east-1', forcePathStyle: false },
    { label: 'MinIO', endpoint: 'http://<host>:9000', region: 'us-east-1', forcePathStyle: true },
    { label: 'IDrive E2', endpoint: 'https://<region>.idrivee2-XX.com', region: 'us-east-1', forcePathStyle: false },
    { label: 'MEGA S4', endpoint: 'https://s3.g.s4.mega.io', region: 'g', forcePathStyle: false },
  ]
  function applyS3Preset(p) {
    form.config = { ...form.config, endpoint: p.endpoint, region: p.region, force_path_style: p.forcePathStyle }
    formTestResult = null
  }

  // Test the current (unsaved) form config before saving.
  let formTesting = $state(false)
  /** @type {{ success: boolean, error?: string } | null} */
  let formTestResult = $state(null)
  // A test result only describes the config it ran against — invalidate it as
  // soon as any config field changes so a stale "Connection OK" can't linger.
  // Tracks form.config only (not formTesting), so completing a test — which
  // doesn't mutate config — never re-runs this and wipes the fresh result.
  $effect(() => {
    JSON.stringify(form.config) // track all nested config fields
    formTestResult = null
  })
  async function testFormConnection() {
    formTesting = true
    formTestResult = null
    // Snapshot the config under test so a result that lands after the user has
    // since edited the form is dropped, rather than showing a stale result for
    // the old config (the invalidation $effect can't undo an in-flight test).
    const testedKey = JSON.stringify({ type: form.type, config: form.config })
    const isStale = () => JSON.stringify({ type: form.type, config: form.config }) !== testedKey
    try {
      const result = await api.testStorageConfig({ id: initial?.id, type: form.type, config: JSON.stringify(configForServer()) })
      if (isStale()) return
      formTestResult = result
      ontoast(result.success ? 'Connection successful!' : `Connection failed: ${result.error || 'unknown error'}`, result.success ? 'success' : 'error')
    } catch (e) {
      if (isStale()) return
      const msg = e?.message || 'Connection test failed'
      formTestResult = { success: false, error: msg }
      ontoast(msg, 'error')
    } finally {
      formTesting = false
    }
  }

  const storageIcons = STORAGE_TYPE_ICONS
  const storageColors = STORAGE_TYPE_COLORS
</script>

<form onsubmit={(e) => { e.preventDefault(); saveStorage() }} class="space-y-5">
  <div>
    <label for="sname" class="block text-sm font-medium text-text-muted mb-1.5">Name</label>
    <input id="sname" type="text" bind:value={form.name} required aria-required="true"
      oninput={() => { nameError = '' }}
      aria-invalid={nameError ? 'true' : undefined}
      aria-describedby={nameError ? `${uid}-name-error` : undefined}
      class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border {nameError ? 'border-danger' : 'border-border'} rounded-lg text-sm text-text placeholder-text-muted" placeholder="e.g. Backup NAS" />
    {#if nameError}
      <p id="{uid}-name-error" class="text-xs text-danger mt-1">{nameError}</p>
    {/if}
  </div>

  <div>
    <span class="block text-sm font-medium text-text-muted mb-1.5">Type</span>
    <div role="group" aria-label="Storage type" class="grid grid-cols-2 sm:grid-cols-3 gap-2">
      {#each storageTypes as t (t.value)}
        <button type="button" aria-pressed={form.type === t.value}
          onclick={() => applyType(t.value)}
          class="flex items-center gap-2 px-3 py-2.5 rounded-lg border text-sm text-left transition-colors
            {cardClass(form.type === t.value)} {form.type === t.value ? 'text-text' : 'text-text-muted hover:text-text'}">
          <svg aria-hidden="true" class="w-5 h-5 shrink-0 {storageColors[t.value] || 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={storageIcons[t.value]}/></svg>
          <span class="font-medium truncate">{t.label}{#if t.srLabel}<span class="sr-only">{t.srLabel}</span>{/if}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- Dynamic config fields per type -->
  {#key form.type}
  {#if form.type === 'local'}
    <div>
      <label for="{uid}-path" class="block text-sm font-medium text-text-muted mb-1.5">Path</label>
      <PathBrowser bind:value={form.config.path} validate={localStoragePathProblem} id="{uid}-path" describedby="{uid}-path-hint" invalid={!!pathError && !!localPathProblem} />
      {#if localPathProblem}
        <p id="{uid}-path-hint" class="text-xs {pathError ? 'text-danger' : 'text-warning'} mt-1.5" data-testid="local-path-hint">{localPathProblem}</p>
      {:else}
        <p id="{uid}-path-hint" class="text-xs text-text-dim mt-1.5">Use a dedicated folder. Deleting this destination with its files removes only Vault's backups.</p>
      {/if}
    </div>
    <label class="flex items-start gap-2 text-sm text-text-muted">
      <input type="checkbox" bind:checked={form.stage_beside_destination} class="accent-vault mt-1"
        aria-labelledby="{uid}-stage-title" aria-describedby="{uid}-stage-desc" />
      <span class="flex-1">
        <span id="{uid}-stage-title" class="block font-medium text-text">Stage temporary backups beside this destination</span>
        <span id="{uid}-stage-desc" class="block text-xs text-text-muted mt-0.5">
          Prefers a hidden <code>.vault-stage</code> folder on the destination disk instead of the cache pool (falling back to the normal staging cascade if unavailable). Useful for large backups that might fill up smaller cache drives during compression.
        </span>
      </span>
    </label>
  {:else if form.type === 'sftp'}
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
      <div class="sm:col-span-2">
        <label for="cfg_host" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
        <input id="cfg_host" type="text" bind:value={form.config.host}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" placeholder="e.g. 192.168.1.100" />
      </div>
      <div>
        <label for="cfg_port" class="block text-sm font-medium text-text-muted mb-1.5">Port</label>
        <input id="cfg_port" type="number" min="1" max="65535" bind:value={form.config.port}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text" placeholder="22" />
      </div>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="cfg_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="cfg_user" type="text" bind:value={form.config.user}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <label for="cfg_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
        <PasswordInput id="cfg_pass" bind:value={form.config.password}
          placeholder={keepsSecret('password') ? 'Saved' : ''}
          aria-describedby={savedSecrets.has('password') ? 'cfg_pass-saved' : undefined}
          toggleLabel="Show password"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        {#if savedSecrets.has('password')}
          <p id="cfg_pass-saved" class="text-xs text-text-dim mt-1">Saved. Leave blank to keep it.</p>
        {/if}
      </div>
    </div>
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="cfg_spath">Remote Path</label>
        <Tooltip label="Remote Path" text="Absolute path on the SFTP server where Vault will store backups. The directory must exist and the user must have write permission." />
      </div>
      <input id="cfg_spath" type="text" bind:value={form.config.base_path}
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" placeholder="e.g. /backups/vault" />
    </div>
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="cfg_shostkey">Host Key Fingerprint</label>
        <Tooltip label="Host Key Fingerprint" text="Optional. Run ssh-keygen -lf on the server's host key and copy the SHA256 value. If left empty, Vault trusts the key seen on the first connection and refuses a different key later. Set this after replacing the server." />
      </div>
      <input id="cfg_shostkey" type="text" autocomplete="off" spellcheck="false" bind:value={form.config.host_key}
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" placeholder="e.g. SHA256:…" />
    </div>
  {:else if form.type === 'smb'}
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="cfg_smbhost" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
        <input id="cfg_smbhost" type="text" bind:value={form.config.host}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" placeholder="e.g. 192.168.1.100" />
      </div>
      <div>
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="cfg_share">Share</label>
          <Tooltip label="Share" text="The top-level SMB share name as configured on the server (e.g. Backups). Use the Path field below for a sub-folder within the share." />
        </div>
        <input id="cfg_share" type="text" bind:value={form.config.share}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" placeholder="e.g. Backups" />
      </div>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="cfg_smbuser" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="cfg_smbuser" type="text" bind:value={form.config.user}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <label for="cfg_smbpass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
        <PasswordInput id="cfg_smbpass" bind:value={form.config.password}
          placeholder={keepsSecret('password') ? 'Saved' : ''}
          aria-describedby={savedSecrets.has('password') ? 'cfg_smbpass-saved' : undefined}
          toggleLabel="Show password"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        {#if savedSecrets.has('password')}
          <p id="cfg_smbpass-saved" class="text-xs text-text-dim mt-1">Saved. Leave blank to keep it.</p>
        {/if}
      </div>
    </div>
    <div>
      <label for="cfg_smbpath" class="block text-sm font-medium text-text-muted mb-1.5">Path</label>
      <input id="cfg_smbpath" type="text" bind:value={form.config.base_path}
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" placeholder="e.g. vault" />
    </div>
  {:else if form.type === 'nfs'}
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div class="sm:col-span-2">
        <label for="nfs_host" class="block text-sm font-medium text-text-muted mb-1.5">NFS Host</label>
        <input id="nfs_host" type="text" bind:value={form.config.host} placeholder="e.g. 192.168.1.100"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div class="sm:col-span-2">
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="nfs_export">Export Path</label>
          <Tooltip label="Export Path" text="The path the NFS server exports – matches the entry in /etc/exports on the server (e.g. /mnt/user/backups). This is what gets mounted, not a sub-path within it." />
        </div>
        <input id="nfs_export" type="text" bind:value={form.config.export} placeholder="e.g. /mnt/user/backups"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="nfs_base">Base Path</label>
          <Tooltip label="Base Path" text="Optional sub-directory within the mounted export where Vault will write its data. Leave blank to use the export root directly." />
        </div>
        <input id="nfs_base" type="text" bind:value={form.config.base_path} placeholder="e.g. vault"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="nfs_version">NFS Version</label>
          <Tooltip label="NFS Version" text="NFSv3: wider compatibility, simpler setup. NFSv4: better security and performance, but may require DNS and auth configuration." />
        </div>
        <select id="nfs_version" bind:value={form.config.version}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="3">NFSv3</option>
          <option value="4">NFSv4</option>
        </select>
      </div>
      <div class="sm:col-span-2">
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="nfs_options">Mount Options</label>
          <Tooltip label="Mount Options" text="Optional NFS mount flags such as rw, sync, hard, soft, or nolock. Leave blank for sensible defaults." />
        </div>
        <input id="nfs_options" type="text" bind:value={form.config.options} placeholder="e.g. rw,sync"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
    </div>
  {:else if form.type === 'webdav'}
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="dav_url">Server URL</label>
        <Tooltip label="Server URL" text="Full URL to the WebDAV endpoint, e.g. https://nextcloud.example.com/remote.php/dav/files/username/" />
      </div>
      <input id="dav_url" type="url" bind:value={form.config.url} placeholder="e.g. https://webdav.example.com/"
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" />
      {#if form.config.url?.trim().toLowerCase().startsWith('http://')}
        <p class="text-xs text-warning mt-1" role="note">Plain HTTP: the login and backup data can be read on the network. Use it only on a network you trust, or use https://.</p>
      {/if}
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="dav_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="dav_user" type="text" bind:value={form.config.username}
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <label for="dav_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password / App Token</label>
        <PasswordInput id="dav_pass" bind:value={form.config.password}
          placeholder={keepsSecret('password') ? 'Saved' : ''}
          aria-describedby={savedSecrets.has('password') ? 'dav_pass-saved' : undefined}
          toggleLabel="Show password"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        {#if savedSecrets.has('password')}
          <p id="dav_pass-saved" class="text-xs text-text-dim mt-1">Saved. Leave blank to keep it.</p>
        {/if}
      </div>
    </div>
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="dav_base">Base Path</label>
        <Tooltip label="Base Path" text="Optional sub-folder under the server URL where Vault will write its data." />
      </div>
      <input id="dav_base" type="text" bind:value={form.config.base_path} placeholder="e.g. vault"
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
    </div>
    <div class="flex items-center gap-2 text-sm text-text-muted">
      <label class="flex items-center gap-2">
        <input type="checkbox" bind:checked={form.config.insecure_skip_verify} class="accent-vault" />
        Allow self-signed TLS certificates
      </label>
      <Tooltip label="Allow self-signed TLS certificates" text="Skip TLS certificate validation. Only enable for trusted private servers using self-signed certificates." />
    </div>
    <details class="group">
      <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
        Advanced &middot; Transfer
      </summary>
      <div class="flex flex-col gap-3 mt-3">
        <div>
          <div class="block text-sm font-medium text-text-muted mb-1.5">
            <label for="dav_chunk">Chunk size (MiB)</label>
            <Tooltip label="Chunk size (MiB)" text="Splits large uploads into separate pieces so one dropped connection doesn't restart the whole file. Recommended: leave at 0 (uses 50 MiB pieces). Use -1 only if your server rejects chunked uploads." />
          </div>
          <input id="dav_chunk" type="number" bind:value={form.config.chunk_size_mb} placeholder="0 (50 MiB)" min="-1"
            class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        </div>
        <div>
          <div class="block text-sm font-medium text-text-muted mb-1.5">
            <label for="dav_stall">Stall timeout (seconds)</label>
            <Tooltip label="Stall timeout (seconds)" text="Gives up on an upload if no data moves for this many seconds – catches a silently stalled connection. The timer resets whenever data flows, so even huge files finish fine. Recommended: 300 (5 min); use -1 to disable." />
          </div>
          <input id="dav_stall" type="number" bind:value={form.config.stall_timeout_seconds} placeholder="300" min="-1"
            class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        </div>
        <div>
          <div class="block text-sm font-medium text-text-muted mb-1.5">
            <label for="dav_overall">Overall request timeout (seconds)</label>
            <Tooltip label="Overall request timeout (seconds)" text="A hard time limit on every request, including a whole file upload. Set too low, it cuts off large uploads. Recommended: leave at 0 (no limit) – the stall timeout above already handles dead connections." />
          </div>
          <input id="dav_overall" type="number" bind:value={form.config.timeout_seconds} placeholder="0 (unlimited)" min="0"
            class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        </div>
      </div>
    </details>
  {:else if form.type === 's3'}
    <div>
      <span class="block text-sm font-medium text-text-muted mb-1.5">Provider preset <Tooltip label="Provider preset" text="Prefills endpoint and region for common S3 providers. You can still edit any field. Placeholders in <angle-brackets> must be filled in with your account details." /></span>
      <div class="flex flex-wrap gap-2">
        {#each s3Presets as p (p.label)}
          <button type="button" onclick={() => applyS3Preset(p)}
            class="px-2.5 py-1 text-xs font-medium rounded-full border border-border bg-surface-3 text-text-muted hover:border-vault/40 hover:text-text transition-colors">
            {p.label}
          </button>
        {/each}
      </div>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="s3_bucket" class="block text-sm font-medium text-text-muted mb-1.5">Bucket</label>
        <input id="s3_bucket" type="text" bind:value={form.config.bucket} placeholder="e.g. my-vault-backups"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
      <div>
        <div class="block text-sm font-medium text-text-muted mb-1.5">
          <label for="s3_region">Region</label>
          <Tooltip label="Region" text="AWS region code, e.g. us-east-1. For S3-compatible providers, use the region required by the provider." />
        </div>
        <input id="s3_region" type="text" bind:value={form.config.region} placeholder="e.g. us-east-1"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
      </div>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <label for="s3_ak" class="block text-sm font-medium text-text-muted mb-1.5">Access Key ID</label>
        <input id="s3_ak" type="text" bind:value={form.config.access_key} autocomplete="off"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" />
      </div>
      <div>
        <label for="s3_sk" class="block text-sm font-medium text-text-muted mb-1.5">Secret Access Key</label>
        <PasswordInput id="s3_sk" bind:value={form.config.secret_key}
          placeholder={keepsSecret('secret_key') ? 'Saved' : ''}
          aria-describedby={savedSecrets.has('secret_key') ? 's3_sk-saved' : undefined}
          toggleLabel="Show secret access key"
          class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" />
        {#if savedSecrets.has('secret_key')}
          <p id="s3_sk-saved" class="text-xs text-text-dim mt-1">Saved. Leave blank to keep it.</p>
        {/if}
      </div>
    </div>
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="s3_endpoint">Endpoint</label>
        <Tooltip label="Endpoint" text="Optional. Required for S3-compatible providers like Backblaze B2, MinIO, Cloudflare R2 or Wasabi. Leave blank for AWS S3." />
      </div>
      <input id="s3_endpoint" type="text" bind:value={form.config.endpoint} placeholder="e.g. https://s3.us-west-002.backblazeb2.com"
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-muted" />
    </div>
    <div>
      <div class="block text-sm font-medium text-text-muted mb-1.5">
        <label for="s3_base">Base Path</label>
        <Tooltip label="Base Path" text="Optional key prefix prepended to every object Vault writes." />
      </div>
      <input id="s3_base" type="text" bind:value={form.config.base_path} placeholder="e.g. vault"
        class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
    </div>
    <div class="flex items-center gap-2 text-sm text-text-muted">
      <label class="flex items-center gap-2">
        <input type="checkbox" bind:checked={form.config.force_path_style} class="accent-vault" />
        Force path-style addressing
      </label>
      <Tooltip label="Force path-style addressing" text="Puts the bucket name in the URL path instead of the hostname. Some older or self-hosted S3 servers (e.g. older MinIO) need this. Recommended: leave off for AWS S3 and most modern providers." />
    </div>
    <details class="group">
      <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
        Advanced &middot; Transfer
      </summary>
      <div class="flex flex-col gap-3 mt-3">
        <div>
          <div class="block text-sm font-medium text-text-muted mb-1.5">
            <label for="s3_upload_timeout">Upload timeout (minutes)</label>
            <Tooltip label="Upload timeout (minutes)" text="The longest a single file's upload may take before Vault gives up. Too low and large files on slow links get cut off. Recommended: leave at 0 (defaults to 240 min / 4 h); raise it only for very large files over slow connections." />
          </div>
          <input id="s3_upload_timeout" type="number" bind:value={form.config.upload_timeout_minutes} placeholder="240 (default)" min="0"
            class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
        </div>
        <div>
          <div class="block text-sm font-medium text-text-muted mb-1.5">
            <label for="s3_part_size">Part size (MiB)</label>
            <Tooltip label="Part size (MiB)" text="Splits large uploads into parts so one network drop doesn't restart the whole transfer. Bigger parts allow bigger single files. Recommended: leave at 0 (64 MiB, handles files up to ~640 GB); raise only for larger files – 256 → ~2.5 TB, 1024 → ~10 TB. Range 5-5120." />
          </div>
          <input id="s3_part_size" type="number" bind:value={form.config.part_size_mb} placeholder="0 (64 MiB)" min="0" max="5120"
            class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
          <p class="mt-1 text-xs text-text-muted" data-testid="s3-part-memory-hint">
            Each upload stream buffers up to {(((Number(form.config.part_size_mb) || 64) * 5)).toLocaleString()} MiB (part size × 5).
          </p>
        </div>
      </div>
    </details>
  {/if}
  {/key}

  <!-- Universal: bandwidth/write-rate throttling. For network destinations
       this protects the internet uplink; for local destinations it caps the
       disk write rate so streaming apps (Plex, Jellyfin) reading from the
       same array keep their I/O headroom during backups (issue #237). -->
  <div>
    <div class="block text-sm font-medium text-text-muted mb-1.5">
      <label for="bandwidth_limit_mbps">{bandwidthLabel}</label>
      {#if form.type === 'local'}
        <Tooltip label={bandwidthLabel} text="Caps how fast backups WRITE to this destination so apps streaming from the same disks (e.g. Plex or Jellyfin) keep their I/O headroom while a backup runs. Restores and verification always read at full speed. Note the unit is megabits: 8 Mbps = 1 MB/s, so to cap at 100 MB/s enter 800. Recommended: 0 (unlimited) unless playback stutters during backups." />
      {:else}
        <Tooltip label={bandwidthLabel} text="Limits how much bandwidth this destination may use, in megabits per second, so backups don't saturate a shared internet line. Recommended: 0 (unlimited) on a dedicated link; set a cap if backups slow down other traffic." />
      {/if}
    </div>
    <input id="bandwidth_limit_mbps" type="number" bind:value={form.config.bandwidth_limit_mbps} min="0" placeholder="0 (unlimited)"
      class="w-full px-3 py-2 max-lg:min-h-11 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-muted" />
  </div>

  <!-- Universal: deduplication. Top-level column on storage_destinations.
       Immutable after creation – backend ignores any update attempt and
       the UI disables the toggle when editing. -->
  <div class="border-t border-border pt-4">
    <label class="flex items-start gap-2 text-sm">
      <input
        type="checkbox"
        bind:checked={form.dedup_enabled}
        disabled={init !== null}
        class="accent-vault mt-1"
        aria-labelledby="{uid}-dedup-title"
        aria-describedby={init !== null ? `${uid}-dedup-desc ${uid}-dedup-locked` : `${uid}-dedup-desc`}
      />
      <span class="flex-1">
        <span id="{uid}-dedup-title" class="block font-medium text-text">Enable deduplication</span>
        <span id="{uid}-dedup-desc" class="block text-xs text-text-muted mt-0.5">
          Stores only changed data blocks across snapshots and jobs targeting this destination. Recommended for backups containing similar data (Immich, Nextcloud, container volumes). <strong>Cannot be changed after creating the destination.</strong>
        </span>
        {#if init !== null}
          <span id="{uid}-dedup-locked" class="block text-xs text-warning mt-1 italic">
            Dedup mode is locked at creation time. Create a new destination to switch.
          </span>
        {/if}
      </span>
    </label>
  </div>

  <div role="status" class="text-sm empty:hidden" data-testid="storage-test-result">
    {#if formTestResult?.success === false}
      <p class="flex items-start gap-1.5 text-danger">
        <svg aria-hidden="true" class="w-4 h-4 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.warning}/></svg>
        <span class="[overflow-wrap:anywhere]">Connection failed: {formTestResult.error || 'unknown error'}</span>
      </p>
    {:else if formTestResult?.success === true}
      <p class="text-success">Connection successful.</p>
    {/if}
  </div>

  <div class="flex flex-wrap items-center justify-between gap-3 pt-4 border-t border-border">
    <button type="button" onclick={testFormConnection} disabled={formTesting || saving}
      class="inline-flex items-center gap-1.5 px-4 py-2 text-sm font-medium rounded-lg border transition-colors disabled:opacity-50 disabled:cursor-not-allowed
        {formTestResult?.success === true ? 'border-success text-success bg-success/10'
         : formTestResult?.success === false ? 'border-danger text-danger bg-danger/10'
         : 'border-vault/50 text-vault-text hover:bg-vault/10'}">
      {#if formTesting}
        <InlineSpinner size="sm" />
        Testing…
      {:else if formTestResult?.success === true}
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.check}/></svg>
        Connection OK
      {:else if formTestResult?.success === false}
        <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.close}/></svg>
        Failed — retry
      {:else}
        Test connection
      {/if}
    </button>
    <div class="flex gap-3">
      <button type="button" onclick={() => oncancel()} disabled={saving} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
        Cancel
      </button>
      <button type="submit" disabled={saving || formTesting} class="px-4 py-2 text-sm font-medium text-on-vault bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
        {#if saving}Saving...{:else}{submitLabel}{/if}
      </button>
    </div>
  </div>
</form>
