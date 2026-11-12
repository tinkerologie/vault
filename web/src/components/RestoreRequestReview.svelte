<script>
  import { onMount } from 'svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import VmRestoreEffects from './VmRestoreEffects.svelte'
  import VmReadinessResult from './VmReadinessResult.svelte'
  import { api } from '../lib/api.js'
  import { canApproveRestoreRequest } from '../lib/runtime-config.js'
  import { formatDate } from '../lib/utils.js'

  let { requestId } = $props()
  const heading = $props.id()
  let request = $state(null)
  let loading = $state(true)
  let loadError = $state('')
  let approvalError = $state('')
  let approving = $state(false)
  let reviewingMetadata = $state(false)
  let confirm = $state(false)
  let confirmStop = $state(false)
  let sendingStop = $state(false)
  let stopStatus = $state('')
  let stopError = $state('')
  let passphrase = $state('')
  let logs = $state([])
  let logsLoading = $state(false)
  let logsError = $state(false)
  const nativeApproval = canApproveRestoreRequest()
  let runStatus = $derived(request?.run?.status || request?.status)
  let stopping = $derived(runStatus === 'running' && (stopStatus === 'stopping' || request?.run?.stopping))
  let canStop = $derived(nativeApproval && request?.run_id && request.run?.active &&
    runStatus === 'running' && !stopping && !stopStatus)
  let displayedStatus = $derived(runStatus === 'running' && stopStatus && stopStatus !== 'stopping' ? stopStatus : runStatus)

  const lifetime = new AbortController()
  let refreshTimer
  let refreshVersion = 0

  onMount(() => {
    void refresh()
    return () => {
      lifetime.abort()
      clearTimeout(refreshTimer)
      passphrase = ''
    }
  })

  async function refresh() {
    clearTimeout(refreshTimer)
    const version = ++refreshVersion
    try {
      const result = await api.getRestoreRequest(requestId)
      if (lifetime.signal.aborted || version !== refreshVersion) return
      request = result
      loadError = ''
    } catch (error) {
      if (!lifetime.signal.aborted && version === refreshVersion) loadError = reviewError(error)
    } finally {
      if (!lifetime.signal.aborted && version === refreshVersion) {
        loading = false
        if (request?.status === 'running') refreshTimer = setTimeout(refresh, 1500)
      }
    }
  }

  async function reviewMetadata() {
    reviewingMetadata = true
    approvalError = ''
    try {
      const result = await api.reviewRestoreRequestMetadata(requestId, request.review_digest, passphrase)
      if (!lifetime.signal.aborted) request = result
    } catch (error) {
      if (!lifetime.signal.aborted) approvalError = error.message
    } finally {
      passphrase = ''
      reviewingMetadata = false
    }
  }

  async function approve() {
    approving = true
    approvalError = ''
    try {
      const result = await api.approveRestoreRequest(requestId, request.review_digest, passphrase)
      if (!lifetime.signal.aborted && result?.run_id && result.run) request = result
    } catch (error) {
      if (!lifetime.signal.aborted && error.status !== 410) approvalError = error.message
    } finally {
      passphrase = ''
      confirm = false
      approving = false
      if (!lifetime.signal.aborted) await refresh()
    }
  }

  async function stop() {
    if (!canStop || sendingStop) return
    const runId = request.run_id
    sendingStop = true
    stopError = ''
    try {
      const result = await api.stopRestoreRun(runId)
      if (lifetime.signal.aborted || request.run_id !== runId) return
      if (result?.run_id !== runId) throw new Error('Could not confirm the stop request.')
      if (result.status === 'running') {
        stopError = 'This Restore Run is no longer active. Refresh its status to check the outcome.'
      } else {
        stopStatus = result.status
      }
    } catch (error) {
      if (!lifetime.signal.aborted) stopError = error.message
    } finally {
      confirmStop = false
      sendingStop = false
      if (!lifetime.signal.aborted) await refresh()
    }
  }

  async function loadRunLog() {
    logsLoading = true
    logsError = false
    try {
      const result = await api.getRunLogs(request.run_id, { tail: true, limit: 50 })
      if (!lifetime.signal.aborted) logs = result.entries
    } catch {
      if (!lifetime.signal.aborted) logsError = true
    } finally {
      logsLoading = false
    }
  }

  function cancelApproval() {
    passphrase = ''
    confirm = false
  }

  const labels = {
    clear_target_contents: 'Clear all existing target contents before writing files.',
    write_backed_up_entries: 'Write backed-up entries to the target.',
    overwrite_matching_entries: 'Overwrite matching entries with the backup.',
    keep_unrelated_entries: 'Keep existing entries the restore does not touch.',
    prune_replayed_entries: 'If Vault can read the final file listing and the earlier Backup Chain indexes, it removes entries the replay wrote that are absent from the final listing.',
    unverified_backup: 'This backup has no recorded Verification.',
    artifact_evidence_unavailable: 'Vault could not confirm all required storage artifacts.',
    item_type_evidence_unavailable: 'The recorded evidence does not confirm the Backup Item type.',
    target_too_shallow_to_clear: 'The requested target is too close to the filesystem root to clear. Vault would use merge.',
    latest_verification_failed: 'The most recent Verification failed.',
    latest_verification_running: 'The most recent Verification is still running.',
    latest_verification_cancelled: 'The most recent Verification was cancelled.',
    latest_verification_unknown: 'The most recent Verification status is unknown.',
    membership_unknown: 'Vault could not confirm folder membership at the selected Restore Point.',
    folder_type_unconfirmed: 'The selected Restore Point does not confirm the item is a folder.',
    required_folder_type_unconfirmed: 'A required Backup Chain step does not confirm the item is a folder.',
    chain_unconfirmed: 'The required Backup Chain could not be confirmed.',
    required_data_unusable: 'Required backup data is missing or unusable.',
    original_target_unknown: 'The original restore location is unavailable.',
    original_target_disallowed: 'The original restore location is outside the allowed folder restore policy.',
    withdrawn_by_requester: 'The requesting assistant withdrew this request.',
    approval_window_elapsed: 'The 24-hour review window elapsed.',
    reviewed_operation_changed: 'The reviewed source or location changed. Ask the assistant to prepare a new request.',
    approval_persistence_failed: 'The approval could not be saved to flash. The Restore did not start.',
    daemon_restarted: 'The daemon restarted during this Restore. The target may be partially restored.',
    restore_failed: 'The Restore failed. Check the Restore Run log for details.',
    stopped_by_operator: 'The operator stopped the Restore Run. The restore location may be partly restored.',
    reviewed_operation_revalidated: 'Reviewed operation revalidated',
    required_backup_data_staged: 'Required backup data staged',
    folder_restore_completed: 'Folder Restore completed',
    backup_metadata_locked: 'Enter the Backup Passphrase to review the saved configuration.',
    item_type_unconfirmed: 'The selected Restore Point does not confirm this Backup Item type.',
    required_item_type_unconfirmed: 'A required Backup Chain step does not confirm this Backup Item type.',
    container_metadata_unusable: 'Vault could not read the saved Docker configuration or resolve its volume locations.',
    manual_restore_required: 'This container needs a host access or image override in the manual Restore wizard.',
    stop_existing_container: 'Stop the existing container if it is running.',
    remove_existing_container: 'Remove the existing container if present.',
    recreate_container: 'Create the container with the saved Docker configuration.',
    load_saved_image: 'Load the saved Docker image.',
    pin_saved_image: 'Use the recorded backup-time image digest.',
    start_container: 'Start the recreated container because it was running at backup time.',
    restore_container_template: 'Restore the saved Unraid Docker template.',
    replay_container_database_if_ready: 'Replay the saved database dump if the database becomes ready.',
    preserve_container_database_dump_if_not_replayed: 'Save the database dump for manual replay if Vault does not replay it.',
    container_started: 'Docker started the container',
    container_start_failed: 'Docker failed to start the container',
    container_database_ready: 'Database accepted connections for replay',
    container_database_not_ready: 'Database did not become ready for replay',
    container_database_replayed: 'Database dump replay completed',
    container_database_replay_failed: 'Database dump replay failed',
    replace: 'Clear directory contents and restore',
    merge: 'Overwrite matching files and keep unrelated files',
    overwrite_file: 'Overwrite the mounted file',
    configuration_only: 'No backed-up data for this mount',
    vm_membership_unknown: 'Vault could not confirm VM membership at the selected Restore Point.',
    vm_membership_missing: 'The VM is absent from the selected Restore Point.',
    vm_chain_membership_unknown: 'Vault could not confirm VM membership at a required Backup Chain step.',
    vm_full_backup_missing: 'The VM Backup Chain has no confirmed full backup.',
    vm_checkpoint_unconfirmed: 'Vault could not confirm the VM checkpoint recorded for a required backup.',
    vm_checkpoint_discontinuity: 'The recorded VM checkpoints do not form a continuous chain.',
    vm_required_disk_layer_missing: 'A required disk has no matching layer in the VM Backup Chain.',
    vm_required_artifact_missing: 'A required VM configuration, disk or NVRAM artifact is missing.',
    vm_sidecar_unconfirmed: 'Vault could not confirm the saved VM configuration or disk inventory.',
    vm_sidecar_too_large: 'The saved VM configuration or disk inventory exceeds the review limit.',
    vm_manifest_unsupported: 'This VM backup uses an unsupported storage format.',
    vm_target_disallowed: 'A VM disk or NVRAM destination is outside the restore policy.',
    vm_target_collision: 'Two VM disks or firmware files would write to the same path.',
    vm_identity_mismatch: 'The saved VM identity does not match the selected VM or the existing domain.',
    vm_target_in_use: 'Another domain uses a VM disk or NVRAM destination.',
    vm_host_unavailable: 'Vault could not check the current VM host and restore destinations.',
    vm_define_saved_domain: 'Define the VM from the saved configuration.',
    vm_replace_domain: 'Remove the existing domain definition and its libvirt metadata.',
    vm_stop_domain: 'Stop the existing VM, with a possible forced stop.',
    vm_restore_disks: 'Create or overwrite only the reviewed VM disk and firmware files.',
    vm_start_domain: 'Start the restored VM and run the configured readiness checks.',
    vm_leave_domain_stopped: 'Leave the restored VM stopped.',
    cycle: 'The recorded Backup Chain contains a cycle.',
    missing_parent: 'A required parent Restore Point is missing.',
    foreign_parent: 'A recorded parent belongs to a different Job or Replication Source.',
    depth_limit: 'Vault could not inspect the complete Backup Chain within the review limit.',

  }

  function reviewError(error) {
    if (error.status === 404) return 'Restore Request not found.'
    if (error.status === 403 || error.status === 401) return 'Operator access required to review this Restore Request.'
    return 'Could not load this Restore Request.'
  }
