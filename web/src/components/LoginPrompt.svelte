<script>
  import { setApiKey, validateApiKey } from '../lib/auth.svelte.js'

  let { onauthenticated = () => {} } = $props()

  let key = $state('')
  let error = $state('')
  let checking = $state(false)

  async function submit(e) {
    e.preventDefault()
    if (!key.trim()) {
      error = 'Please enter an API key'
      return
    }
    checking = true
    error = ''
    const valid = await validateApiKey(key.trim())
    if (valid) {
      setApiKey(key.trim())
      onauthenticated()
    } else {
      error = 'Invalid API key'
    }
    checking = false
  }
</script>

<div class="min-h-screen flex items-center justify-center bg-surface px-4">
  <div class="w-full max-w-sm">
    <div class="bg-surface-2 border border-border rounded-xl shadow-lg p-6">
      <div class="flex items-center justify-center gap-3 mb-6">
        <div class="w-10 h-10 bg-vault rounded-lg flex items-center justify-center shrink-0">
          <svg aria-hidden="true" class="w-6 h-6 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
        </div>
        <div>
          <h1 class="text-lg font-bold text-text">VAULT</h1>
          <p class="text-xs text-text-dim -mt-0.5">Backup Manager</p>
        </div>
      </div>

      <p class="text-sm text-text-muted text-center mb-5">This server requires an API key to access.</p>

      <form onsubmit={submit}>
        <label for="api-key" class="block text-sm font-medium text-text-muted mb-1.5">API Key</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input
          id="api-key"
          type="password"
          bind:value={key}
          placeholder="Enter your API key"
          class="w-full px-3 py-2.5 text-sm bg-surface-3 border border-border rounded-lg text-text placeholder:text-text-dim focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
          disabled={checking}
          autofocus
        />
        {#if error}
          <p class="text-xs text-danger mt-1.5">{error}</p>
        {/if}
        <button
          type="submit"
          disabled={checking}
          class="w-full mt-4 px-4 py-2.5 text-sm font-medium text-white bg-vault hover:bg-vault-dark rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {checking ? 'Verifying...' : 'Authenticate'}
        </button>
      </form>
    </div>
  </div>
</div>
