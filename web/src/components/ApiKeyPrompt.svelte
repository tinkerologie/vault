<script>
  import { getApiKey, setApiKey } from '../lib/auth.svelte.js'
  import ApiKeyLocalOnlyNotice from './ApiKeyLocalOnlyNotice.svelte'
  import Modal from './Modal.svelte'

  const rejected = getApiKey() !== ''
  let key = $state('')
  let storageBlocked = $state(false)

  function submit(event) {
    event.preventDefault()
    const trimmed = key.trim()
    if (!trimmed) return
    if (!setApiKey(trimmed)) {
      storageBlocked = true
      return
    }
    location.reload()
  }
</script>

<!-- Nothing behind this works without a key, so there is nothing to close to:
     the dialog gets a title, but Esc and the close button leave it open. -->
<Modal show title="API key required" size="md" onclose={() => {}}>
  <form onsubmit={submit} class="space-y-3">
    <p class="text-sm text-text-muted">
      To use Vault with no key, log in to the Unraid web UI and open Vault there. Opening this address directly from another machine needs an API key, which can't be created from here.
    </p>
    <ApiKeyLocalOnlyNotice />
    {#if rejected}
      <p role="alert" class="text-sm text-danger">That key was not accepted.</p>
    {/if}
    {#if storageBlocked}
      <p role="alert" class="text-sm text-danger">This browser is blocking session storage, so the key can't be kept.</p>
    {/if}
    <label class="block text-sm font-medium text-text">
      API key
      <input data-autofocus type="password" autocomplete="off" bind:value={key} class="mt-1 w-full px-3 py-2 bg-surface border border-border rounded-lg text-sm text-text" />
    </label>
    <button type="submit" class="px-4 py-2 bg-vault text-on-vault rounded-lg text-sm font-medium">Continue</button>
  </form>
</Modal>