</script>

<section aria-labelledby={heading} class="mb-4 rounded-xl border border-border bg-surface-2 p-4">
  <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
    <h2 id={heading} class="text-base font-semibold text-text">Restore Request review</h2>
    <a href="#/restore" class="hit-target text-sm font-medium text-info hover:underline">Back to Restore</a>
  </div>
  {#if loading}
    <p role="status" class="text-sm text-text-muted">Loading Restore Request…</p>
  {:else if request}
    <dl class="grid gap-3 text-sm sm:grid-cols-2">
      <div><dt class="text-text-muted">Requester</dt><dd class="text-text [overflow-wrap:anywhere]">{request.requester_name}</dd></div>
      <div><dt class="text-text-muted">Status</dt><dd class="capitalize text-text">{request.status}</dd></div>
      <div><dt class="text-text-muted">Job and {request.item.item_type}</dt>
        <dd class="text-text [overflow-wrap:anywhere]">{request.job_name} · {request.item.item_name}</dd>
      </div>
      <div><dt class="text-text-muted">Restore Point #{request.restore_point.id}</dt>
        <dd class="text-text"><time datetime={request.restore_point.created_at}>{formatDate(request.restore_point.created_at)}</time></dd>
      </div>
      <div><dt class="text-text-muted">{request.target_choice === 'alternate' ? 'Alternate' : 'Original'} location</dt>
        <dd class="break-all text-text">{request.resolved_path || (request.vm ? 'Paths shown under VM resources' : request.container ? 'Original container volume locations' : 'Unavailable')}</dd>
        {#if request.configured_path && request.configured_path !== request.resolved_path}
          <dd class="break-all text-xs text-text-muted">Configured as {request.configured_path}</dd>
        {/if}
      </div>
      <div><dt class="text-text-muted">Expires</dt>
        <dd class="text-text"><time datetime={request.expires_at}>{formatDate(request.expires_at)}</time></dd>
      </div>
      <div><dt class="text-text-muted">Backup Item membership</dt><dd class="capitalize text-text">{request.membership}</dd></div>
      <div><dt class="text-text-muted">Verification</dt>
        <dd class="text-text">
          {#if request.verification.status === 'none' && request.verification.verified_during_backup === true}
            Passed during Backup Run
          {:else}
            <span class="capitalize">{request.verification.status}</span>
          {/if}
          {#if request.verification.mode} · {request.verification.mode}{/if}
          {#if request.verification.completed_at || request.verification.started_at}
            · {formatDate(request.verification.completed_at || request.verification.started_at)}
          {/if}
        </dd>
      </div>
    </dl>
    <div class="mt-4 grid gap-4 text-sm sm:grid-cols-2">
      <div>
        <h3 class="font-medium text-text">Backup Chain <span class="font-normal text-text-muted">· {request.chain.status}</span></h3>
        <ol class="mt-1 list-decimal space-y-1 pl-5 text-text-muted">
          {#each request.chain.steps as step (step.restore_point_id)}
            <li>#{step.restore_point_id} · {step.backup_type} · membership {step.membership}
              {#if step.data_required} · data {step.artifacts || 'unknown'}{:else} · catalog only{/if}
              {#if step.vm_targets?.length} · disks {step.vm_targets.join(', ')}{/if}
            </li>
          {/each}
        </ol>
      </div>
      <div>
        <h3 class="font-medium text-text">Write effects <span class="font-normal capitalize text-text-muted">· {request.effective_write_mode}</span></h3>
        <ul class="mt-1 list-disc space-y-1 pl-5 text-text-muted">
          {#each request.effects as effect (effect)}
            <li>{labels[effect] ?? effect}</li>
          {/each}
        </ul>
      </div>
    </div>
    {#if request.container}
      <div class="mt-4 text-sm">
        <h3 class="font-medium text-text">Docker container</h3>
        <dl class="mt-1 grid gap-2 text-text-muted sm:grid-cols-2">
          <div><dt>Name</dt><dd class="break-all text-text">{request.container.name}</dd></div>
          <div><dt>Image</dt><dd class="break-all text-text">{request.container.image}</dd></div>
          {#if request.container.image_id && !request.container.image_digest}
            <div class="sm:col-span-2"><dt>Saved image ID</dt>
              <dd class="break-all text-text">{request.container.image_id}</dd>
            </div>
          {/if}
          {#if request.container.image_digest}
            <div class="sm:col-span-2"><dt>Backup-time image digest</dt>
              <dd class="break-all text-text">{request.container.image_digest}</dd>
            </div>
          {/if}
        </dl>
        {#if request.container.binds?.length}
          <h4 class="mt-2 font-medium text-text">Docker bind settings</h4>
          <ul class="mt-1 list-disc space-y-1 break-all pl-5 text-text-muted">
            {#each request.container.binds as bind, index (index)}
              <li>{bind}</li>
            {/each}
          </ul>
        {/if}
        {#if request.container.volumes.length}
          <h4 class="mt-2 font-medium text-text">Volume targets</h4>
          <ul class="mt-1 list-disc space-y-1 pl-5 text-text-muted">
            {#each request.container.volumes as volume, index (index)}
              <li><span class="break-all">{volume.mount} → {volume.target}</span> · {labels[volume.write_mode]}</li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}
    {#if request.vm}<VmRestoreEffects effects={request.vm} />{/if}
    {#if request.warnings.length || request.persistence_warning}
      <ul class="mt-4 list-disc space-y-1 pl-5 text-sm text-warning">
        {#each request.warnings as warning (warning)}
          <li>{labels[warning] ?? warning}</li>
        {/each}
        {#if request.persistence_warning}<li>{request.persistence_warning}</li>{/if}
      </ul>
    {/if}
    {#if request.status === 'pending' && request.requires_metadata_review}
      {#if nativeApproval && request.review_digest}
        <form class="mt-4 text-sm" onsubmit={(event) => { event.preventDefault(); void reviewMetadata() }}>
          <label class="block max-w-md text-text">
            Backup Passphrase
            <input type="password" bind:value={passphrase} autocomplete="off" required disabled={reviewingMetadata}
              class="mt-1 w-full rounded-lg border border-border bg-surface-3 px-3 py-2 text-text" />
          </label>
          <button type="submit" disabled={!passphrase || reviewingMetadata}
            class="mt-2 hit-target rounded-lg bg-vault px-3 py-1 font-medium text-on-vault disabled:opacity-50">
            {reviewingMetadata ? 'Reading saved configuration…' : 'Review saved configuration'}
          </button>
        </form>
      {:else}
        <p class="mt-4 text-sm text-text-muted">Open this request from the signed-in Unraid Vault page to review the saved configuration.</p>
      {/if}
    {:else if !request.eligible_for_approval}
      <h3 class="mt-4 text-sm font-medium text-danger">Ineligible for approval</h3>
      <ul class="mt-1 list-disc space-y-1 pl-5 text-sm text-danger">
        {#each request.ineligibility_reasons as reason (reason)}
          <li>{labels[reason] ?? reason}</li>
        {/each}
      </ul>
    {/if}
    {#if request.manual_restore_required || request.terminal_reason === 'manual_restore_required'}
      <a href="#/restore" class="mt-2 inline-flex hit-target items-center text-sm text-info hover:underline">Open manual Restore wizard</a>
    {/if}
    {#if request.terminal_reason}
      <p class="mt-4 text-sm text-text-muted">{labels[request.terminal_reason] ?? request.terminal_reason}</p>
    {/if}
    {#if request.run_id}
      <div class="mt-4 border-t border-border pt-3 text-sm">
        <h3 class="font-medium text-text">Restore Run #{request.run_id}</h3>
        <p role="status" class="mt-1 capitalize text-text">{stopping ? 'Stopping…' : displayedStatus}
          {#if !stopping && displayedStatus === 'running' && request.run?.percent != null} · {request.run.percent}%{/if}
        </p>
        {#if canStop}
          <button type="button" class="mt-2 hit-target rounded-lg border border-danger px-3 py-1 font-medium text-danger"
            onclick={() => { confirmStop = true }}>Stop Restore Run</button>
        {/if}
        <ConfirmDialog show={confirmStop} title={`Stop Restore Run #${request.run_id}`}
          message="Stopping can leave the restore location partly restored."
          confirmLabel="Stop Restore Run" busy={sendingStop} confirmDisabled={!canStop}
          source={[{ label: 'Restore location', value: request.resolved_path }]}
          onconfirm={stop} oncancel={() => { confirmStop = false }} />
        {#if stopError}<p role="alert" class="mt-2 text-danger">{stopError}</p>{/if}
        {#if request.run}
          <p class="mt-1 text-text-muted">{request.run.items_done} of {request.run.items_total} items restored
            {#if request.run.items_failed} · {request.run.items_failed} failed{/if}
          </p>
          {#if request.run.vm_readiness}
            <VmReadinessResult readiness={request.run.vm_readiness} />
          {:else}
            <p class="mt-1 text-text-muted">Application health <span>{request.run.application_health}</span></p>
          {/if}
          {#if request.run.readiness_checks?.length}
            <ul class="mt-1 list-disc pl-5 text-text-muted">
              {#each request.run.readiness_checks as check (check)}
                <li>{labels[check] ?? check}</li>
              {/each}
            </ul>
          {/if}
        {/if}
        {#if request.approval}
          <p class="mt-1 text-text-muted">Approved by {request.approval.operator_name || 'a signed-in Unraid operator'}
            · {formatDate(request.approval.approved_at)}
          </p>
        {/if}
        <details class="mt-2" ontoggle={(event) => { if (event.currentTarget.open) void loadRunLog() }}>
          <summary class="cursor-pointer text-info hover:underline">Restore Run log</summary>
          {#if logsLoading}
            <p role="status" class="text-text-muted">Loading run log…</p>
          {:else if logsError}
            <p role="alert" class="text-danger">Could not load this Restore Run log.</p>
          {:else if logs.length}
            <ul class="mt-2 space-y-1 break-words text-text-muted">
              {#each logs as entry (entry.id)}
                <li>{entry.message}</li>
              {/each}
            </ul>
          {:else}
            <p class="text-text-muted">No log entries yet.</p>
          {/if}
          <button type="button" class="hit-target text-info hover:underline" disabled={logsLoading} onclick={loadRunLog}>Refresh log</button>
        </details>
      </div>
    {/if}
    {#if request.status === 'pending' && request.eligible_for_approval}
      {#if nativeApproval && request.review_digest}
        <button class="mt-4 hit-target rounded-lg bg-vault px-3 py-1 text-sm font-medium text-on-vault disabled:opacity-50"
          type="button" disabled={approving} onclick={() => { confirm = true }}>Approve and run</button>
        <ConfirmDialog show={confirm} title="Approve Restore Request"
          message="Start this Restore Request now."
          confirmLabel="Approve and run" confirmWord={request.container || request.effective_write_mode === 'replace' ? 'RESTORE' : ''}
          confirmDisabled={request.requires_passphrase && !passphrase} busy={approving}
          source={request.container
            ? [{ label: 'Docker container', value: request.container.name }, { label: 'Image', value: request.container.image_digest || request.container.image }]
            : [{ label: 'Restore location', value: request.resolved_path || (request.vm ? 'Reviewed VM disk and firmware paths' : '') }]}
          consequences={request.effects.map(effect => labels[effect] ?? effect)}
          onconfirm={approve} oncancel={cancelApproval}>
          <label class="mt-4 block text-sm text-text">
            Backup Passphrase{request.requires_passphrase ? '' : ' (if needed)'}
            <input type="password" bind:value={passphrase} autocomplete="off" disabled={approving}
              class="mt-1 w-full rounded-lg border border-border bg-surface-3 px-3 py-2 text-text" />
          </label>
        </ConfirmDialog>
      {:else}
        <p class="mt-4 text-sm text-text-muted">Open this request from the signed-in Unraid Vault page to approve it.</p>
      {/if}
    {/if}
    {#if approvalError}<p role="alert" class="mt-3 text-sm text-danger">{approvalError}</p>{/if}
    {#if loadError}<p role="alert" class="mt-3 text-sm text-danger">{loadError}</p>{/if}
  {:else}
    <p role="alert" class="text-sm text-danger">{loadError}</p>
  {/if}
</section>
