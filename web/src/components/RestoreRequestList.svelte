<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatDate } from '../lib/utils.js'

  let { history = false } = $props()
  const heading = $props.id()
  let requests = $state([])
  let cursor = $state('')
  let loading = $state(true)
  let error = $state('')
  let warning = $state('')

  async function load(before = '') {
    loading = true
    error = ''
    try {
      const page = await api.listRestoreRequests({ status: history ? 'withdrawn,expired' : 'pending', before })
      requests = before ? [...requests, ...page.requests] : page.requests
      cursor = page.next_before || ''
      warning = page.persistence_warning || ''
    } catch (e) {
      error = e.status === 403 ? 'Operator access required to view Restore Requests.' : 'Could not load Restore Requests.'
    } finally {
      loading = false
    }
  }

  onMount(() => { void load() })
</script>

<section aria-labelledby={heading} class="mb-4 rounded-xl border border-border bg-surface-2 p-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 id={heading} class="text-base font-semibold text-text">{history ? 'Request history' : 'Pending Restore Requests'}</h2>
    <button type="button" class="hit-target text-sm font-medium text-info hover:underline disabled:opacity-50"
      disabled={loading} onclick={() => load()}>Refresh requests</button>
  </div>

  {#if error}
    <p role="alert" class="text-sm text-danger">{error}</p>
  {:else if loading && requests.length === 0}
    <p role="status" class="text-sm text-text-muted">Loading Restore Requests…</p>
  {:else if requests.length === 0}
    <p class="text-sm text-text-muted">{history ? 'No withdrawn or expired Restore Requests.' : 'No pending Restore Requests.'}</p>
  {/if}

  {#if warning}
    <p role="status" class="text-sm text-warning">{warning}</p>
  {/if}

  {#if requests.length > 0}
    <ul class="divide-y divide-border">
      {#each requests as request (request.id)}
        <li class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2">
          <a class="hit-target text-sm font-medium text-info [overflow-wrap:anywhere] hover:underline"
            href={'#/restore?request=' + encodeURIComponent(request.id)}>{request.item.item_name} · {request.job_name}</a>
          <span class="text-xs text-text-muted">
            {#if history}
              <span class="capitalize">{request.status}</span>
              {#if request.terminal_at} · <time datetime={request.terminal_at}>{formatDate(request.terminal_at)}</time>{/if}
            {:else}
              Expires <time datetime={request.expires_at}>{formatDate(request.expires_at)}</time>
            {/if}
          </span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if cursor}
    <button type="button" class="hit-target text-sm font-medium text-info hover:underline disabled:opacity-50"
      disabled={loading} onclick={() => load(cursor)}>Load more requests</button>
  {/if}
</section>
