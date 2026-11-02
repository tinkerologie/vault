<script>
  import { getApiKey, setApiKey } from '../lib/auth.svelte.js'
  import ApiKeyLocalOnlyNotice from './ApiKeyLocalOnlyNotice.svelte'

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

<div class="fixed inset-0 z-[150] flex items-center justify-center bg-surface p-4">
  <form onsubmit={submit} class="w-full max-w-md bg-surface-2 border border-border rounded-xl p-5 space-y-3">
    <h1 class="text-base font-semibold text-text">API key required</h1>
    <p class="text-sm text-text-muted">
      This Vault only accepts remote connections with an API key, and a key can't be created from here.
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
      <input type="password" autocomplete="off" bind:value={key} class="mt-1 w-full px-3 py-2 bg-surface border border-border rounded-lg text-sm text-text" />
    </label>
    <button type="submit" class="px-4 py-2 bg-vault text-on-vault rounded-lg text-sm font-medium">Continue</button>
  </form>
</div>
