<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { copyText } from '../lib/clipboard.js'
  import { formatDate } from '../lib/utils.js'
  import ApiKeyLocalOnlyNotice from './ApiKeyLocalOnlyNotice.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { remote, notify } = $props()
  const heading = $props.id()
  let keys = $state([])
  let loading = $state(true)
  let loadError = $state('')
  let name = $state('')
  let creating = $state(false)
  let issued = $state(null)
  let selected = $state(null)
  let revoking = $state(false)

  async function load() {
    loading = true
    loadError = ''
    try {
      keys = await api.listAssistantKeys()
    } catch (e) {
      loadError = e.message
    } finally {
      loading = false
    }
  }

  onMount(() => { void load() })

  async function create(event) {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || creating || issued || remote) return
    creating = true
    try {
      const { assistant_key, ...record } = await api.createAssistantKey(trimmed)
      keys = [...keys, record]
      issued = { id: record.id, token: assistant_key }
      name = ''
    } catch (e) {
      notify(e.message, 'error')
    } finally {
      creating = false
    }
  }

  async function copy() {
    if (issued && await copyText(issued.token)) {
      notify('Assistant Key copied', 'success')
    } else {
      notify('Could not copy the Assistant Key', 'error')
    }
  }

  async function revoke() {
    if (!selected || revoking || remote) return
    const { id } = selected
    revoking = true
    try {
      await api.revokeAssistantKey(id)
      keys = keys.filter(key => key.id !== id)
      if (issued?.id === id) issued = null
      selected = null
      notify('Assistant Key revoked', 'success')
    } catch (e) {
      notify(e.message, 'error')
    } finally {
      revoking = false
    }
  }
</script>

<section aria-labelledby={heading} class="bg-surface-2 border border-border rounded-xl overflow-hidden">
  <div class="px-5 py-4 border-b border-border">
    <h2 id={heading} class="text-base font-semibold text-text">Assistant Keys</h2>
    <p class="mt-1 text-xs text-text-muted">Give each assistant read-only access to the recovery catalog.</p>
  </div>
  <div class="px-5 py-4 space-y-4">
    {#if loading}
      <p role="status" class="text-sm text-text-muted">Loading Assistant Keys…</p>
    {:else if loadError}
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <p role="alert" class="text-danger">{loadError}</p>
        <button type="button" onclick={load} class="hit-target font-medium text-info hover:underline">Retry</button>
      </div>
    {:else if keys.length === 0}
      <p class="text-sm text-text-muted">No Assistant Keys</p>
    {:else}
      <ul class="divide-y divide-border">
        {#each keys as key (key.id)}
          <li class="flex items-center justify-between gap-4 py-2">
            <div class="min-w-0">
              <p class="text-sm font-medium text-text [overflow-wrap:anywhere]">{key.name}</p>
              <time datetime={key.created_at} class="text-xs text-text-muted">{formatDate(key.created_at)}</time>
            </div>
            {#if !remote}
              <button type="button" onclick={() => selected = key} aria-label={'Revoke ' + key.name}
                class="hit-target shrink-0 text-sm font-medium text-danger hover:underline">Revoke</button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if remote}
      <ApiKeyLocalOnlyNotice subject="Assistant Key creation and revocation" />
    {:else if issued}
      <div class="rounded-lg border border-border bg-surface p-3">
        <p role="status" class="text-sm text-text">Copy this Assistant Key now. Vault shows it once.</p>
        <code class="mt-2 block break-all select-all text-sm text-text">{issued.token}</code>
        <div class="mt-2 flex flex-wrap gap-3">
          <button type="button" onclick={copy} class="hit-target text-sm font-medium text-info hover:underline">Copy Assistant Key</button>
          <button type="button" onclick={() => issued = null} class="hit-target text-sm font-medium text-text-muted hover:underline">Dismiss Assistant Key</button>
        </div>
      </div>
    {:else if !loading && !loadError}
      <form onsubmit={create} class="flex flex-wrap items-end gap-3">
        <label class="min-w-40 flex-1 text-sm font-medium text-text">
          Assistant name
          <input type="text" bind:value={name} maxlength="100" required autocomplete="off" disabled={creating}
            class="mt-1.5 block w-full rounded-lg border border-border bg-surface px-3 py-2 text-text focus:border-vault focus:outline-none" />
        </label>
        <button type="submit" disabled={creating || !name.trim()}
          class="hit-target rounded-lg bg-vault px-4 py-2 text-sm font-semibold text-on-vault hover:bg-vault-dark disabled:opacity-50">
          {creating ? 'Creating…' : 'Create Assistant Key'}
        </button>
      </form>
    {/if}
  </div>
</section>

<ConfirmDialog show={selected !== null && !remote}
  title="Revoke Assistant Key"
  message="This assistant will lose access to Vault."
  source={selected ? [{ label: 'Assistant', value: selected.name }] : []}
  confirmLabel="Revoke key"
  busy={revoking}
  onconfirm={revoke}
  oncancel={() => selected = null} />
