<div class="text-sm text-text-muted">
  <p>Create or manage the API key from the Vault host, using any of these:</p>
  <ul class="list-disc pl-5 mt-1 space-y-1">
    <li><strong>Unraid plugin page:</strong> Settings → Security → API Access</li>
    <li><strong>SSH tunnel:</strong> <code class="bg-surface px-1 rounded">ssh -L 24085:127.0.0.1:24085 root@&lt;host&gt;</code>, then open <code class="bg-surface px-1 rounded">http://127.0.0.1:24085</code></li>
    <li><strong>CLI on the host:</strong> <code class="bg-surface px-1 rounded">vault api-key generate</code> or <code class="bg-surface px-1 rounded">vault api-key show</code></li>
  </ul>
</div>
