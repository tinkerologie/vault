<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatBytes, formatDate } from '../lib/utils.js'
  import { getWsStatus, connectWs, disconnectWs } from '../lib/ws.svelte.js'
  import { getStyle, setStyle, getMode, setMode } from '../lib/theme.svelte.js'
  import Toast from '../components/Toast.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'
  import Spinner from '../components/Spinner.svelte'
  import PathBrowser from '../components/PathBrowser.svelte'

  let loading = $state(true)
  let health = $state(null)
  /** @type {Record<string, string>} */
  let settings = $state({})
  let saving = $state(false)
  let toast = $state({ message: '', type: 'info', key: 0 })

  // Tab navigation
  /** @type {string} */
  let activeTab = $state('general')
  const tabs = [
    { id: 'general', label: 'General' },
    { id: 'security', label: 'Security' },
    { id: 'notifications', label: 'Notifications' },
    { id: 'reference', label: 'Reference' },
  ]

  // Encryption state
  let encryptionEnabled = $state(false)
  let encPassphrase = $state('')
  let encConfirm = $state('')
  let encSaving = $state(false)
  let showEncPassphrase = $state(false)
  let confirmEncRemoval = $state(false)
  let revealedPassphrase = $state('')
  let showingPassphrase = $state(false)
  let loadingPassphrase = $state(false)
  let changingPassphrase = $state(false)
  let changeNewPass = $state('')
  let changeConfirmPass = $state('')

  // Staging state
  let stagingInfo = $state(null)
  let stagingOverrideInput = $state('')
  let stagingSaving = $state(false)
  let cascadeExpanded = $state(false)

  // Database info state
  let databaseInfo = $state(null)
  let snapshotPathInput = $state('')
  let snapshotPathSaving = $state(false)

  // Discord state
  let discordWebhookUrl = $state('')
  let discordNotifyOn = $state('always')
  let discordSaving = $state(false)
  let discordTesting = $state(false)

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(async () => {
    try {
      const [h, s, enc, staging, dbInfo] = await Promise.all([api.health(), api.getSettings(), api.getEncryptionStatus(), api.getStagingInfo().catch(() => null), api.getDatabaseInfo().catch(() => null)])
      health = h
      settings = s || {}
      encryptionEnabled = enc?.encryption_enabled || false
      stagingInfo = staging
      stagingOverrideInput = staging?.override || ''
      discordWebhookUrl = s?.discord_webhook_url || ''
      discordNotifyOn = s?.discord_notify_on || 'always'
      databaseInfo = dbInfo
      snapshotPathInput = dbInfo?.snapshot_path_override || ''
    } catch (e) {
      console.error('Settings load error:', e)
    } finally {
      loading = false
    }
  })

  async function toggleNotifications() {
    const isEnabled = settings.notifications_enabled !== 'false'
    const newVal = isEnabled ? 'false' : 'true'
    saving = true
    try {
      settings = await api.updateSettings({ notifications_enabled: newVal })
      showToast(newVal === 'true' ? 'Notifications enabled' : 'Notifications disabled', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  async function toggleBackupTarget(key) {
    const isEnabled = settings[key] !== 'false'
    const newVal = isEnabled ? 'false' : 'true'
    saving = true
    try {
      settings = await api.updateSettings({ [key]: newVal })
      const labels = { container_backup_enabled: 'Containers', vm_backup_enabled: 'Virtual Machines', folder_backup_enabled: 'Folders & Files', flash_backup_enabled: 'Flash Drive' }
      showToast(`${labels[key]} backup tracking ${newVal === 'true' ? 'enabled' : 'disabled'}`, 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  function reconnectWebSocket() {
    disconnectWs()
    setTimeout(connectWs, 100)
    showToast('Reconnecting WebSocket...', 'info')
  }

  async function saveDiscordSettings() {
    discordSaving = true
    try {
      settings = await api.updateSettings({
        discord_webhook_url: discordWebhookUrl,
        discord_notify_on: discordNotifyOn,
      })
      showToast('Discord settings saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      discordSaving = false
    }
  }

  async function testDiscord() {
    if (!discordWebhookUrl) {
      showToast('Enter a webhook URL first', 'error')
      return
    }
    discordTesting = true
    try {
      await api.testDiscordWebhook(discordWebhookUrl)
      showToast('Test notification sent to Discord!', 'success')
    } catch (e) {
      showToast('Discord test failed: ' + e.message, 'error')
    } finally {
      discordTesting = false
    }
  }

  async function saveStagingOverride() {
    stagingSaving = true
    try {
      stagingInfo = await api.setStagingOverride(stagingOverrideInput)
      stagingOverrideInput = stagingInfo?.override || ''
      showToast(stagingOverrideInput ? 'Staging path updated' : 'Staging reset to auto', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      stagingSaving = false
    }
  }

  async function resetStagingOverride() {
    stagingOverrideInput = ''
    stagingSaving = true
    try {
      stagingInfo = await api.setStagingOverride('')
      showToast('Staging reset to automatic cascade', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      stagingSaving = false
    }
  }

  async function saveSnapshotPath() {
    snapshotPathSaving = true
    try {
      databaseInfo = await api.setSnapshotPath(snapshotPathInput)
      snapshotPathInput = databaseInfo?.snapshot_path_override || ''
      showToast(snapshotPathInput ? 'Snapshot path updated (takes effect on restart)' : 'Snapshot path reset to default', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      snapshotPathSaving = false
    }
  }

  async function resetSnapshotPath() {
    snapshotPathInput = ''
    snapshotPathSaving = true
    try {
      databaseInfo = await api.setSnapshotPath('')
      showToast('Snapshot path reset to default (takes effect on restart)', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      snapshotPathSaving = false
    }
  }

  async function saveEncryption() {
    if (encPassphrase !== encConfirm) {
      showToast('Passphrases do not match', 'error')
      return
    }
    if (encPassphrase.length < 8) {
      showToast('Passphrase must be at least 8 characters', 'error')
      return
    }
    encSaving = true
    try {
      await api.setEncryption(encPassphrase)
      encryptionEnabled = true
      encPassphrase = ''
      encConfirm = ''
      showToast('Encryption passphrase set', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      encSaving = false
    }
  }

  async function removeEncryption() {
    confirmEncRemoval = true
  }

  async function doRemoveEncryption() {
    confirmEncRemoval = false
    encSaving = true
    try {
      await api.setEncryption('')
      encryptionEnabled = false
      revealedPassphrase = ''
      showingPassphrase = false
      changingPassphrase = false
      showToast('Encryption disabled', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      encSaving = false
    }
  }

  async function downloadEmergencyKit() {
    try {
      const res = await api.getEncryptionPassphrase()
      const date = new Date().toISOString().split('T')[0]
      const host = window.location.hostname
      const content = [
        'VAULT EMERGENCY KIT',
        '====================',
        '',
        `Encryption Passphrase: ${res.passphrase}`,
        '',
        `Created: ${date}`,
        `Server:  ${host}`,
        '',
        'IMPORTANT: Keep this file in a safe place.',
        'You will need this passphrase to restore encrypted backups.',
        'If you lose this passphrase, encrypted backups cannot be recovered.',
        '',
      ].join('\n')
      const blob = new Blob([content], { type: 'text/plain' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vault-emergency-kit-${date}.txt`
      a.click()
      URL.revokeObjectURL(url)
      showToast('Emergency kit downloaded', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function toggleShowPassphrase() {
    if (showingPassphrase) {
      showingPassphrase = false
      revealedPassphrase = ''
      return
    }
    loadingPassphrase = true
    try {
      const res = await api.getEncryptionPassphrase()
      revealedPassphrase = res.passphrase
      showingPassphrase = true
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      loadingPassphrase = false
    }
  }

  function startChangePassphrase() {
    changingPassphrase = true
    changeNewPass = ''
    changeConfirmPass = ''
  }

  function cancelChangePassphrase() {
    changingPassphrase = false
    changeNewPass = ''
    changeConfirmPass = ''
  }

  async function saveChangePassphrase() {
    if (changeNewPass !== changeConfirmPass) {
      showToast('Passphrases do not match', 'error')
      return
    }
    if (changeNewPass.length < 8) {
      showToast('Passphrase must be at least 8 characters', 'error')
      return
    }
    encSaving = true
    try {
      await api.setEncryption(changeNewPass)
      changingPassphrase = false
      changeNewPass = ''
      changeConfirmPass = ''
      revealedPassphrase = ''
      showingPassphrase = false
      showToast('Encryption passphrase changed. Existing backups still require the old passphrase.', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      encSaving = false
    }
  }

  let notificationsOn = $derived(settings.notifications_enabled !== 'false')
  let containerBackupOn = $derived(settings.container_backup_enabled !== 'false')
  let vmBackupOn = $derived(settings.vm_backup_enabled !== 'false')
  let folderBackupOn = $derived(settings.folder_backup_enabled !== 'false')
  let flashBackupOn = $derived(settings.flash_backup_enabled !== 'false')

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text).then(() => {
      showToast('Copied to clipboard', 'success')
    }).catch(() => {
      showToast('Failed to copy', 'error')
    })
  }
</script>

<Toast message={toast.message} type={toast.type} key={toast.key} />

<div>
  <div class="mb-6">
    <h1 class="text-2xl font-bold text-text">Settings</h1>
    <p class="text-sm text-text-muted mt-1">Server information and preferences</p>
  </div>

  {#if loading}
    <Spinner text="Loading settings..." />
  {:else}
    <!-- Tab Navigation -->
    <div class="flex gap-1 border-b border-border mb-6 overflow-x-auto">
      {#each tabs as tab (tab.id)}
        <button
          onclick={() => activeTab = tab.id}
          class="px-4 py-2 text-sm font-medium border-b-2 transition-colors whitespace-nowrap {activeTab === tab.id ? 'border-vault text-vault' : 'border-transparent text-text-muted hover:text-text'}"
        >
          {tab.label}
        </button>
      {/each}
    </div>

    <div class="space-y-6">
      <!-- === GENERAL TAB === -->
      {#if activeTab === 'general'}
      <!-- Appearance / Theme -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Appearance</h2>
        </div>
        <div class="px-5 py-4 space-y-4">
          <!-- Theme Style -->
          <div>
            <p class="text-sm text-text-muted mb-2">Theme style</p>
            <div class="grid grid-cols-4 gap-1 bg-surface-3 rounded-lg p-1 max-w-sm">
              {#each [
                { value: 'default', label: 'Default', icon: 'M7 21a4 4 0 01-4-4V5a2 2 0 012-2h4a2 2 0 012 2v12a4 4 0 01-4 4zm0 0h12a2 2 0 002-2v-4a2 2 0 00-2-2h-2.343M11 7.343l1.657-1.657a2 2 0 012.828 0l2.829 2.829a2 2 0 010 2.828l-8.486 8.485M7 17h.01' },
                { value: '1bit', label: '1-Bit', icon: 'M8 9l4-4 4 4m0 6l-4 4-4-4' },
                { value: '8bit', label: '8-Bit', icon: 'M14 10l-2 1m0 0l-2-1m2 1v2.5M20 7l-2 1m2-1l-2-1m2 1v2.5M14 4l-2-1-2 1M4 7l2-1M4 7l2 1M4 7v2.5M12 21l-2-1m2 1l2-1m-2 1v-2.5M6 18l-2-1v-2.5M18 18l2-1v-2.5' },
                { value: '16bit', label: '16-Bit', icon: 'M11 4a2 2 0 114 0v1a1 1 0 001 1h3a1 1 0 011 1v3a1 1 0 01-1 1h-1a2 2 0 100 4h1a1 1 0 011 1v3a1 1 0 01-1 1h-3a1 1 0 01-1-1v-1a2 2 0 10-4 0v1a1 1 0 01-1 1H7a1 1 0 01-1-1v-3a1 1 0 00-1-1H4a2 2 0 110-4h1a1 1 0 001-1V7a1 1 0 011-1h3a1 1 0 001-1V4z' },
              ] as opt (opt.value)}
                <button
                  type="button"
                  onclick={() => setStyle(/** @type {'default'|'1bit'|'8bit'|'16bit'} */ (opt.value))}
                  class="flex items-center justify-center gap-1.5 px-3 py-2 text-sm rounded-md transition-all {getStyle() === opt.value ? 'bg-vault text-white font-medium shadow-sm' : 'text-text-muted hover:text-text'}"
                >
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={opt.icon}/></svg>
                  {opt.label}
                </button>
              {/each}
            </div>
          </div>

          <!-- Color Mode -->
          <div>
            <p class="text-sm text-text-muted mb-2">Color mode</p>
            <div class="grid grid-cols-3 gap-1 bg-surface-3 rounded-lg p-1 max-w-xs">
              {#each [
                { value: 'light', label: 'Light', icon: 'M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z' },
                { value: 'system', label: 'System', icon: 'M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z' },
                { value: 'dark', label: 'Dark', icon: 'M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z' },
              ] as opt (opt.value)}
                <button
                  type="button"
                  onclick={() => setMode(/** @type {'light'|'system'|'dark'} */ (opt.value))}
                  class="flex items-center justify-center gap-1.5 px-3 py-2 text-sm rounded-md transition-all {getMode() === opt.value ? 'bg-vault text-white font-medium shadow-sm' : 'text-text-muted hover:text-text'}"
                >
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={opt.icon}/></svg>
                  {opt.label}
                </button>
              {/each}
            </div>
          </div>
        </div>
      </div>

      <!-- Backup Targets -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Backup Targets</h2>
          <p class="text-xs text-text-muted mt-0.5">Choose which categories to track for protection status. Disabled categories won't count as unprotected on the Dashboard or Recovery pages.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Containers</p>
              <p class="text-xs text-text-muted mt-0.5">Docker containers on this server</p>
            </div>
            <button
              onclick={() => toggleBackupTarget('container_backup_enabled')}
              disabled={saving}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={containerBackupOn}
              aria-label="Toggle container backup tracking"
            >
              <div class="w-11 h-6 rounded-full transition-colors {containerBackupOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {containerBackupOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Virtual Machines</p>
              <p class="text-xs text-text-muted mt-0.5">Libvirt VMs on this server</p>
            </div>
            <button
              onclick={() => toggleBackupTarget('vm_backup_enabled')}
              disabled={saving}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={vmBackupOn}
              aria-label="Toggle VM backup tracking"
            >
              <div class="w-11 h-6 rounded-full transition-colors {vmBackupOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {vmBackupOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Folders & Files</p>
              <p class="text-xs text-text-muted mt-0.5">Custom folder and file path backups</p>
            </div>
            <button
              onclick={() => toggleBackupTarget('folder_backup_enabled')}
              disabled={saving}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={folderBackupOn}
              aria-label="Toggle folder backup tracking"
            >
              <div class="w-11 h-6 rounded-full transition-colors {folderBackupOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {folderBackupOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Flash Drive</p>
              <p class="text-xs text-text-muted mt-0.5">Unraid USB boot drive backup</p>
            </div>
            <button
              onclick={() => toggleBackupTarget('flash_backup_enabled')}
              disabled={saving}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={flashBackupOn}
              aria-label="Toggle flash drive backup tracking"
            >
              <div class="w-11 h-6 rounded-full transition-colors {flashBackupOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {flashBackupOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
        </div>
      </div>

      {/if}

      <!-- === NOTIFICATIONS TAB === -->
      {#if activeTab === 'notifications'}
      <!-- Notifications -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Notifications</h2>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Unraid Notifications</p>
              <p class="text-xs text-text-muted mt-0.5">Send alerts to the Unraid notification system when backups fail, partially complete, or succeed (based on each job's "Notify On" setting).</p>
            </div>
            <button
              onclick={toggleNotifications}
              disabled={saving}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={notificationsOn}
              aria-label="Toggle notifications"
            >
              <div class="w-11 h-6 rounded-full transition-colors {notificationsOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {notificationsOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          {#if notificationsOn}
            <div class="px-5 py-3">
              <p class="text-xs text-text-dim">
                Each job has its own "Notify On" preference (Always, On Failure, Never) — configure it in the job's advanced settings. This global toggle enables or disables all Vault notifications.
              </p>
            </div>
          {/if}
        </div>
      </div>

      <!-- Discord Notifications -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Discord Notifications</h2>
          <p class="text-xs text-text-muted mt-0.5">Get backup status alerts sent to a Discord channel via webhook.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4">
            <label for="discord-url" class="block text-sm font-medium text-text mb-1.5">Webhook URL</label>
            <div class="flex gap-2">
              <input
                id="discord-url"
                type="url"
                bind:value={discordWebhookUrl}
                placeholder="https://discord.com/api/webhooks/..."
                class="flex-1 text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
              <button
                onclick={testDiscord}
                disabled={discordTesting || !discordWebhookUrl}
                class="px-3 py-2 text-sm font-medium text-text-muted bg-surface-3 border border-border rounded-lg hover:bg-surface-4 transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5 shrink-0"
              >
                {#if discordTesting}
                  <Spinner size="sm" />
                {:else}
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9" /></svg>
                {/if}
                Test
              </button>
            </div>
          </div>
          <div class="px-5 py-4">
            <label for="discord-notify" class="block text-sm font-medium text-text mb-1.5">Notify On</label>
            <select
              id="discord-notify"
              bind:value={discordNotifyOn}
              class="text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="always">All backups (success & failure)</option>
              <option value="failure">Failures only</option>
              <option value="never">Disabled</option>
            </select>
          </div>
          <div class="px-5 py-3 flex justify-end">
            <button
              onclick={saveDiscordSettings}
              disabled={discordSaving}
              class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
            >
              {#if discordSaving}
                <Spinner size="sm" />
              {/if}
              Save Discord Settings
            </button>
          </div>
        </div>
      </div>

      {/if}

      <!-- === SECURITY TAB === -->
      {#if activeTab === 'security'}
      <!-- Encryption -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Encryption</h2>
        </div>
        <div class="divide-y divide-border">
          {#if encryptionEnabled}
            <!-- Description -->
            <div class="px-5 py-4">
              <p class="text-sm text-text-muted leading-relaxed">Keep this passphrase in a safe place, as you will need it to restore your encrypted backups. Download it as an emergency kit file and store it somewhere safe. Encryption keeps your backups private and secure.</p>
            </div>

            <!-- Download emergency kit -->
            <div class="px-5 py-4 flex items-center justify-between gap-4">
              <div>
                <p class="text-sm font-medium text-text">Download emergency kit</p>
                <p class="text-xs text-text-muted mt-0.5">We recommend saving this encryption key somewhere secure.</p>
              </div>
              <button onclick={downloadEmergencyKit} class="flex items-center gap-2 text-sm font-medium text-info hover:text-info/80 transition-colors shrink-0">
                <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" /></svg>
                Download
              </button>
            </div>

            <!-- Show passphrase -->
            <div class="px-5 py-4">
              <div class="flex items-center justify-between gap-4">
                <div>
                  <p class="text-sm font-medium text-text">Show my encryption key</p>
                  <p class="text-xs text-text-muted mt-0.5">Please keep your encryption key private.</p>
                </div>
                <button onclick={toggleShowPassphrase} disabled={loadingPassphrase} class="text-sm font-medium text-info hover:text-info/80 transition-colors shrink-0 disabled:opacity-50">
                  {loadingPassphrase ? 'Loading...' : showingPassphrase ? 'Hide' : 'Show'}
                </button>
              </div>
              {#if showingPassphrase}
                <div class="mt-3 px-3 py-2.5 bg-surface border border-border rounded-lg">
                  <code class="text-sm text-text break-all select-all">{revealedPassphrase}</code>
                </div>
              {/if}
            </div>

            <!-- Change passphrase -->
            <div class="px-5 py-4">
              {#if !changingPassphrase}
                <div class="flex items-center justify-between gap-4">
                  <div>
                    <p class="text-sm font-medium text-text">Change encryption key</p>
                    <p class="text-xs text-text-muted mt-0.5">All future backups will use this encryption key.</p>
                  </div>
                  <button onclick={startChangePassphrase} class="text-sm font-medium text-danger hover:text-danger/80 transition-colors shrink-0">
                    Change
                  </button>
                </div>
              {:else}
                <div class="space-y-3 max-w-sm">
                  <p class="text-sm font-medium text-text">Change encryption key</p>
                  <p class="text-xs text-warning">Existing encrypted backups will still require the old passphrase to restore.</p>
                  <div>
                    <label for="change-pass" class="block text-xs font-medium text-text-muted mb-1">New passphrase</label>
                    <input id="change-pass" type="password" bind:value={changeNewPass} placeholder="Enter new passphrase" class="w-full px-3 py-2 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                  </div>
                  <div>
                    <label for="change-confirm" class="block text-xs font-medium text-text-muted mb-1">Confirm new passphrase</label>
                    <input id="change-confirm" type="password" bind:value={changeConfirmPass} placeholder="Confirm new passphrase" class="w-full px-3 py-2 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                  </div>
                  {#if changeNewPass && changeConfirmPass && changeNewPass !== changeConfirmPass}
                    <p class="text-xs text-danger">Passphrases do not match</p>
                  {/if}
                  <div class="flex items-center gap-2">
                    <button onclick={saveChangePassphrase} disabled={encSaving || !changeNewPass || !changeConfirmPass || changeNewPass !== changeConfirmPass} class="px-4 py-2 text-sm font-medium rounded-lg bg-vault text-white hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
                      {encSaving ? 'Saving...' : 'Update Passphrase'}
                    </button>
                    <button onclick={cancelChangePassphrase} class="px-4 py-2 text-sm font-medium rounded-lg text-text-muted hover:text-text transition-colors">
                      Cancel
                    </button>
                  </div>
                </div>
              {/if}
            </div>

            <!-- Disable encryption -->
            <div class="px-5 py-3 flex justify-end">
              <button onclick={removeEncryption} disabled={encSaving} class="text-xs text-text-dim hover:text-danger transition-colors disabled:opacity-50">
                {encSaving ? 'Removing...' : 'Disable encryption'}
              </button>
            </div>
          {:else}
            <div class="px-5 py-4">
              <div class="flex items-center gap-2 mb-3">
                <span class="inline-block w-2 h-2 rounded-full bg-text-dim"></span>
                <span class="text-sm font-medium text-text">No encryption passphrase set</span>
              </div>
              <p class="text-xs text-text-muted mb-4">Set a global passphrase to enable age encryption for backup jobs. Jobs must individually opt-in to encryption. Existing encrypted backups always require the original passphrase to restore.</p>
              <div class="space-y-3 max-w-sm">
                <div>
                  <label for="enc-pass" class="block text-xs font-medium text-text-muted mb-1">Passphrase</label>
                  <div class="relative">
                    <input id="enc-pass" type={showEncPassphrase ? 'text' : 'password'} bind:value={encPassphrase} placeholder="Enter encryption passphrase" class="w-full px-3 py-2 pr-10 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                    <button type="button" onclick={() => showEncPassphrase = !showEncPassphrase} class="absolute right-2 top-1/2 -translate-y-1/2 text-text-dim hover:text-text p-1" aria-label="Toggle passphrase visibility">
                      <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={showEncPassphrase ? 'M13.875 18.825A10.05 10.05 0 0112 19c-4.478 0-8.268-2.943-9.543-7a9.97 9.97 0 011.563-3.029m5.858.908a3 3 0 114.243 4.243M9.878 9.878l4.242 4.242M9.878 9.878L3 3m6.878 6.878L21 21' : 'M15 12a3 3 0 11-6 0 3 3 0 016 0z M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z'} /></svg>
                    </button>
                  </div>
                </div>
                <div>
                  <label for="enc-confirm" class="block text-xs font-medium text-text-muted mb-1">Confirm Passphrase</label>
                  <input id="enc-confirm" type={showEncPassphrase ? 'text' : 'password'} bind:value={encConfirm} placeholder="Confirm passphrase" class="w-full px-3 py-2 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                </div>
                {#if encPassphrase && encConfirm && encPassphrase !== encConfirm}
                  <p class="text-xs text-danger">Passphrases do not match</p>
                {/if}
                <button onclick={saveEncryption} disabled={encSaving || !encPassphrase || !encConfirm || encPassphrase !== encConfirm} class="px-4 py-2 text-sm font-medium rounded-lg bg-vault text-white hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
                  {encSaving ? 'Saving...' : 'Set Passphrase'}
                </button>
              </div>
            </div>
          {/if}
        </div>
      </div>

      {/if}

      <!-- === GENERAL TAB (cont.) === -->
      {#if activeTab === 'general'}

      <!-- Staging Directory -->
      {#if stagingInfo}
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Staging Directory</h2>
        </div>
        <div class="p-5 space-y-4">
          <div>
            <p class="text-sm text-text font-mono">{stagingInfo.resolved_path}</p>
            <p class="text-xs text-text-muted mt-0.5">
              {stagingInfo.source === 'override' ? 'Custom override' :
               stagingInfo.source === 'cache' ? 'SSD Cache (automatic)' :
               stagingInfo.source === 'local-storage' ? 'Local storage fallback' :
               'System temp fallback'}
            </p>
          </div>

          {#if stagingInfo.disk_total_bytes > 0}
            {@const usedPct = ((stagingInfo.disk_total_bytes - stagingInfo.disk_free_bytes) / stagingInfo.disk_total_bytes) * 100}
            <div>
              <div class="h-2 rounded-full bg-surface overflow-hidden">
                <div class="h-full rounded-full transition-all {usedPct > 90 ? 'bg-danger' : usedPct > 70 ? 'bg-warning' : 'bg-success'}"
                     style="width: {usedPct.toFixed(1)}%"></div>
              </div>
              <p class="text-xs text-text-muted mt-1">
                {formatBytes(stagingInfo.disk_free_bytes)} free of {formatBytes(stagingInfo.disk_total_bytes)}
              </p>
            </div>
          {/if}

          <div>
            <span class="text-xs text-text-muted block mb-1.5">Custom Path (optional)</span>
            <PathBrowser bind:value={stagingOverrideInput} onselect={saveStagingOverride} />
            {#if stagingInfo.override}
              <button onclick={resetStagingOverride} disabled={stagingSaving} class="mt-2 text-xs text-vault hover:underline">
                Reset to automatic
              </button>
            {/if}
          </div>

          <div>
            <button onclick={() => cascadeExpanded = !cascadeExpanded} class="text-xs text-text-muted hover:text-text flex items-center gap-1">
              <svg aria-hidden="true" class="w-3 h-3 transition-transform {cascadeExpanded ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/>
              </svg>
              Cascade order
            </button>
            {#if cascadeExpanded}
              <div class="mt-2 space-y-1 text-xs text-text-muted">
                {#each stagingInfo.cascade as item, i (i)}
                  <div class="flex items-center gap-2">
                    <span>{i + 1}.</span>
                    <span class="font-mono">{item.path}</span>
                    <span>({item.source})</span>
                    {#if item.available}
                      <svg aria-hidden="true" class="w-3.5 h-3.5 text-success" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/>
                      </svg>
                    {:else}
                      <svg aria-hidden="true" class="w-3.5 h-3.5 text-text-muted" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
                      </svg>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      </div>
      {/if}

      <!-- Server Info -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Server Information</h2>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Status</span>
            <span class="text-sm font-medium {health?.status === 'ok' ? 'text-success' : 'text-danger'}">
              {health?.status === 'ok' ? 'Online' : 'Offline'}
            </span>
          </div>
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Version</span>
            <span class="text-sm font-medium text-text">{health?.version || 'Unknown'}</span>
          </div>
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">API Endpoint</span>
            <code class="text-xs bg-surface-3 text-text-muted px-2 py-1 rounded">{window.location.origin}/api/v1</code>
          </div>
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">WebSocket</span>
            <div class="flex items-center gap-2">
              <span class="w-2 h-2 rounded-full {getWsStatus() === 'connected' ? 'bg-success' : getWsStatus() === 'connecting' ? 'bg-warning animate-pulse' : 'bg-danger'}"></span>
              <span class="text-sm text-text capitalize">{getWsStatus()}</span>
              <button onclick={reconnectWebSocket} class="ml-2 text-xs text-vault hover:text-vault-dark transition-colors">Reconnect</button>
            </div>
          </div>
        </div>
      </div>

      {/if}

      <!-- === REFERENCE TAB === -->
      {#if activeTab === 'reference'}
      <!-- Compression Info -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Compression Guide</h2>
        </div>
        <div class="p-5">
          <p class="text-sm text-text-muted mb-4">Each backup job can choose its own compression. Here's how they compare:</p>
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="text-left text-text-muted border-b border-border">
                  <th class="pb-2 pr-4 font-medium">Algorithm</th>
                  <th class="pb-2 pr-4 font-medium">Speed</th>
                  <th class="pb-2 pr-4 font-medium">Ratio</th>
                  <th class="pb-2 font-medium">Best For</th>
                </tr>
              </thead>
              <tbody class="text-text">
                <tr class="border-b border-border/50">
                  <td class="py-2 pr-4 font-medium">
                    Zstandard
                    <span class="ml-1.5 text-[10px] px-1.5 py-0.5 rounded bg-vault/20 text-vault font-semibold uppercase">recommended</span>
                  </td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                    </div>
                  </td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                    </div>
                  </td>
                  <td class="py-2 text-xs text-text-muted">Best all-rounder. Fast compression & decompression with strong ratios. Industry standard for backups.</td>
                </tr>
                <tr class="border-b border-border/50">
                  <td class="py-2 pr-4 font-medium">Gzip</td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                    </div>
                  </td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-info"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                    </div>
                  </td>
                  <td class="py-2 text-xs text-text-muted">Universal compatibility. Moderate speed. Good if restoring on systems without zstd.</td>
                </tr>
                <tr>
                  <td class="py-2 pr-4 font-medium">None</td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                      <div class="w-2 h-2 rounded-full bg-success"></div>
                    </div>
                  </td>
                  <td class="py-2 pr-4">
                    <div class="flex gap-0.5">
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                      <div class="w-2 h-2 rounded-full bg-surface-4"></div>
                    </div>
                  </td>
                  <td class="py-2 text-xs text-text-muted">Fastest backup/restore. Use when storage space is not a concern or data is already compressed.</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <!-- Backup Type Guide -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Backup Type Guide</h2>
        </div>
        <div class="p-5">
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-border">
                  <th class="text-left py-2 pr-4 text-text-muted font-medium">Type</th>
                  <th class="text-left py-2 pr-4 text-text-muted font-medium">Description</th>
                  <th class="text-left py-2 pr-4 text-text-muted font-medium">Speed</th>
                  <th class="text-left py-2 text-text-muted font-medium">Storage</th>
                </tr>
              </thead>
              <tbody class="text-text">
                <tr class="border-b border-border/50">
                  <td class="py-2 pr-4 font-medium">Full</td>
                  <td class="py-2 pr-4">Complete backup every time</td>
                  <td class="py-2 pr-4">Slowest</td>
                  <td class="py-2">Largest</td>
                </tr>
                <tr class="border-b border-border/50">
                  <td class="py-2 pr-4 font-medium">Incremental</td>
                  <td class="py-2 pr-4">Only changes since last backup (any type)</td>
                  <td class="py-2 pr-4">Fastest</td>
                  <td class="py-2">Smallest</td>
                </tr>
                <tr>
                  <td class="py-2 pr-4 font-medium">Differential</td>
                  <td class="py-2 pr-4">Changes since last full backup</td>
                  <td class="py-2 pr-4">Medium</td>
                  <td class="py-2">Medium</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <!-- API Info -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">API Endpoints</h2>
        </div>
        <div class="p-5">
          <div class="space-y-2 text-sm font-mono">
            {#each [
              ['GET', '/api/v1/health', 'Health check'],
              ['GET', '/api/v1/settings', 'Get settings'],
              ['PUT', '/api/v1/settings', 'Update settings'],
              ['GET', '/api/v1/storage', 'List storage destinations'],
              ['POST', '/api/v1/storage', 'Create storage destination'],
              ['GET', '/api/v1/jobs', 'List backup jobs'],
              ['POST', '/api/v1/jobs', 'Create backup job'],
              ['GET', '/api/v1/jobs/:id/history', 'Job run history'],
              ['GET', '/api/v1/jobs/:id/restore-points', 'Restore points'],
              ['WS', '/api/v1/ws', 'WebSocket events'],
            ] as [method, path, desc] (`${method}:${path}`)}
              <div class="flex items-center gap-3">
                <span class="text-xs px-2 py-0.5 rounded font-medium min-w-[3rem] text-center
                  {method === 'GET' ? 'bg-info/20 text-info' :
                   method === 'POST' ? 'bg-success/20 text-success' :
                   method === 'PUT' ? 'bg-warning/20 text-warning' :
                   'bg-vault/20 text-vault'}">{method}</span>
                <span class="text-text-muted">{path}</span>
                <span class="text-text-dim text-xs ml-auto hidden sm:inline">{desc}</span>
              </div>
            {/each}
          </div>
        </div>
      </div>

      {/if}

      <!-- === GENERAL TAB (cont.) === -->
      {#if activeTab === 'general'}

      <!-- Database Location -->
      {#if databaseInfo}
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Database Location</h2>
          <p class="text-xs text-text-muted mt-0.5">Where Vault stores its database and how it protects the USB flash drive.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Mode</span>
            <span class="text-sm font-medium text-text">
              {#if databaseInfo.mode === 'hybrid'}
                Hybrid (RAM + SSD snapshots)
              {:else if databaseInfo.mode === 'direct'}
                Direct SSD
              {:else}
                Legacy USB
              {/if}
            </span>
          </div>
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Working</span>
            <span class="text-sm font-mono text-text truncate ml-4">{databaseInfo.working_path}</span>
          </div>
          {#if databaseInfo.snapshot_path}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Snapshot</span>
            <span class="text-sm font-mono text-text truncate ml-4">{databaseInfo.snapshot_path}</span>
          </div>
          {/if}
          {#if databaseInfo.last_snapshot}
          {@const snapDate = new Date(databaseInfo.last_snapshot)}
          {#if snapDate.getFullYear() > 1970}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Last snapshot</span>
            <span class="text-sm text-text">{formatDate(databaseInfo.last_snapshot)}</span>
          </div>
          {/if}
          {/if}
          {#if databaseInfo.snapshot_size_bytes}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Snapshot size</span>
            <span class="text-sm text-text">{formatBytes(databaseInfo.snapshot_size_bytes)}</span>
          </div>
          {/if}
        </div>
        {#if databaseInfo.mode === 'hybrid'}
        <div class="px-5 py-4 border-t border-border">
          <span class="text-xs text-text-muted block mb-1.5">Custom Snapshot Path (optional)</span>
          <PathBrowser bind:value={snapshotPathInput} onselect={saveSnapshotPath} />
          {#if databaseInfo.snapshot_path_override}
            <button onclick={resetSnapshotPath} disabled={snapshotPathSaving} class="mt-2 text-xs text-vault hover:underline">
              Reset to default
            </button>
          {/if}
          <p class="text-xs text-text-dim mt-2">Changes take effect on next daemon restart.</p>
        </div>
        {/if}
        {#if databaseInfo.mode === 'legacy_usb'}
        <div class="px-5 py-3 bg-amber-500/10 border-t border-amber-500/20">
          <p class="text-xs text-amber-400">No cache drive detected. Database writes go directly to the USB flash drive, which may reduce its lifespan.</p>
        </div>
        {/if}
      </div>
      {/if}

      <!-- About -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">About Vault</h2>
        </div>
        <div class="p-5">
          <p class="text-sm text-text-muted leading-relaxed">
            Vault is a backup and restore manager for Unraid servers. It provides automated backup of Docker containers and libvirt VMs to pluggable storage destinations including local paths, SFTP, SMB/CIFS, and NFS shares.
          </p>
          <div class="mt-4 flex items-center gap-4">
            <a href="https://github.com/ruaan-deysel/vault" target="_blank" rel="noopener"
              class="text-sm text-vault hover:text-vault-dark transition-colors flex items-center gap-2">
              <svg aria-hidden="true" class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"/></svg>
              GitHub Repository
            </a>
          </div>
        </div>
      </div>
      {/if}
    </div>
  {/if}
</div>

<ConfirmDialog
  show={confirmEncRemoval}
  title="Remove Encryption"
  message="Remove encryption passphrase? Existing encrypted backups will still require the original passphrase to restore."
  confirmLabel="Remove"
  variant="danger"
  onconfirm={doRemoveEncryption}
  oncancel={() => { confirmEncRemoval = false }}
/>
