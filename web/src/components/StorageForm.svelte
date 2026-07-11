<script>
  import { api } from '../lib/api.js'
  import { parseConfig } from '../lib/utils.js'
  import PathBrowser from './PathBrowser.svelte'
  import Tooltip from './Tooltip.svelte'

  let {
    initial = null, // existing destination object when editing, null when adding
    submitLabel = 'Add Storage',
    onsaved = () => {}, // called with the created/updated destination
    oncancel = () => {},
    ontoast = () => {}, // (message, type) — routed to the host page's Toast
  } = $props()

  function defaultForm() {
    return {
      name: '',
      type: 'local',
      config: { path: '' },
      dedup_enabled: false,
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
    return {
      name: dest.name,
      type: dest.type,
      config: cfg,
      dedup_enabled: !!dest.dedup_enabled,
      // Carry the DB-backup fan-out flag through the modal save so editing
      // an unrelated field doesn't reset it. The dedicated row toggle on
      // the card is still the primary control.
      backup_database_enabled: !!dest.backup_database_enabled,
    }
  }

  // `initial` is deliberately read once at mount: the host remounts this
  // component each time the modal opens, so the form never needs to track
  // later changes to the prop. Consumers that can change `initial` while the
  // component stays mounted must remount it instead (wrap in {#key}).
  // svelte-ignore state_referenced_locally
  const init = initial
  let form = $state(initForm(init))
  let saving = $state(false)

  async function saveStorage() {
    if (saving) return
    saving = true
    try {
      const payload = {
        name: form.name,
        type: form.type,
        config: JSON.stringify(form.config),
        // Top-level: stored as its own column on storage_destinations.
        // Immutable after creation (UI gates the toggle when editing).
        dedup_enabled: !!form.dedup_enabled,
        // Preserve the current DB fan-out value through modal saves so
        // editing other fields doesn't accidentally disable it. New
        // destinations default to false.
        backup_database_enabled: !!form.backup_database_enabled,
      }
      let result
      if (init) {
        result = await api.updateStorage(init.id, payload)
      } else {
        result = await api.createStorage(payload)
      }
      onsaved(result)
    } catch (e) {
      ontoast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  function applyType(nextType) {
    // Re-clicking the selected type must not wipe entered credentials.
    if (nextType === form.type) return
    const defaults = {
      local: { path: '' },
      sftp: { host: '', port: 22, user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      smb: { host: '', share: '', user: '', password: '', base_path: '', bandwidth_limit_mbps: 0 },
      nfs: { host: '', export: '', base_path: '', version: '4', options: '', bandwidth_limit_mbps: 0 },
      webdav: { url: '', username: '', password: '', base_path: '', insecure_skip_verify: false, timeout_seconds: 0, stall_timeout_seconds: 300, chunk_size_mb: 0, bandwidth_limit_mbps: 0 },
      s3: { bucket: '', region: '', access_key: '', secret_key: '', endpoint: '', base_path: '', force_path_style: false, upload_timeout_minutes: 0, part_size_mb: 0, bandwidth_limit_mbps: 0 },
    }
    // Reassign the full form object so Svelte always re-renders the keyed
    // config block when switching destination type.
    form = {
      ...form,
      type: nextType,
      config: defaults[nextType] || {},
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
    { value: 's3', label: 'S3 / S3-Compatible' },
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
      const result = await api.testStorageConfig({ type: form.type, config: JSON.stringify(form.config) })
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

  const storageIcons = {
    local: 'M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z',
    sftp: 'M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z',
    smb: 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z',
    nfs: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
    webdav: 'M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z',
    s3: 'M5 19a2 2 0 01-2-2V7a2 2 0 012-2h3.586a1 1 0 01.707.293l1.414 1.414A1 1 0 0011.414 7H19a2 2 0 012 2v8a2 2 0 01-2 2H5z',
  }

  const storageColors = {
    local: 'text-blue-400',
    sftp: 'text-emerald-400',
    smb: 'text-purple-400',
    nfs: 'text-amber-400',
    webdav: 'text-cyan-400',
    s3: 'text-orange-400',
  }
</script>

<form onsubmit={(e) => { e.preventDefault(); saveStorage() }} class="space-y-5">
  <div>
    <label for="sname" class="block text-sm font-medium text-text-muted mb-1.5">Name</label>
    <input id="sname" type="text" bind:value={form.name} required
      class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="My Backup Target" />
  </div>

  <div>
    <span class="block text-sm font-medium text-text-muted mb-1.5">Type</span>
    <div role="group" aria-label="Storage type" class="grid grid-cols-2 sm:grid-cols-3 gap-2">
      {#each storageTypes as t (t.value)}
        <button type="button" aria-pressed={form.type === t.value}
          onclick={() => applyType(t.value)}
          class="flex items-center gap-2 px-3 py-2.5 rounded-lg border text-sm text-left transition-colors
            {form.type === t.value ? 'border-vault bg-vault/10 text-text' : 'border-border bg-surface-3 text-text-muted hover:border-border-hover hover:text-text'}">
          <svg aria-hidden="true" class="w-5 h-5 shrink-0 {storageColors[t.value] || 'text-text-muted'}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d={storageIcons[t.value]}/></svg>
          <span class="font-medium truncate">{t.label}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- Dynamic config fields per type -->
  {#key form.type}
  {#if form.type === 'local'}
    <div>
      <span class="block text-sm font-medium text-text-muted mb-1.5">Path</span>
      <PathBrowser bind:value={form.config.path} />
    </div>
  {:else if form.type === 'sftp'}
    <div class="grid grid-cols-3 gap-3">
      <div class="col-span-2">
        <label for="cfg_host" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
        <input id="cfg_host" type="text" bind:value={form.config.host}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="192.168.1.100" />
      </div>
      <div>
        <label for="cfg_port" class="block text-sm font-medium text-text-muted mb-1.5">Port</label>
        <input id="cfg_port" type="number" min="1" max="65535" bind:value={form.config.port}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text" placeholder="22" />
      </div>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="cfg_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="cfg_user" type="text" bind:value={form.config.user}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="cfg_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
        <input id="cfg_pass" type="password" autocomplete="off" bind:value={form.config.password}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    </div>
    <div>
      <label for="cfg_spath" class="block text-sm font-medium text-text-muted mb-1.5">Remote Path <Tooltip text="Absolute path on the SFTP server where Vault will store backups. The directory must exist and the user must have write permission." /></label>
      <input id="cfg_spath" type="text" bind:value={form.config.base_path}
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="/backups/vault" />
    </div>
  {:else if form.type === 'smb'}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="cfg_smbhost" class="block text-sm font-medium text-text-muted mb-1.5">Host</label>
        <input id="cfg_smbhost" type="text" bind:value={form.config.host}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="192.168.1.100" />
      </div>
      <div>
        <label for="cfg_share" class="block text-sm font-medium text-text-muted mb-1.5">Share <Tooltip text="The top-level SMB share name as configured on the server (e.g. Backups). Use the Path field below for a sub-folder within the share." /></label>
        <input id="cfg_share" type="text" bind:value={form.config.share}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" placeholder="Backups" />
      </div>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="cfg_smbuser" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="cfg_smbuser" type="text" bind:value={form.config.user}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="cfg_smbpass" class="block text-sm font-medium text-text-muted mb-1.5">Password</label>
        <input id="cfg_smbpass" type="password" autocomplete="off" bind:value={form.config.password}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    </div>
    <div>
      <label for="cfg_smbpath" class="block text-sm font-medium text-text-muted mb-1.5">Path</label>
      <input id="cfg_smbpath" type="text" bind:value={form.config.base_path}
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" placeholder="vault" />
    </div>
  {:else if form.type === 'nfs'}
    <div class="grid grid-cols-2 gap-3">
      <div class="col-span-2">
        <label for="nfs_host" class="block text-sm font-medium text-text-muted mb-1.5">NFS Host</label>
        <input id="nfs_host" type="text" bind:value={form.config.host} placeholder="192.168.1.100"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div class="col-span-2">
        <label for="nfs_export" class="block text-sm font-medium text-text-muted mb-1.5">Export Path <Tooltip text="The path the NFS server exports – matches the entry in /etc/exports on the server (e.g. /mnt/user/backups). This is what gets mounted, not a sub-path within it." /></label>
        <input id="nfs_export" type="text" bind:value={form.config.export} placeholder="/mnt/user/backups"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="nfs_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional sub-directory within the mounted export where Vault will write its data. Leave blank to use the export root directly." /></label>
        <input id="nfs_base" type="text" bind:value={form.config.base_path} placeholder="vault"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="nfs_version" class="block text-sm font-medium text-text-muted mb-1.5">NFS Version <Tooltip text="NFSv3: wider compatibility, simpler setup. NFSv4: better security and performance, but may require DNS and auth configuration." /></label>
        <select id="nfs_version" bind:value={form.config.version}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text">
          <option value="3">NFSv3</option>
          <option value="4">NFSv4</option>
        </select>
      </div>
      <div class="col-span-2">
        <label for="nfs_options" class="block text-sm font-medium text-text-muted mb-1.5">Mount Options <Tooltip text="Optional NFS mount flags such as rw, sync, hard, soft, or nolock. Leave blank for sensible defaults." /></label>
        <input id="nfs_options" type="text" bind:value={form.config.options} placeholder="Optional: rw,sync"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    </div>
  {:else if form.type === 'webdav'}
    <div>
      <label for="dav_url" class="block text-sm font-medium text-text-muted mb-1.5">Server URL <Tooltip text="Full URL to the WebDAV endpoint, e.g. https://nextcloud.example.com/remote.php/dav/files/username/" /></label>
      <input id="dav_url" type="url" bind:value={form.config.url} placeholder="https://webdav.example.com/"
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="dav_user" class="block text-sm font-medium text-text-muted mb-1.5">Username</label>
        <input id="dav_user" type="text" bind:value={form.config.username}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="dav_pass" class="block text-sm font-medium text-text-muted mb-1.5">Password / App Token</label>
        <input id="dav_pass" type="password" autocomplete="off" bind:value={form.config.password}
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    </div>
    <div>
      <label for="dav_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional sub-folder under the server URL where Vault will write its data." /></label>
      <input id="dav_base" type="text" bind:value={form.config.base_path} placeholder="vault"
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
    </div>
    <label class="flex items-center gap-2 text-sm text-text-muted">
      <input type="checkbox" bind:checked={form.config.insecure_skip_verify} class="accent-vault" />
      Allow self-signed TLS certificates
      <Tooltip text="Skip TLS certificate validation. Only enable for trusted private servers using self-signed certificates." />
    </label>
    <details class="group">
      <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
        Advanced &middot; Transfer
      </summary>
      <div class="flex flex-col gap-3 mt-3">
        <div>
          <label for="dav_chunk" class="block text-sm font-medium text-text-muted mb-1.5">
            Chunk size (MiB)
            <Tooltip text="Splits large uploads into separate pieces so one dropped connection doesn't restart the whole file. Recommended: leave at 0 (uses 50 MiB pieces). Use -1 only if your server rejects chunked uploads." />
          </label>
          <input id="dav_chunk" type="number" bind:value={form.config.chunk_size_mb} placeholder="0 (50 MiB)" min="-1"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="dav_stall" class="block text-sm font-medium text-text-muted mb-1.5">
            Stall timeout (seconds)
            <Tooltip text="Gives up on an upload if no data moves for this many seconds – catches a silently stalled connection. The timer resets whenever data flows, so even huge files finish fine. Recommended: 300 (5 min); use -1 to disable." />
          </label>
          <input id="dav_stall" type="number" bind:value={form.config.stall_timeout_seconds} placeholder="300" min="-1"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="dav_overall" class="block text-sm font-medium text-text-muted mb-1.5">
            Overall request timeout (seconds)
            <Tooltip text="A hard time limit on every request, including a whole file upload. Set too low, it cuts off large uploads. Recommended: leave at 0 (no limit) – the stall timeout above already handles dead connections." />
          </label>
          <input id="dav_overall" type="number" bind:value={form.config.timeout_seconds} placeholder="0 (unlimited)" min="0"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
    </details>
  {:else if form.type === 's3'}
    <div>
      <span class="block text-sm font-medium text-text-muted mb-1.5">Provider preset <Tooltip text="Prefills endpoint and region for common S3 providers. You can still edit any field. Placeholders in <angle-brackets> must be filled in with your account details." /></span>
      <div class="flex flex-wrap gap-2">
        {#each s3Presets as p (p.label)}
          <button type="button" onclick={() => applyS3Preset(p)}
            class="px-2.5 py-1 text-xs font-medium rounded-full border border-border bg-surface-3 text-text-muted hover:border-vault/40 hover:text-text transition-colors">
            {p.label}
          </button>
        {/each}
      </div>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="s3_bucket" class="block text-sm font-medium text-text-muted mb-1.5">Bucket</label>
        <input id="s3_bucket" type="text" bind:value={form.config.bucket} placeholder="my-vault-backups"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
      <div>
        <label for="s3_region" class="block text-sm font-medium text-text-muted mb-1.5">Region <Tooltip text="AWS region code, e.g. us-east-1. For S3-compatible providers, use the region required by the provider." /></label>
        <input id="s3_region" type="text" bind:value={form.config.region} placeholder="us-east-1"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
      </div>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="s3_ak" class="block text-sm font-medium text-text-muted mb-1.5">Access Key ID</label>
        <input id="s3_ak" type="text" bind:value={form.config.access_key} autocomplete="off"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
      </div>
      <div>
        <label for="s3_sk" class="block text-sm font-medium text-text-muted mb-1.5">Secret Access Key</label>
        <input id="s3_sk" type="password" bind:value={form.config.secret_key} autocomplete="off"
          class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
      </div>
    </div>
    <div>
      <label for="s3_endpoint" class="block text-sm font-medium text-text-muted mb-1.5">Endpoint <Tooltip text="Optional. Required for S3-compatible providers like Backblaze B2, MinIO, Cloudflare R2 or Wasabi. Leave blank for AWS S3." /></label>
      <input id="s3_endpoint" type="text" bind:value={form.config.endpoint} placeholder="https://s3.us-west-002.backblazeb2.com"
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text font-mono placeholder-text-dim" />
    </div>
    <div>
      <label for="s3_base" class="block text-sm font-medium text-text-muted mb-1.5">Base Path <Tooltip text="Optional key prefix prepended to every object Vault writes." /></label>
      <input id="s3_base" type="text" bind:value={form.config.base_path} placeholder="vault"
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
    </div>
    <label class="flex items-center gap-2 text-sm text-text-muted">
      <input type="checkbox" bind:checked={form.config.force_path_style} class="accent-vault" />
      Force path-style addressing
      <Tooltip text="Puts the bucket name in the URL path instead of the hostname. Some older or self-hosted S3 servers (e.g. older MinIO) need this. Recommended: leave off for AWS S3 and most modern providers." />
    </label>
    <details class="group">
      <summary class="text-sm font-medium text-text-muted hover:text-text cursor-pointer select-none">
        Advanced &middot; Transfer
      </summary>
      <div class="flex flex-col gap-3 mt-3">
        <div>
          <label for="s3_upload_timeout" class="block text-sm font-medium text-text-muted mb-1.5">
            Upload timeout (minutes)
            <Tooltip text="The longest a single file's upload may take before Vault gives up. Too low and large files on slow links get cut off. Recommended: leave at 0 (defaults to 240 min / 4 h); raise it only for very large files over slow connections." />
          </label>
          <input id="s3_upload_timeout" type="number" bind:value={form.config.upload_timeout_minutes} placeholder="240 (default)" min="0"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
        <div>
          <label for="s3_part_size" class="block text-sm font-medium text-text-muted mb-1.5">
            Part size (MiB)
            <Tooltip text="Splits large uploads into parts so one network drop doesn't restart the whole transfer. Bigger parts allow bigger single files. Recommended: leave at 0 (64 MiB, handles files up to ~640 GB); raise only for larger files – 256 → ~2.5 TB, 1024 → ~10 TB. Range 5-5120." />
          </label>
          <input id="s3_part_size" type="number" bind:value={form.config.part_size_mb} placeholder="0 (64 MiB)" min="0" max="5120"
            class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
        </div>
      </div>
    </details>
  {/if}
  {/key}

  <!-- Universal (remote only): bandwidth throttling. Local destinations
       talk directly to the host's filesystem; there is no upstream link
       to protect, so the field is hidden + the backend factory skips the
       throttle wrapper for `type === 'local'`. -->
  {#if form.type !== 'local'}
    <div>
      <label for="bandwidth_limit_mbps" class="block text-sm font-medium text-text-muted mb-1.5">
        Bandwidth limit (Mbps)
        <Tooltip text="Limits how much bandwidth this destination may use, in megabits per second, so backups don't saturate a shared internet line. Recommended: 0 (unlimited) on a dedicated link; set a cap if backups slow down other traffic." />
      </label>
      <input id="bandwidth_limit_mbps" type="number" bind:value={form.config.bandwidth_limit_mbps} min="0" placeholder="0 (unlimited)"
        class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder-text-dim" />
    </div>
  {/if}

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
      />
      <span class="flex-1">
        <span class="block font-medium text-text">Enable deduplication</span>
        <span class="block text-xs text-text-muted mt-0.5">
          Stores only changed data blocks across snapshots and jobs targeting this destination. Recommended for backups containing similar data (Immich, Nextcloud, container volumes). <strong>Cannot be changed after creating the destination.</strong>
        </span>
        {#if init !== null}
          <span class="block text-xs text-warning mt-1 italic">
            Dedup mode is locked at creation time. Create a new destination to switch.
          </span>
        {/if}
      </span>
    </label>
  </div>

  <div class="flex items-center justify-between gap-3 pt-4 border-t border-border">
    <button type="button" onclick={testFormConnection} disabled={formTesting || saving}
      class="inline-flex items-center gap-1.5 px-4 py-2 text-sm font-medium rounded-lg border transition-colors disabled:opacity-50 disabled:cursor-not-allowed
        {formTestResult?.success === true ? 'border-success text-success bg-success/10'
         : formTestResult?.success === false ? 'border-danger text-danger bg-danger/10'
         : 'border-vault/50 text-vault-text hover:bg-vault/10'}">
      {#if formTesting}
        <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
        Testing…
      {:else if formTestResult?.success === true}
        <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
        Connection OK
      {:else if formTestResult?.success === false}
        <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
        Failed — retry
      {:else}
        Test connection
      {/if}
    </button>
    <div class="flex gap-3">
      <button type="button" onclick={() => oncancel()} disabled={saving} class="px-4 py-2 text-sm font-medium text-text-muted hover:text-text bg-surface-3 hover:bg-surface-4 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
        Cancel
      </button>
      <button type="submit" disabled={saving || formTesting} class="px-4 py-2 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
        {#if saving}Saving...{:else}{submitLabel}{/if}
      </button>
    </div>
  </div>
</form>
