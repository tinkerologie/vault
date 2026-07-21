<script>
  import { onMount } from 'svelte'
  import { api, isReplicaMode } from '../lib/api.js'
  import { formatBytes, formatDate, snapshotMigrationMessage } from '../lib/utils.js'
  import { copyText } from '../lib/clipboard.js'
  import { getStyle, setStyle, getMode, setMode } from '../lib/theme.svelte.js'
  import Toast from '../components/Toast.svelte'
  import ConfirmDialog from '../components/ConfirmDialog.svelte'
  import Spinner from '../components/Spinner.svelte'
  import InlineSpinner from '../components/InlineSpinner.svelte'
  import PathBrowser from '../components/PathBrowser.svelte'
  import Tooltip from '../components/Tooltip.svelte'
  import RetryDelaysEditor from '../components/RetryDelaysEditor.svelte'
  import ChangelogModal from '../components/ChangelogModal.svelte'
  import { setAnomalyEnabled, setReplicationEnabled } from '../lib/settings.svelte.js'

  let loading = $state(true)
  let health = $state(null)
  /** @type {Record<string, string>} */
  let settings = $state({})
  let saving = $state(false)
  let toast = $state({ message: '', type: 'info', key: 0 })

  // Replica instances are read-only: keep general/notification config visible
  // but non-editable so operators can still read the current values.
  const readOnly = isReplicaMode()

  // Tab navigation
  /** @type {string} */
  let activeTab = $state('general')
  const tabs = [
    { id: 'general', label: 'General' },
    { id: 'security', label: 'Security' },
    { id: 'notifications', label: 'Notifications' },
    { id: 'reference', label: 'Reference' },
  ]

  // Jump-list for the long General tab (issue #208 / E11). Everyday settings up
  // front, power-user knobs after; clicking scrolls to the section's card.
  const generalSections = [
    { id: 'set-appearance', label: 'Appearance' },
    { id: 'set-targets', label: 'Backup Targets' },
    { id: 'set-retry', label: 'Retry Policy' },
    { id: 'set-throttle', label: 'Upload Throttling' },
    { id: 'set-dedup', label: 'Dedup' },
    { id: 'set-anomaly', label: 'Anomaly Detection' },
    { id: 'set-logging', label: 'Storage Logging' },
    { id: 'set-history', label: 'History Retention' },
    { id: 'set-server', label: 'Server Info' },
    { id: 'set-database', label: 'Database' },
    { id: 'set-diagnostics', label: 'Diagnostics' },
    { id: 'set-about', label: 'About' },
  ]
  function jumpToSetting(id) {
    const el = document.getElementById(id)
    if (!el) return
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    // Scroll ONLY the <main> scroll container. scrollIntoView() also scrolls the
    // document — a programmatically-scrollable overflow:hidden box on tall pages
    // — shoving the whole app up so the sticky Jump-to bar leaves the viewport
    // and empty space shows below (the Diagnostics-jump bug). Offset by the
    // sticky bar's height so the target isn't hidden under it.
    const main = el.closest('main')
    if (main) {
      const bar = main.querySelector('.sticky')
      const offset = bar ? bar.offsetHeight : 0
      const top = main.scrollTop + el.getBoundingClientRect().top - main.getBoundingClientRect().top - offset
      main.scrollTo({ top: Math.max(0, top), behavior: reduce ? 'auto' : 'smooth' })
    } else {
      el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
    }
    // Move focus so keyboard / screen-reader users land in the target section
    // (preventScroll so focusing can't re-scroll the document).
    el.setAttribute('tabindex', '-1')
    el.focus({ preventScroll: true })
  }

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

  // Database info state
  let databaseInfo = $state(null)
  let snapshotPathInput = $state('')
  let snapshotPathSaving = $state(false)

  // History retention state
  let historyRetention = $state('365')
  let historyRetentionSaving = $state(false)

  // Discord state
  let discordWebhookUrl = $state('')
  let discordNotifyOn = $state('always')
  let discordBotUsername = $state('')
  let discordBotAvatarUrl = $state('')
  let discordMentionRoleId = $state('')
  let discordMentionOn = $state('never')
  let discordSaving = $state(false)
  let discordTesting = $state(false)

  // Diagnostics state
  let diagnosticsDownloading = $state(false)

  // Retry policy state (Task 11 – resilience hardening). Backend stores the
  // delays as a JSON-encoded array string in the settings table. The
  // RetryDelaysEditor component handles parsing/serialising and only ever
  // emits a valid JSON array string (or '' for "unset"), so no client-side
  // validator is needed any more.
  let retryMax = $state('')
  // Adaptive upload throttle (issue #237)
  let autoThrottleEnabled = $state(false)
  let autoThrottleLink = $state('')
  let autoThrottleFloor = $state('')
  let autoThrottleSaving = $state(false)
  let retryDelays = $state('')
  let retrySaving = $state(false)

  // Compaction threshold state. Backend stores as a fractional string "0.0".."1.0";
  // the UI exposes it as an integer percentage 0..100.
  let compactionThreshold = $state('')
  let compactionSaving = $state(false)

  // API Key state
  let apiKeyEnabled = $state(false)
  let apiKeyRevealed = $state('')
  let apiKeyShowing = $state(false)
  let apiKeyLoading = $state(false)
  let apiKeyGenerating = $state(false)
  let apiKeyRevoking = $state(false)
  let confirmApiKeyRevoke = $state(false)

  // About card state (merged into the existing "About Vault" card).
  /** @type {Array<{ version: string, date?: string, sections: Record<string, string[]> }>} */
  let releases = $state([])
  /** @type {{ tag: string, published_at: string, url: string } | null} */
  let latest = $state(null)
  let currentVersion = $state('dev')
  let aboutLoading = $state(true)
  let aboutModalOpen = $state(false)

  // Strip a leading "v" so a daemon-reported "2026.05.02" matches a
  // GitHub tag like "v2026.05.02" before comparing.
  /** @param {string | undefined} v */
  function normalizeVersion(v) {
    if (!v) return ''
    return String(v).replace(/^v/i, '')
  }

  const aboutStatus = $derived.by(() => {
    if (currentVersion === 'dev') {
      return { kind: 'dev', label: 'Development build', note: '' }
    }
    if (latest === null) {
      return { kind: 'unknown', label: '', note: 'Update status unknown.' }
    }
    // YYYY.MM.PATCH versions are zero-padded so plain string compare is
    // chronologically correct (e.g. "2026.05.03" < "2026.05.10"). Use
    // ordering instead of equality so a daemon ahead of the latest
    // GitHub release (freshly-tagged build before the release publishes,
    // or a development build) is treated as up-to-date / pre-release
    // rather than the misleading "Update available".
    const cur = normalizeVersion(currentVersion)
    const tag = normalizeVersion(latest.tag)
    if (cur === tag) {
      return { kind: 'ok', label: 'Up to date', note: '' }
    }
    if (cur > tag) {
      return { kind: 'ok', label: 'Pre-release', note: `Latest published: ${latest.tag}` }
    }
    return { kind: 'update', label: 'Update available', note: `Latest: ${latest.tag}` }
  })

  const aboutReleasedNote = $derived.by(() => {
    if (!latest) return ''
    const d = new Date(latest.published_at)
    if (Number.isNaN(d.getTime())) return ''
    const diff = Math.max(0, Math.round((Date.now() - d.getTime()) / 86400000))
    if (diff === 0) return 'Released today.'
    if (diff === 1) return 'Released 1 day ago.'
    return `Released ${diff} days ago.`
  })

  const aboutBadgeClass = $derived(
    aboutStatus.kind === 'ok'
      ? 'bg-emerald-500/15 text-emerald-400'
      : aboutStatus.kind === 'update'
        ? 'bg-orange-500/15 text-orange-400'
        : 'bg-surface text-text-muted',
  )

  function showToast(message, type = 'info') {
    toast = { message, type, key: toast.key + 1 }
  }

  onMount(async () => {
    try {
      const [h, s, enc, staging, dbInfo, apiKeyStatus, changelog, latestRelease] = await Promise.all([
        api.health(),
        api.getSettings(),
        api.getEncryptionStatus(),
        api.getStagingInfo().catch(() => null),
        api.getDatabaseInfo().catch(() => null),
        api.getAPIKeyStatus().catch(() => null),
        api.getChangelog().catch(() => []),
        api.getLatestRelease().catch(() => null),
      ])
      health = h
      settings = s || {}
      encryptionEnabled = enc?.encryption_enabled || false
      apiKeyEnabled = apiKeyStatus?.enabled || false
      stagingInfo = staging
      stagingOverrideInput = staging?.override || ''
      discordWebhookUrl = s?.discord_webhook_url || ''
      discordNotifyOn = s?.discord_notify_on || 'always'
      discordBotUsername = s?.discord_bot_username || ''
      discordBotAvatarUrl = s?.discord_bot_avatar_url || ''
      discordMentionRoleId = s?.discord_mention_role_id || ''
      discordMentionOn = s?.discord_mention_on || 'never'
      databaseInfo = dbInfo
      snapshotPathInput = dbInfo?.snapshot_path_override || ''
      historyRetention = String(s.history_retention_days ?? '365')
      retryMax = s?.retry_max_default ?? ''
      retryDelays = s?.retry_delays_default ?? ''
      autoThrottleEnabled = s?.auto_throttle_enabled === 'true'
      autoThrottleLink = s?.auto_throttle_link_mbps ?? ''
      autoThrottleFloor = s?.auto_throttle_floor_mbps ?? ''
      // Anomaly detection settings (Task 19)
      anomalyEnabled = s?.anomaly_detection_enabled !== 'false'
      anomalySensitivityDefault = s?.anomaly_sensitivity_default || 'balanced'
      anomalyNotifyMinSeverity = s?.anomaly_notify_min_severity || 'critical'
      // Replication enable (mirrors settings.svelte.js derive).
      if (s?.replication_enabled === 'true') {
        replicationEnabledSetting = true
      } else if (s?.replication_enabled === 'false') {
        replicationEnabledSetting = false
      } else {
        const replSources = await api.listReplicationSources().catch(() => [])
        replicationEnabledSetting = Array.isArray(replSources) && replSources.length > 0
      }
      // Storage verbose logging (Task 12 – storage resilience)
      storageVerboseLogging = s?.storage_verbose_logging === 'true'
      // Stored as a fraction "0.0".."1.0"; UI shows it as an integer percentage 0..100.
      const rawRatio = s?.dedup_compaction_min_dead_ratio
      if (rawRatio !== undefined && rawRatio !== null && rawRatio !== '') {
        const f = Number.parseFloat(rawRatio)
        if (Number.isFinite(f)) {
          compactionThreshold = String(Math.round(f * 100))
        }
      }
      currentVersion = (h && h.version) || 'dev'
      releases = changelog
      latest = latestRelease
    } catch (e) {
      console.error('Settings load error:', e)
    } finally {
      loading = false
      aboutLoading = false
    }
  })

  async function toggleNotifications() {
    if (readOnly) return
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
    if (readOnly) return
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

  async function toggleBackupRule() {
    if (readOnly) return
    const newVal = settings.backup_rule_enabled === 'false' ? 'true' : 'false'
    saving = true
    try {
      settings = await api.updateSettings({ backup_rule_enabled: newVal })
      showToast(`3-2-1 Backup Rule ${newVal === 'true' ? 'shown' : 'hidden'} on Dashboard`, 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      saving = false
    }
  }

  async function saveDiscordSettings() {
    if (readOnly) return
    discordSaving = true
    try {
      settings = await api.updateSettings({
        discord_webhook_url: discordWebhookUrl,
        discord_notify_on: discordNotifyOn,
        discord_bot_username: discordBotUsername,
        discord_bot_avatar_url: discordBotAvatarUrl,
        discord_mention_role_id: discordMentionRoleId,
        discord_mention_on: discordMentionOn,
      })
      showToast('Discord settings saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      discordSaving = false
    }
  }

  async function testDiscord() {
    if (readOnly) return
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
    if (readOnly) return
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
    if (readOnly) return
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
      const { text, tone } = snapshotMigrationMessage(
        databaseInfo,
        snapshotPathInput ? 'Snapshot path updated' : 'Snapshot path reset to default',
      )
      showToast(text, tone)
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
      const { text, tone } = snapshotMigrationMessage(databaseInfo, 'Snapshot path reset to default')
      showToast(text, tone)
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      snapshotPathSaving = false
    }
  }

  async function saveHistoryRetention() {
    if (readOnly) return
    historyRetentionSaving = true
    try {
      settings = await api.updateSettings({ history_retention_days: historyRetention })
      showToast('History retention saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      historyRetentionSaving = false
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
      // Defer revocation: some browsers start the download asynchronously
      // after a.click(), and revoking the URL synchronously can race the
      // download on slow disks.
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      showToast('Emergency kit downloaded', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    }
  }

  async function saveCompactionThreshold() {
    if (readOnly) return
    compactionSaving = true
    try {
      const str = String(compactionThreshold).trim()
      if (str === '') {
        showToast('Nothing to save', 'info')
        return
      }
      const n = Number.parseInt(str, 10)
      if (!Number.isInteger(n) || n < 0 || n > 100) {
        showToast('Compaction threshold must be an integer between 0 and 100', 'error')
        return
      }
      const value = (n / 100).toFixed(2) // "0.50"
      settings = await api.updateSettings({ dedup_compaction_min_dead_ratio: value })
      const rawRatio = settings?.dedup_compaction_min_dead_ratio
      if (rawRatio !== undefined && rawRatio !== null && rawRatio !== '') {
        const f = Number.parseFloat(rawRatio)
        if (Number.isFinite(f)) {
          compactionThreshold = String(Math.round(f * 100))
        }
      }
      showToast('Compaction threshold saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      compactionSaving = false
    }
  }

  async function saveAutoThrottle() {
    if (readOnly) return
    autoThrottleSaving = true
    try {
      const link = Number.parseInt(String(autoThrottleLink).trim() || '0', 10)
      const floor = Number.parseInt(String(autoThrottleFloor).trim() || '5', 10)
      if (autoThrottleEnabled && (!Number.isInteger(link) || link <= 0)) {
        showToast('Set your link upload capacity in Mbps to enable adaptive throttling', 'error')
        autoThrottleSaving = false
        return
      }
      if (!Number.isInteger(floor) || floor < 0 || (link > 0 && floor > link)) {
        showToast('Minimum rate must be between 0 and the link capacity', 'error')
        autoThrottleSaving = false
        return
      }
      settings = await api.updateSettings({
        auto_throttle_enabled: autoThrottleEnabled ? 'true' : 'false',
        auto_throttle_link_mbps: String(link),
        auto_throttle_floor_mbps: String(floor),
      })
      showToast('Upload throttling saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      autoThrottleSaving = false
    }
  }

  async function saveRetryPolicy() {
    if (readOnly) return
    retrySaving = true
    try {
      const payload = {}
      const maxStr = String(retryMax).trim()
      // Allow blank to leave it unchanged (the backend keeps the existing
      // value). Coerce to a string because settings are stored as strings.
      if (maxStr !== '') {
        const n = Number.parseInt(maxStr, 10)
        if (!Number.isInteger(n) || n < 0 || n > 10) {
          showToast('Max retries must be between 0 and 10', 'error')
          retrySaving = false
          return
        }
        payload.retry_max_default = String(n)
      }
      const delaysStr = (retryDelays || '').trim()
      if (delaysStr !== '') {
        payload.retry_delays_default = delaysStr
      }
      if (Object.keys(payload).length === 0) {
        showToast('Nothing to save', 'info')
        retrySaving = false
        return
      }
      settings = await api.updateSettings(payload)
      retryMax = settings?.retry_max_default ?? retryMax
      retryDelays = settings?.retry_delays_default ?? retryDelays
      showToast('Retry policy saved', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      retrySaving = false
    }
  }

  async function downloadDiagnostics() {
    diagnosticsDownloading = true
    try {
      const blob = await api.downloadDiagnostics()
      const date = new Date().toISOString().split('T')[0]
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vault-diagnostics-${date}.zip`
      a.click()
      URL.revokeObjectURL(url)
      showToast('Diagnostics bundle downloaded', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      diagnosticsDownloading = false
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

  async function generateApiKey() {
    apiKeyGenerating = true
    try {
      const res = await api.generateAPIKey()
      apiKeyEnabled = true
      apiKeyRevealed = res.api_key
      apiKeyShowing = true
      showToast('API key generated – copy it now', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      apiKeyGenerating = false
    }
  }

  async function toggleApiKeyReveal() {
    if (apiKeyShowing) {
      apiKeyShowing = false
      apiKeyRevealed = ''
      return
    }
    apiKeyLoading = true
    try {
      const res = await api.revealAPIKey()
      apiKeyRevealed = res.api_key
      apiKeyShowing = true
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      apiKeyLoading = false
    }
  }

  async function rotateApiKey() {
    apiKeyGenerating = true
    try {
      const res = await api.rotateAPIKey()
      apiKeyRevealed = res.api_key
      apiKeyShowing = true
      showToast('API key rotated – copy the new key', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      apiKeyGenerating = false
    }
  }

  async function revokeApiKey() {
    confirmApiKeyRevoke = false
    apiKeyRevoking = true
    try {
      await api.revokeAPIKey()
      apiKeyEnabled = false
      apiKeyRevealed = ''
      apiKeyShowing = false
      showToast('API key revoked – authentication disabled', 'success')
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      apiKeyRevoking = false
    }
  }

  async function copyApiKey() {
    if (apiKeyRevealed) {
      if (await copyText(apiKeyRevealed)) {
        showToast('API key copied to clipboard', 'success')
      } else {
        showToast('Failed to copy – clipboard access denied', 'error')
      }
    }
  }

  let notificationsOn = $derived(settings.notifications_enabled !== 'false')
  let backupRuleOn = $derived(settings.backup_rule_enabled !== 'false')
  let containerBackupOn = $derived(settings.container_backup_enabled !== 'false')
  let vmBackupOn = $derived(settings.vm_backup_enabled !== 'false')
  let folderBackupOn = $derived(settings.folder_backup_enabled !== 'false')
  let flashBackupOn = $derived(settings.flash_backup_enabled !== 'false')

  // Anomaly detection settings state (Task 19)
  let anomalyEnabled = $state(true)
  let anomalySensitivityDefault = $state('balanced')
  let anomalyNotifyMinSeverity = $state('critical')
  let anomalySaving = $state(false)

  // Replication enable/disable (feature visibility)
  let replicationEnabledSetting = $state(true)
  let replicationSaving = $state(false)

  // Storage verbose logging (Task 12 – storage resilience)
  let storageVerboseLogging = $state(false)
  let storageVerboseSaving = $state(false)

  async function saveAnomalySettings() {
    if (readOnly) return
    anomalySaving = true
    try {
      settings = await api.updateSettings({
        anomaly_detection_enabled: anomalyEnabled ? 'true' : 'false',
        anomaly_sensitivity_default: anomalySensitivityDefault,
        anomaly_notify_min_severity: anomalyNotifyMinSeverity,
      })
      showToast('Anomaly detection settings saved', 'success')
      setAnomalyEnabled(anomalyEnabled)
    } catch (e) {
      showToast(e.message, 'error')
    } finally {
      anomalySaving = false
    }
  }

  // Both toggles save immediately on flip (no separate Save button); on
  // failure the switch rolls back so the UI never lies about server state.
  async function toggleReplicationEnabled() {
    if (replicationSaving || readOnly) return
    replicationEnabledSetting = !replicationEnabledSetting
    replicationSaving = true
    try {
      settings = await api.updateSettings({
        replication_enabled: replicationEnabledSetting ? 'true' : 'false',
      })
      setReplicationEnabled(replicationEnabledSetting)
      showToast(replicationEnabledSetting ? 'Replication enabled' : 'Replication disabled', 'success')
    } catch (e) {
      // The PUT may have persisted even though the response was lost —
      // reconcile from the server rather than blindly inverting, so the
      // switch never disagrees with actual daemon state.
      try {
        settings = await api.getSettings()
        replicationEnabledSetting = settings?.replication_enabled === 'true'
        setReplicationEnabled(replicationEnabledSetting)
      } catch {
        replicationEnabledSetting = !replicationEnabledSetting
      }
      showToast(e.message, 'error')
    } finally {
      replicationSaving = false
    }
  }

  async function toggleStorageVerboseLogging() {
    if (readOnly || storageVerboseSaving) return
    storageVerboseLogging = !storageVerboseLogging
    storageVerboseSaving = true
    try {
      settings = await api.updateSettings({
        storage_verbose_logging: storageVerboseLogging ? 'true' : 'false',
      })
      showToast(storageVerboseLogging ? 'Verbose storage logging enabled' : 'Verbose storage logging disabled', 'success')
    } catch (e) {
      try {
        settings = await api.getSettings()
        storageVerboseLogging = settings?.storage_verbose_logging === 'true'
      } catch {
        storageVerboseLogging = !storageVerboseLogging
      }
      showToast(e.message, 'error')
    } finally {
      storageVerboseSaving = false
    }
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
    {#if isReplicaMode()}
      <div class="flex items-center gap-2.5 bg-surface-3 border border-border rounded-xl px-4 py-2.5 mb-4 text-sm text-text-muted">
        <svg aria-hidden="true" class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/></svg>
        <span>Read-only replica — secret and destructive actions are disabled on this instance.</span>
      </div>
    {/if}

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
      <!-- Jump-to bar (#208 / E11) — sticky so long-tab settings stay reachable -->
      <div class="sticky top-0 z-10 -mx-1 px-1 py-2 bg-surface/95 backdrop-blur border-b border-border flex flex-wrap items-center gap-2">
        <span class="text-[11px] font-semibold uppercase tracking-wide text-text-muted shrink-0">Jump to</span>
        {#each generalSections.filter(s => s.id !== 'set-database' || databaseInfo) as sec (sec.id)}
          <button type="button" onclick={() => jumpToSetting(sec.id)}
            class="px-2.5 py-1 text-xs font-medium rounded-full border border-border bg-surface-3 text-text-muted hover:border-vault/40 hover:text-text transition-colors whitespace-nowrap shrink-0">
            {sec.label}
          </button>
        {/each}
      </div>

      <!-- Appearance / Theme -->
      <div id="set-appearance" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
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
      <div id="set-targets" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Backup Targets</h2>
          <p class="text-xs text-text-muted mt-0.5">Select what Vault should monitor. Disabled items won't show as unprotected on Dashboard or Recovery.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Containers</p>
              <p class="text-xs text-text-muted mt-0.5">Docker containers on this server</p>
            </div>
            <button
              onclick={() => toggleBackupTarget('container_backup_enabled')}
              disabled={saving || readOnly}
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
              disabled={saving || readOnly}
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
              disabled={saving || readOnly}
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
              disabled={saving || readOnly}
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

      <!-- Dashboard -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Dashboard</h2>
          <p class="text-xs text-text-muted mt-0.5">Control which optional panels appear on the Dashboard.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">3-2-1 Backup Rule</p>
              <p class="text-xs text-text-muted mt-0.5">Show the 3-2-1 best-practice compliance panel. Hide it if you run an intentionally simple setup.</p>
            </div>
            <button
              onclick={() => toggleBackupRule()}
              disabled={saving || readOnly}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={backupRuleOn}
              aria-label="Toggle 3-2-1 Backup Rule panel"
            >
              <div class="w-11 h-6 rounded-full transition-colors {backupRuleOn ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {backupRuleOn ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
        </div>
      </div>

      <!-- Retry policy -->
      <div id="set-retry" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Retry Policy <Tooltip text="Failed runs are retried in the background with the delays below before being marked permanently failed. Individual jobs can override these defaults." /></h2>
          <p class="text-xs text-text-muted mt-0.5">How failed runs are retried before being marked permanently failed.</p>
        </div>
        <div class="p-5 space-y-4">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="retry-max" class="block text-sm font-medium text-text-muted mb-1.5">
                Max retries
                <Tooltip text="Maximum number of retry attempts after the initial failure. 0 disables retries entirely. Range: 0–10." />
              </label>
              <input
                id="retry-max"
                type="number"
                min="0"
                max="10"
                bind:value={retryMax}
                disabled={readOnly}
                placeholder="2"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
            </div>
            <div>
              <div class="flex items-center mb-1.5">
                <span class="block text-sm font-medium text-text-muted">Delays between retries</span>
                <Tooltip text='How long to wait before each retry. The Nth retry waits for the Nth delay before running. Defaults to 15 min, 1 h, 4 h.' />
              </div>
              <div class:pointer-events-none={readOnly} class:opacity-60={readOnly}>
                <RetryDelaysEditor bind:value={retryDelays} />
              </div>
            </div>
          </div>
          {#if !readOnly}
          <div class="flex justify-end">
            <button
              onclick={saveRetryPolicy}
              disabled={retrySaving}
              class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
            >
              {#if retrySaving}<InlineSpinner />{/if}
              Save Retry Policy
            </button>
          </div>
          {/if}
        </div>
      </div>

      <!-- Adaptive Upload Throttling (issue #237) -->
      <div id="set-throttle" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Upload Throttling <Tooltip text="When enabled, Vault continuously measures non-Vault traffic on the network link and slows its own uploads so streaming services like Plex or Jellyfin keep their bandwidth. Vault speeds back up when the link goes quiet." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Automatically yield upload bandwidth to other services using your internet link.</p>
        </div>
        <div class="p-5 space-y-4">
          <div class="flex items-center justify-between">
            <p class="text-sm font-medium text-text">Adaptively throttle uploads when the link is busy</p>
            <button
              onclick={() => { if (!readOnly) autoThrottleEnabled = !autoThrottleEnabled }}
              disabled={readOnly}
              class="relative inline-flex items-center shrink-0 cursor-pointer disabled:opacity-60"
              role="switch"
              aria-checked={autoThrottleEnabled}
              aria-label="Toggle adaptive upload throttling"
            >
              <div class="w-11 h-6 rounded-full transition-colors {autoThrottleEnabled ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {autoThrottleEnabled ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="throttle-link" class="block text-sm font-medium text-text-muted mb-1.5">
                Link upload capacity (Mbps)
                <Tooltip text="Your internet connection's upstream speed as quoted by your ISP, in megabits per second. Vault targets this minus current non-Vault traffic minus 10% headroom." />
              </label>
              <input id="throttle-link" type="number" min="1" bind:value={autoThrottleLink} disabled={readOnly}
                placeholder="e.g. 40"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
            </div>
            <div>
              <label for="throttle-floor" class="block text-sm font-medium text-text-muted mb-1.5">
                Minimum Vault rate (Mbps)
                <Tooltip text="Vault never throttles below this rate, so backups always keep progressing even while the link is busy." />
              </label>
              <input id="throttle-floor" type="number" min="0" bind:value={autoThrottleFloor} disabled={readOnly}
                placeholder="5"
                class="w-full px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
            </div>
          </div>
          <p class="text-xs text-text-dim">Applies to uploads to network destinations (SFTP, SMB, NFS, WebDAV, S3). Per-destination bandwidth caps still apply — the stricter limit wins. Takes effect within a few seconds of saving, no restart needed.</p>
          {#if !readOnly}
          <div class="flex justify-end">
            <button onclick={saveAutoThrottle} disabled={autoThrottleSaving}
              class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
              {#if autoThrottleSaving}<InlineSpinner />{/if}
              Save Upload Throttling
            </button>
          </div>
          {/if}
        </div>
      </div>

      <!-- Dedup Compaction Threshold -->
      <div id="set-dedup" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Dedup Compaction <Tooltip text="With deduplication on, deleting backups leaves gaps inside storage files. 'Run cleanup' rewrites a file once this % of it is wasted space, reclaiming it. Recommended: 50. Lower reclaims sooner (more rewriting); 100 never compacts." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Threshold for repacking partially-dead dedup packs during cleanup.</p>
        </div>
        <div class="p-5 space-y-4">
          <div class="space-y-1">
            <label for="compaction-threshold" class="block text-sm font-medium text-text-muted">
              Compaction threshold (% dead bytes)
            </label>
            <p class="text-xs text-text-muted">
              "Run cleanup" repacks a mixed dedup pack when its dead bytes ≥ this percentage of its size.
              100 disables compaction; smaller values compact more aggressively. Applies to every dedup destination.
            </p>
            <div class="flex items-center gap-2 mt-2">
              <input
                id="compaction-threshold"
                type="number"
                min="0"
                max="100"
                step="1"
                bind:value={compactionThreshold}
                disabled={readOnly}
                placeholder="50"
                class="w-24 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
              <span class="text-sm text-text-muted">%</span>
              {#if !readOnly}
              <button
                type="button"
                onclick={saveCompactionThreshold}
                disabled={compactionSaving}
                class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
              >
                {#if compactionSaving}<InlineSpinner />{/if}
                {compactionSaving ? 'Saving…' : 'Save'}
              </button>
              {/if}
            </div>
          </div>
        </div>
      </div>

      <!-- Anomaly Detection -->
      <div id="set-anomaly" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Anomaly Detection <Tooltip text="Vault monitors backup size, duration, and reliability for statistical anomalies. Anomalies are surfaced in the Anomalies page and optionally sent as notifications." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Automatically detect unusual patterns in backup behaviour.</p>
        </div>
        <div class="divide-y divide-border">
          <!-- Enabled toggle -->
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Anomaly detection enabled</p>
              <p class="text-xs text-text-muted mt-0.5">Evaluate each completed backup run for statistical anomalies.</p>
            </div>
            <button
              onclick={() => anomalyEnabled = !anomalyEnabled}
              disabled={readOnly}
              class="relative inline-flex items-center shrink-0 cursor-pointer"
              role="switch"
              aria-checked={anomalyEnabled}
              aria-label="Toggle anomaly detection"
            >
              <div class="w-11 h-6 rounded-full transition-colors {anomalyEnabled ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {anomalyEnabled ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
          <!-- Default sensitivity -->
          <div class="px-5 py-4">
            <label for="anomaly-sensitivity" class="block text-sm font-medium text-text mb-1.5">
              Default sensitivity
              <Tooltip text="Controls how aggressively anomalies are raised. Strict raises anomalies at small deviations (more alerts). Permissive only raises anomalies at large deviations (fewer alerts). Individual jobs can override this." />
            </label>
            <select
              id="anomaly-sensitivity"
              bind:value={anomalySensitivityDefault}
              disabled={readOnly}
              class="w-full max-w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="strict">Strict – flag small deviations</option>
              <option value="balanced">Balanced – moderate threshold (recommended)</option>
              <option value="permissive">Permissive – flag large deviations only</option>
            </select>
          </div>
          <!-- Notify minimum severity -->
          <div class="px-5 py-4">
            <label for="anomaly-notify-severity" class="block text-sm font-medium text-text mb-1.5">
              Notify minimum severity
              <Tooltip text="Only send notifications for anomalies at or above this severity level. Lower severities are still recorded but won't trigger a notification." />
            </label>
            <select
              id="anomaly-notify-severity"
              bind:value={anomalyNotifyMinSeverity}
              disabled={readOnly}
              class="w-full max-w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="info">Info – notify on all anomalies</option>
              <option value="warning">Warning – skip informational</option>
              <option value="critical">Critical – only urgent anomalies</option>
            </select>
          </div>
          <!-- Save button -->
          {#if !readOnly}
          <div class="px-5 py-3 flex justify-end">
            <button
              onclick={saveAnomalySettings}
              disabled={anomalySaving}
              class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
            >
              {#if anomalySaving}
                <InlineSpinner />
              {/if}
              Save Anomaly Settings
            </button>
          </div>
          {/if}
        </div>
      </div>

      <!-- Replication -->
      {#if health?.mode !== 'replica'}
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Replication <Tooltip text="Replication mirrors backups to other Vault instances. Disable to hide the Replication page and pause all scheduled replication syncs." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Show the Replication page and run scheduled replication syncs.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Replication enabled</p>
              <p class="text-xs text-text-muted mt-0.5">When off, Replication is hidden from the sidebar and scheduled syncs are paused.</p>
            </div>
            <button
              onclick={toggleReplicationEnabled}
              disabled={replicationSaving || readOnly}
              class="relative inline-flex items-center shrink-0 cursor-pointer disabled:opacity-60"
              role="switch"
              aria-checked={replicationEnabledSetting}
              aria-label="Toggle replication"
            >
              <div class="w-11 h-6 rounded-full transition-colors {replicationEnabledSetting ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {replicationEnabledSetting ? 'translate-x-5' : 'translate-x-0'}"></div>
              </div>
            </button>
          </div>
        </div>
      </div>
      {/if}

      <!-- Storage Verbose Logging (Task 12 – storage resilience) -->
      <div id="set-logging" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Storage Logging <Tooltip text="When enabled, every file-level storage operation (upload, download, delete) is traced to the daemon log. Useful for diagnosing intermittent transfer failures; off by default to avoid log noise." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Per-operation trace logging for storage adapters.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-4 flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-text">Verbose storage logging</p>
              <p class="text-xs text-text-muted mt-0.5">Per-operation trace in the daemon log; off by default.</p>
            </div>
            <button
              onclick={toggleStorageVerboseLogging}
              disabled={readOnly || storageVerboseSaving}
              class="relative inline-flex items-center shrink-0 cursor-pointer disabled:opacity-60"
              role="switch"
              aria-checked={storageVerboseLogging}
              aria-label="Toggle verbose storage logging"
            >
              <div class="w-11 h-6 rounded-full transition-colors {storageVerboseLogging ? 'bg-vault' : 'bg-surface-4'}">
                <div class="absolute top-[2px] left-[2px] w-5 h-5 bg-white rounded-full shadow transition-transform {storageVerboseLogging ? 'translate-x-5' : 'translate-x-0'}"></div>
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
              disabled={saving || readOnly}
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
                Each job has its own "Notify On" preference (Always, On Failure, Never) – configure it in the job's advanced settings. This global toggle enables or disables all Vault notifications.
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
                disabled={readOnly}
                placeholder="https://discord.com/api/webhooks/..."
                class="flex-1 text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
              <button
                onclick={testDiscord}
                disabled={discordTesting || !discordWebhookUrl || readOnly}
                class="px-3 py-2 text-sm font-medium text-text-muted bg-surface-3 border border-border rounded-lg hover:bg-surface-4 transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5 shrink-0"
              >
                {#if discordTesting}
                  <InlineSpinner />
                {:else}
                  <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9" /></svg>
                {/if}
                Test
              </button>
            </div>
          </div>
          <div class="px-5 py-4">
            <label for="discord-notify" class="block text-sm font-medium text-text mb-1.5">Notify On <Tooltip text="Controls which Discord alerts this webhook sends. This is independent of each job's own notification preference." /></label>
            <select
              id="discord-notify"
              bind:value={discordNotifyOn}
              disabled={readOnly}
              class="text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
            >
              <option value="always">All backups (success & failure)</option>
              <option value="failure">Failures only</option>
              <option value="never">Disabled</option>
            </select>
          </div>
          <div class="px-5 py-4 grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="discord-bot-username" class="block text-sm font-medium text-text mb-1.5">Bot Username <Tooltip text="Overrides the name the webhook posts under. Leave blank to use the webhook's configured name." /></label>
              <input
                id="discord-bot-username"
                type="text"
                bind:value={discordBotUsername}
                disabled={readOnly}
                placeholder="Vault"
                class="w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
            </div>
            <div>
              <label for="discord-bot-avatar" class="block text-sm font-medium text-text mb-1.5">Bot Avatar URL <Tooltip text="Overrides the avatar the webhook posts with. Must be a direct image URL. Leave blank to use the webhook's configured avatar." /></label>
              <input
                id="discord-bot-avatar"
                type="url"
                bind:value={discordBotAvatarUrl}
                disabled={readOnly}
                placeholder="https://example.com/avatar.png"
                class="w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
            </div>
          </div>
          <div class="px-5 py-4 grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="discord-mention-role" class="block text-sm font-medium text-text mb-1.5">Mention Role ID <Tooltip text="A Discord role ID to ping on alerts (enable Developer Mode, then Server Settings → Roles → Copy ID). Only this role is pinged — @everyone is never used." /></label>
              <input
                id="discord-mention-role"
                type="text"
                inputmode="numeric"
                bind:value={discordMentionRoleId}
                disabled={readOnly}
                placeholder="123456789012345678"
                class="w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              />
            </div>
            <div>
              <label for="discord-mention-on" class="block text-sm font-medium text-text mb-1.5">Mention On <Tooltip text="When to ping the role above. Failures only pings on failed or partial backups; All backups pings on every alert this webhook sends." /></label>
              <select
                id="discord-mention-on"
                bind:value={discordMentionOn}
                disabled={readOnly}
                class="w-full text-sm px-3 py-2 bg-surface-1 border border-border rounded-lg text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
              >
                <option value="never">Never</option>
                <option value="failure">Failures only</option>
                <option value="always">All backups</option>
              </select>
            </div>
          </div>
          {#if !readOnly}
          <div class="px-5 py-3 flex justify-end">
            <button
              onclick={saveDiscordSettings}
              disabled={discordSaving}
              class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
            >
              {#if discordSaving}
                <InlineSpinner />
              {/if}
              Save Discord Settings
            </button>
          </div>
          {/if}
        </div>
      </div>

      {/if}

      <!-- === SECURITY TAB === -->
      {#if activeTab === 'security'}
      <!-- Encryption -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Encryption <Tooltip text="Uses age encryption. Each job must opt in individually. The passphrase is irrecoverable if lost – store it safely." /></h2>
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
                  {loadingPassphrase ? 'Loading…' : showingPassphrase ? 'Hide' : 'Show'}
                </button>
              </div>
              {#if showingPassphrase}
                <div class="mt-3 px-3 py-2.5 bg-surface border border-border rounded-lg">
                  <code class="text-sm text-text break-all select-all">{revealedPassphrase}</code>
                </div>
              {/if}
            </div>

            <!-- Change passphrase -->
            {#if !isReplicaMode()}
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
                    <input id="change-pass" type="password" autocomplete="new-password" bind:value={changeNewPass} placeholder="Enter new passphrase" class="w-full px-3 py-2 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                  </div>
                  <div>
                    <label for="change-confirm" class="block text-xs font-medium text-text-muted mb-1">Confirm new passphrase</label>
                    <input id="change-confirm" type="password" autocomplete="new-password" bind:value={changeConfirmPass} placeholder="Confirm new passphrase" class="w-full px-3 py-2 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
                  </div>
                  {#if changeNewPass && changeConfirmPass && changeNewPass !== changeConfirmPass}
                    <p class="text-xs text-danger">Passphrases do not match</p>
                  {/if}
                  <div class="flex items-center gap-2">
                    <button onclick={saveChangePassphrase} disabled={encSaving || !changeNewPass || !changeConfirmPass || changeNewPass !== changeConfirmPass} class="px-4 py-2 text-sm font-semibold rounded-lg bg-vault text-white hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
                      {#if encSaving}<InlineSpinner />{/if}
                      {encSaving ? 'Saving…' : 'Update Passphrase'}
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
                {encSaving ? 'Removing…' : 'Disable encryption'}
              </button>
            </div>
            {/if}
          {:else}
            <div class="px-5 py-4">
              <div class="flex items-center gap-2 mb-3">
                <span class="inline-block w-2 h-2 rounded-full bg-text-dim"></span>
                <span class="text-sm font-medium text-text">No encryption passphrase set</span>
              </div>
              <p class="text-xs text-text-muted mb-4">Set a global passphrase to enable age encryption for backup jobs. Jobs must individually opt-in to encryption. Existing encrypted backups always require the original passphrase to restore.</p>
              {#if !isReplicaMode()}
              <div class="space-y-3 max-w-sm">
                <div>
                  <label for="enc-pass" class="block text-xs font-medium text-text-muted mb-1">Passphrase</label>
                  <div class="relative">
                    <input id="enc-pass" type={showEncPassphrase ? 'text' : 'password'} autocomplete="new-password" bind:value={encPassphrase} placeholder="Enter encryption passphrase" class="w-full px-3 py-2 pr-10 text-sm bg-surface border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault" />
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
                <button onclick={saveEncryption} disabled={encSaving || !encPassphrase || !encConfirm || encPassphrase !== encConfirm} class="px-4 py-2 text-sm font-semibold rounded-lg bg-vault text-white hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
                  {#if encSaving}<InlineSpinner />{/if}
                  {encSaving ? 'Saving…' : 'Set Passphrase'}
                </button>
              </div>
              {/if}
            </div>
          {/if}
        </div>
      </div>

      <!-- API Access -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">API Access <Tooltip text="Generate an API key to authenticate external integrations (Home Assistant, replication) when Vault is exposed on the network. Not required when accessed via localhost." /></h2>
        </div>
        <div class="divide-y divide-border">
          {#if apiKeyEnabled}
            <!-- Status -->
            <div class="px-5 py-4">
              <div class="flex items-center gap-2 mb-2">
                <span class="inline-block w-2 h-2 rounded-full bg-success"></span>
                <span class="text-sm font-medium text-text">API key active</span>
              </div>
              <p class="text-xs text-text-muted">External clients must include the <code class="bg-surface px-1 rounded">X-API-Key</code> header when connecting from non-localhost addresses. Localhost connections are always exempt.</p>
            </div>

            <!-- Reveal key -->
            <div class="px-5 py-4">
              <div class="flex items-center justify-between gap-4">
                <div>
                  <p class="text-sm font-medium text-text">Show API key</p>
                  <p class="text-xs text-text-muted mt-0.5">Reveal the key to copy it for external integrations.</p>
                </div>
                <button onclick={toggleApiKeyReveal} disabled={apiKeyLoading} class="text-sm font-medium text-info hover:text-info/80 transition-colors shrink-0 disabled:opacity-50">
                  {apiKeyLoading ? 'Loading…' : apiKeyShowing ? 'Hide' : 'Show'}
                </button>
              </div>
              {#if apiKeyShowing && apiKeyRevealed}
                <div class="mt-3 flex items-center gap-2">
                  <div class="flex-1 px-3 py-2.5 bg-surface border border-border rounded-lg">
                    <code class="text-sm text-text break-all select-all">{apiKeyRevealed}</code>
                  </div>
                  <button onclick={copyApiKey} class="shrink-0 p-2 text-text-muted hover:text-text transition-colors" aria-label="Copy API key">
                    <svg aria-hidden="true" class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" /></svg>
                  </button>
                </div>
              {/if}
            </div>

            <!-- Rotate key -->
            {#if !isReplicaMode()}
            <div class="px-5 py-4 flex items-center justify-between gap-4">
              <div>
                <p class="text-sm font-medium text-text">Rotate API key</p>
                <p class="text-xs text-text-muted mt-0.5">Replace with a new key. Existing integrations will need the new key.</p>
              </div>
              <button onclick={rotateApiKey} disabled={apiKeyGenerating} class="text-sm font-medium text-warning hover:text-warning/80 transition-colors shrink-0 disabled:opacity-50">
                {apiKeyGenerating ? 'Rotating…' : 'Rotate'}
              </button>
            </div>

            <!-- Revoke key -->
            <div class="px-5 py-3 flex justify-end">
              {#if confirmApiKeyRevoke}
                <div class="flex items-center gap-2">
                  <span class="text-xs text-danger">Revoke key and disable authentication?</span>
                  <button onclick={revokeApiKey} disabled={apiKeyRevoking} class="text-xs font-medium text-danger hover:text-danger/80 transition-colors disabled:opacity-50">
                    {apiKeyRevoking ? 'Revoking…' : 'Yes, revoke'}
                  </button>
                  <button onclick={() => confirmApiKeyRevoke = false} class="text-xs text-text-muted hover:text-text transition-colors">
                    Cancel
                  </button>
                </div>
              {:else}
                <button onclick={() => confirmApiKeyRevoke = true} disabled={apiKeyRevoking} class="text-xs text-text-dim hover:text-danger transition-colors disabled:opacity-50">
                  Revoke API key
                </button>
              {/if}
            </div>
            {/if}
          {:else}
            <div class="px-5 py-4">
              <div class="flex items-center gap-2 mb-3">
                <span class="inline-block w-2 h-2 rounded-full bg-text-dim"></span>
                <span class="text-sm font-medium text-text">No API key configured</span>
              </div>
              <p class="text-xs text-text-muted mb-4">Generate an API key to protect the Vault API when it is exposed beyond localhost. API keys are required for third-party integrations (e.g. Home Assistant) and replication between Vault instances on different servers.</p>
              {#if !isReplicaMode()}
              <button onclick={generateApiKey} disabled={apiKeyGenerating} class="px-4 py-2 text-sm font-semibold rounded-lg bg-vault text-white hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
                {#if apiKeyGenerating}<InlineSpinner />{/if}
                {apiKeyGenerating ? 'Generating…' : 'Generate API Key'}
              </button>
              {/if}
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
          <h2 class="text-base font-semibold text-text">Temporary Work Area <Tooltip text="The staging directory is where Vault temporarily assembles backup archives before sending them to the final storage destination." /></h2>
          <p class="text-xs text-text-muted mt-0.5">Where Vault assembles backup files before transferring them to storage.</p>
        </div>
        <div class="p-5 space-y-4">
          <div>
            <p class="text-sm text-text font-mono">{stagingInfo.resolved_path}</p>
            <p class="text-xs text-text-muted mt-0.5">
              {stagingInfo.source === 'override' ? 'Custom location' :
               stagingInfo.source === 'cache' ? 'Using SSD cache for fast backup processing' :
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
            <span class="text-xs text-text-muted block mb-1.5">Custom Location</span>
            <p class="text-xs text-text-dim mb-2">Override the automatic location. Use this if you want backups to be assembled on a specific drive. NVMe-backed ZFS pools are automatically prioritized when detected.</p>
            <div class="flex gap-2 items-end">
              <div class="flex-1" class:pointer-events-none={readOnly} class:opacity-60={readOnly}>
                <PathBrowser bind:value={stagingOverrideInput} onselect={saveStagingOverride} includeZfs={true} />
              </div>
              {#if !readOnly}
              <button onclick={saveStagingOverride} disabled={stagingSaving || !stagingOverrideInput} class="px-3 py-2 bg-vault text-white text-sm rounded-lg hover:bg-vault-dark disabled:opacity-50 transition-colors shrink-0 flex items-center gap-2">
                {#if stagingSaving}<InlineSpinner />{/if}
                Apply
              </button>
              {/if}
            </div>
            {#if stagingInfo.override && !readOnly}
              <button onclick={resetStagingOverride} disabled={stagingSaving} class="mt-2 text-xs text-vault hover:underline">
                Reset to automatic
              </button>
            {/if}
          </div>


        </div>
      </div>
      {/if}

      <!-- History Retention -->
      <div id="set-history" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">History Retention</h2>
          <p class="text-xs text-text-muted mt-0.5">How long to keep backup/restore run history. Recoverable backups are not affected - they follow each job's own retention.</p>
        </div>
        <div class="p-5 flex items-center gap-2">
          <select bind:value={historyRetention} disabled={readOnly}
            class="px-2.5 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text focus:outline-none focus:ring-1 focus:ring-vault focus:border-vault cursor-pointer">
            <option value="30">30 days</option>
            <option value="90">90 days</option>
            <option value="180">6 months</option>
            <option value="365">1 year</option>
            <option value="730">2 years</option>
            <option value="0">Keep everything</option>
          </select>
          {#if !readOnly}
          <button type="button" onclick={saveHistoryRetention} disabled={historyRetentionSaving}
            class="px-4 py-2 text-sm font-semibold text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2">
            {#if historyRetentionSaving}<InlineSpinner />{/if}
            Save
          </button>
          {/if}
        </div>
      </div>

      <!-- Server Info -->
      <div id="set-server" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
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

        </div>
      </div>

      {/if}

      <!-- === REFERENCE TAB === -->
      {#if activeTab === 'reference'}
      <!-- Online documentation -->
      <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Documentation</h2>
        </div>
        <div class="p-5">
          <p class="text-sm text-text-muted mb-4">
            The full Vault manual (getting started, guides, and a complete settings reference) is published online and stays in sync with each release.
          </p>
          <a href="https://ruaan-deysel.github.io/vault/" target="_blank" rel="noopener"
            aria-label="Open documentation (opens in a new tab)"
            class="inline-flex items-center gap-2 px-3 py-1.5 text-sm font-medium text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors">
            <svg aria-hidden="true" class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><path d="M6 2a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6H6zm7 1.5L18.5 9H13V3.5zM8 12h8v1.5H8V12zm0 3h6v1.5H8V15z"/></svg>
            Open documentation
          </a>
        </div>
      </div>

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

      {/if}

      <!-- === GENERAL TAB (cont.) === -->
      {#if activeTab === 'general'}

      <!-- Database Location -->
      {#if databaseInfo}
      <div id="set-database" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Database Location</h2>
          <p class="text-xs text-text-muted mt-0.5">Vault's database tracks your jobs, schedules, and restore points. The storage mode determines where this data is written.</p>
        </div>
        <div class="divide-y divide-border">
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Mode <Tooltip text="Where Vault keeps its working database. Hybrid: in RAM for speed, saved to SSD periodically (recommended). Direct SSD: every change written straight to SSD. Legacy USB: writes to the boot flash drive – wears it out, not recommended." /></span>
            <span class="text-sm font-medium text-text">
              {#if databaseInfo.mode === 'hybrid'}
                Hybrid – runs in memory for speed, saves to SSD periodically
              {:else if databaseInfo.mode === 'direct'}
                Direct SSD
              {:else}
                Legacy USB
              {/if}
            </span>
          </div>
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted" title="Where the database currently operates from. In hybrid mode this is in RAM.">Active database</span>
            <span class="text-sm font-mono text-text truncate ml-4">{databaseInfo.working_path}</span>
          </div>
          {#if databaseInfo.snapshot_path}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Saved copy</span>
            <span class="text-sm font-mono text-text truncate ml-4">{databaseInfo.snapshot_path}</span>
          </div>
          {/if}
          {#if databaseInfo.last_snapshot}
          {@const snapDate = new Date(databaseInfo.last_snapshot)}
          {#if snapDate.getFullYear() > 1970}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Last saved</span>
            <span class="text-sm text-text">{formatDate(databaseInfo.last_snapshot)}</span>
          </div>
          {/if}
          {/if}
          {#if databaseInfo.snapshot_size_bytes}
          <div class="px-5 py-3 flex items-center justify-between">
            <span class="text-sm text-text-muted">Saved copy size</span>
            <span class="text-sm text-text">{formatBytes(databaseInfo.snapshot_size_bytes)}</span>
          </div>
          {/if}
        </div>
        {#if databaseInfo.mode === 'hybrid' && !isReplicaMode()}
        <div class="px-5 py-4 border-t border-border">
          <span class="text-xs text-text-muted block mb-1.5">Custom save location <Tooltip text="Overrides where the persistent database snapshot is saved." /></span>
          <p class="text-xs text-text-dim mb-2">Choose where the persistent database copy is stored. Defaults to SSD cache. ZFS zpools are also available as high-performance locations.</p>
          <div class="flex gap-2 items-end">
            <div class="flex-1">
              <PathBrowser bind:value={snapshotPathInput} onselect={saveSnapshotPath} includeZfs={true} />
            </div>
            <button onclick={saveSnapshotPath} disabled={snapshotPathSaving || !snapshotPathInput} class="px-3 py-2 bg-vault text-white text-sm rounded-lg hover:bg-vault-dark disabled:opacity-50 transition-colors shrink-0 flex items-center gap-2">
              {#if snapshotPathSaving}<InlineSpinner />{/if}
              Apply
            </button>
          </div>
          {#if databaseInfo.snapshot_path_override}
            <button onclick={resetSnapshotPath} disabled={snapshotPathSaving} class="mt-2 text-xs text-vault hover:underline">
              Reset to default
            </button>
          {/if}
        </div>
        {/if}
        {#if databaseInfo.mode === 'legacy_usb'}
        <div class="px-5 py-3 bg-amber-500/10 border-t border-amber-500/20">
          <p class="text-xs text-amber-400">No cache drive detected. Database writes go directly to the USB flash drive, which may reduce its lifespan. Add a cache drive to your server, or set a custom save location above to avoid writing to the USB drive.</p>
        </div>
        {/if}
      </div>
      {/if}

      <!-- Diagnostics -->
      <div id="set-diagnostics" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">Diagnostics</h2>
        </div>
        <div class="p-5 space-y-3">
          <p class="text-sm text-text-muted leading-relaxed">
            Download a diagnostics bundle containing system information, job configurations, recent run history, and activity logs. Sensitive data such as passwords and API keys are automatically redacted.
          </p>
          <button
            onclick={downloadDiagnostics}
            disabled={diagnosticsDownloading}
            class="flex items-center gap-2 text-sm font-medium text-info hover:text-info/80 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {#if diagnosticsDownloading}
              <InlineSpinner />
              Generating...
            {:else}
              <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
              </svg>
              Download diagnostics bundle
            {/if}
          </button>
        </div>
      </div>

      <!-- About Vault – single merged card -->
      <div id="set-about" class="scroll-mt-16 bg-surface-2 border border-border rounded-xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold text-text">About Vault</h2>
        </div>
        <div class="p-5">
          <p class="text-sm text-text-muted leading-relaxed">
            Vault is a backup and restore manager for Unraid servers. It automates scheduled, encrypted backups of Docker containers, libvirt VMs, plugins, ZFS datasets, and arbitrary folders (including the Unraid flash drive) to a wide range of storage destinations &mdash; local paths, SFTP, SMB/CIFS, NFS, WebDAV, and S3-compatible object stores (AWS S3, Backblaze B2, MinIO, Cloudflare R2, Wasabi).
          </p>

          <div class="mt-5 pt-4 border-t border-border">
            <div class="flex items-center gap-3 flex-wrap">
              <span class="text-sm font-medium text-text">Version <span class="font-mono">{currentVersion}</span></span>
              {#if aboutStatus.label}
                <span class="text-xs px-2 py-0.5 rounded {aboutBadgeClass}">{aboutStatus.label}</span>
              {/if}
              {#if aboutStatus.note}
                <span class="text-xs text-text-muted">{aboutStatus.note}</span>
              {/if}
            </div>
            {#if aboutReleasedNote}
              <p class="text-xs text-text-muted mt-1">{aboutReleasedNote}</p>
            {/if}
          </div>

          <div class="mt-4 flex items-center gap-4 flex-wrap">
            <button
              type="button"
              onclick={() => (aboutModalOpen = true)}
              disabled={aboutLoading || releases.length === 0}
              class="px-3 py-1.5 text-sm font-medium text-white bg-vault rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              View Changelog
            </button>
            <a href="https://github.com/ruaan-deysel/vault" target="_blank" rel="noopener"
              class="text-sm text-vault hover:text-vault-dark transition-colors flex items-center gap-2">
              <svg aria-hidden="true" class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"/></svg>
              GitHub Repository
            </a>
            <a href="https://github.com/sponsors/ruaan-deysel" target="_blank" rel="noopener"
              class="text-sm text-vault hover:text-vault-dark transition-colors flex items-center gap-2">
              <svg aria-hidden="true" class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z"/></svg>
              Sponsor
            </a>
          </div>
        </div>
      </div>

      <ChangelogModal
        show={aboutModalOpen}
        onclose={() => (aboutModalOpen = false)}
        {releases}
        {currentVersion}
        latestTag={latest?.tag ?? ''}
      />
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
