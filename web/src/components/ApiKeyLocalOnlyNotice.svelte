<script>
  // The CLI only manages the API key, so it's offered for that subject alone.
  let { subject = 'the API key' } = $props()
  const isApiKey = $derived(subject === 'the API key')
</script>

<div class="text-sm text-text-muted">
  <p>You can only get to {subject} from the Unraid server:</p>
  <ul class="list-disc pl-5 mt-1 space-y-1">
    <li><strong>Unraid web UI:</strong> Vault → Settings → Security{isApiKey ? ' → API Access' : ''}</li>
    <li><strong>SSH tunnel:</strong> <code class="bg-surface px-1 rounded">ssh -L 24085:127.0.0.1:24085 root@&lt;host&gt;</code>, then open <code class="bg-surface px-1 rounded">http://127.0.0.1:24085</code></li>
    {#if isApiKey}
      <li><strong>CLI on the host:</strong> <code class="bg-surface px-1 rounded">vault api-key generate</code> or <code class="bg-surface px-1 rounded">vault api-key show</code></li>
    {/if}
  </ul>
</div>
