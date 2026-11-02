<script>
  // One console row, opened inline (#534): every entry the row stands for, with
  // its full text wrapped. It renders the same message and `key=value` tail as
  // Details and logLine, and never the raw structured details.
  //
  // A fold can hold thousands of lines, and the render window (#507) exists to
  // keep those off the page, so a long list arrives in steps.
  import { rowEntries } from '../lib/logrows.js'
  import { formatLine } from '../lib/logline.js'
  import { rowTime } from '../lib/tsformat.js'

  const STEP = 100
  let { id, row, border } = $props()
  let limit = $state(STEP)

  const entries = $derived(rowEntries(row))
  const hidden = $derived(entries.length - limit)
</script>

<div {id} data-log-details class="border-l-2 {border} bg-surface-2 pl-8 pr-3 py-1 text-text">
  <ul class="space-y-0.5" aria-label={entries.length > 1 ? `${entries.length} folded log lines` : 'Log line details'}>
    {#each entries.slice(0, limit) as entry (entry.id)}
      {@const line = formatLine(entry)}
      <li class="flex items-baseline gap-2">
        <span class="shrink-0 text-text-muted tabular-nums">{rowTime(entry.ts)}</span>
        <span class="min-w-0 whitespace-pre-wrap break-words">{line.message}{#if line.meta}<span class="text-text-muted">{` | ${line.meta}`}</span>{/if}</span>
      </li>
    {/each}
  </ul>
  {#if hidden > 0}
    <button type="button" onclick={() => { limit += STEP }}
      class="mt-1 min-h-6 px-2 text-xs rounded-md bg-surface-3 border border-border text-text-muted hover:text-text transition-colors">
      Show {Math.min(hidden, STEP)} more of {hidden}
    </button>
  {/if}
</div>
