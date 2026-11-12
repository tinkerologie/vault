<script>
  import { api } from '../lib/api.js'
  import { formatDate } from '../lib/utils.js'

  let { requestId } = $props()
  const heading = $props.id()
  let review = $derived(api.getRestoreRequest(requestId))

  const labels = {
    clear_target_contents: 'Clear all existing target contents before writing files.',
    write_backed_up_entries: 'Write backed-up entries to the target.',
    overwrite_matching_entries: 'Overwrite matching entries with the backup.',
    keep_unrelated_entries: 'Keep existing entries the restore does not touch.',
    prune_replayed_entries: 'Remove entries written by earlier Backup Chain steps when the final listing shows they were deleted or excluded.',
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
  {#await review}
    <p role="status" class="text-sm text-text-muted">Loading Restore Request…</p>
  {:then request}
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
        <dd class="break-all text-text">{request.resolved_path || 'Unavailable'}</dd>
      </div>
      <div><dt class="text-text-muted">Expires</dt>
        <dd class="text-text"><time datetime={request.expires_at}>{formatDate(request.expires_at)}</time></dd>
      </div>
      <div><dt class="text-text-muted">Folder membership</dt><dd class="capitalize text-text">{request.membership}</dd></div>
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
    {#if request.warnings.length || request.persistence_warning}
      <ul class="mt-4 list-disc space-y-1 pl-5 text-sm text-warning">
        {#each request.warnings as warning (warning)}
          <li>{labels[warning] ?? warning}</li>
        {/each}
        {#if request.persistence_warning}<li>{request.persistence_warning}</li>{/if}
      </ul>
    {/if}
    {#if !request.eligible_for_approval}
      <h3 class="mt-4 text-sm font-medium text-danger">Ineligible for approval</h3>
      <ul class="mt-1 list-disc space-y-1 pl-5 text-sm text-danger">
        {#each request.ineligibility_reasons as reason (reason)}
          <li>{labels[reason] ?? reason}</li>
        {/each}
      </ul>
    {/if}
    {#if request.terminal_reason}
      <p class="mt-4 text-sm text-text-muted">{labels[request.terminal_reason] ?? request.terminal_reason}</p>
    {/if}
  {:catch error}
    <p role="alert" class="text-sm text-danger">{reviewError(error)}</p>
  {/await}
</section>
