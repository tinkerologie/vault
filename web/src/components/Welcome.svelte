<script>
  let { onstart = () => {} } = $props()
</script>

<div class="flex flex-col items-center justify-center min-h-[60vh] text-center px-4">
  <div class="w-20 h-20 bg-vault/10 rounded-2xl flex items-center justify-center mb-6">
    <svg class="w-10 h-10 text-vault" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
  </div>
  <h1 class="text-3xl font-bold text-text mb-2">Welcome to Vault</h1>
  <p class="text-text-muted max-w-md mb-10">
    Protect your Unraid server with automated backups of Docker containers, VMs, and folders.
  </p>

  <div class="flex flex-col sm:flex-row gap-6 mb-10 text-left">
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault text-white text-sm font-bold shrink-0">1</span>
      <div>
        <p class="font-medium text-text">Add Storage</p>
        <p class="text-sm text-text-dim">Choose where backups are saved</p>
      </div>
    </div>
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault/20 text-vault text-sm font-bold shrink-0">2</span>
      <div>
        <p class="font-medium text-text">Create a Job</p>
        <p class="text-sm text-text-dim">Pick what to back up and when</p>
      </div>
    </div>
    <div class="flex items-start gap-3">
      <span class="flex items-center justify-center w-8 h-8 rounded-full bg-vault/20 text-vault text-sm font-bold shrink-0">3</span>
      <div>
        <p class="font-medium text-text">Run Backup</p>
        <p class="text-sm text-text-dim">Protect your data in minutes</p>
      </div>
    </div>
  </div>

  <button onclick={onstart} class="btn btn-primary text-base px-8 py-3">
    Get Started
  </button>
</div>
