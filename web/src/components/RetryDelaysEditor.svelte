<script>
  /**
   * RetryDelaysEditor — friendly editor for a JSON-array-of-seconds retry
   * delays string. Lets users pick a preset, edit per-row values with a unit
   * dropdown, and add/remove retries — replacing the raw JSON text input
   * that used to power Settings ▸ Retry Policy and the Job override section.
   *
   * Wire format (the bindable `value`) stays as a JSON array of integer
   * seconds (e.g. "[900,3600,14400]") or the empty string, which is what
   * the backend already expects.
   *
   * Internally the bound `value` is the source of truth — we never store a
   * second copy in component state. Each edit re-serialises the rows to
   * JSON and writes the new string straight back to `value`, so $derived
   * picks up the change and re-renders.
   *
   * Props:
   * - value: JSON-array-of-seconds string, or "" for "no delays set". Two-way
   *   bound via $bindable so parents can read edits live.
   * - placeholder: when non-empty, the component renders a "Use global
   *   default" toggle that lets the user clear the value entirely. Pass
   *   this in the per-job override context; omit on the global setting.
   */

  let { value = $bindable(''), placeholder = '' } = $props()

  const UNITS = [
    { id: 's', label: 'Seconds', mul: 1 },
    { id: 'm', label: 'Minutes', mul: 60 },
    { id: 'h', label: 'Hours', mul: 3600 },
    { id: 'd', label: 'Days', mul: 86400 },
  ]

  const PRESETS = [
    { id: 'default', label: 'Default (15 m / 1 h / 4 h)', delays: [900, 3600, 14400] },
    { id: 'aggressive', label: 'Aggressive (1 m / 5 m / 15 m)', delays: [60, 300, 900] },
    { id: 'patient', label: 'Patient (1 h / 6 h / 24 h)', delays: [3600, 21600, 86400] },
    { id: 'custom', label: 'Custom', delays: null },
  ]

  const DEFAULT_PRESET = PRESETS[0]

  /**
   * Return the largest unit that divides `seconds` evenly. Falls back to
   * seconds when none of the larger units fit. Guards against zero/negative
   * inputs by treating them as "1 second" so the row remains editable.
   */
  function largestUnit(seconds) {
    const n = Number.isFinite(seconds) ? Math.max(1, Math.round(seconds)) : 1
    for (const u of [...UNITS].reverse()) {
      if (n % u.mul === 0) {
        return { value: n / u.mul, unit: u.id }
      }
    }
    return { value: n, unit: 's' }
  }

  /** Convert one row back to seconds. Clamps at 1 to enforce non-zero delays. */
  function rowToSeconds(row) {
    const u = UNITS.find(x => x.id === row.unit) || UNITS[0]
    const n = Number.parseInt(row.value, 10)
    if (!Number.isFinite(n) || n < 1) return u.mul
    return n * u.mul
  }

  /** Parse a JSON-array-of-seconds string into a Row[] for editing. */
  function parseRows(raw) {
    const s = (raw || '').trim()
    if (s === '') return []
    try {
      const parsed = JSON.parse(s)
      if (!Array.isArray(parsed)) return []
      return parsed
        .map(n => Number.parseInt(n, 10))
        .filter(n => Number.isFinite(n) && n >= 1)
        .map(n => largestUnit(n))
    } catch {
      return []
    }
  }

  /** Detect which preset matches the given seconds array (or 'custom'). */
  function presetIdFor(secondsArray) {
    for (const p of PRESETS) {
      if (!p.delays) continue
      if (
        p.delays.length === secondsArray.length &&
        p.delays.every((d, i) => d === secondsArray[i])
      ) {
        return p.id
      }
    }
    return 'custom'
  }

  /** Serialise a Row[] back to JSON (or '' when empty). */
  function serialise(rowsArr) {
    if (rowsArr.length === 0) return ''
    return JSON.stringify(rowsArr.map(rowToSeconds))
  }

  // The bindable `value` is the canonical state. Everything else is derived.
  let supportsGlobal = $derived(Boolean(placeholder))
  let useGlobal = $derived(supportsGlobal && (value || '').trim() === '')
  let rows = $derived(parseRows(value))
  let preset = $derived(presetIdFor(rows.map(rowToSeconds)))

  /** Write a new rows array back to `value`. */
  function commit(nextRows) {
    value = serialise(nextRows)
  }

  // --- User actions -----------------------------------------------------------

  function applyPreset(id) {
    const p = PRESETS.find(x => x.id === id)
    // Selecting "Custom" is a no-op — the dropdown already mirrors the user's
    // edits whenever the rows stop matching a known preset.
    if (!p || !p.delays) return
    commit(p.delays.map(d => largestUnit(d)))
  }

  function updateRowValue(idx, raw) {
    const n = Number.parseInt(raw, 10)
    const clean = Number.isFinite(n) && n >= 1 ? n : 1
    commit(rows.map((r, i) => i === idx ? { ...r, value: clean } : r))
  }

  function updateRowUnit(idx, unit) {
    commit(rows.map((r, i) => i === idx ? { ...r, unit } : r))
  }

  function removeRow(idx) {
    if (rows.length <= 1) return
    commit(rows.filter((_, i) => i !== idx))
  }

  function addRow() {
    const last = rows[rows.length - 1]
    const next = last ? { value: last.value, unit: last.unit } : { value: 15, unit: 'm' }
    commit([...rows, next])
  }

  function toggleUseGlobal(checked) {
    if (checked) {
      // Turn on → emit empty string so the backend falls back to the global.
      value = ''
    } else {
      // Turn off → seed with the default preset so the editor has rows.
      commit(DEFAULT_PRESET.delays.map(d => largestUnit(d)))
    }
  }

  // When the editor is visible but `value` is empty (parent didn't pass a
  // placeholder, so there is no "Use global default" mode), seed with one
  // default row so the user has something to edit. This is the only spot
  // where we touch `value` from an effect, and it self-stabilises after a
  // single write (rows.length becomes 1 → condition is false).
  $effect(() => {
    if (!supportsGlobal && (value || '').trim() === '') {
      value = JSON.stringify([15 * 60])
    }
  })
</script>

<div class="retry-delays-editor space-y-3">
  {#if supportsGlobal}
    <label class="flex items-center gap-2 text-xs text-text-muted">
      <input
        type="checkbox"
        checked={useGlobal}
        onchange={(e) => toggleUseGlobal(e.currentTarget.checked)}
        class="w-4 h-4 rounded border-border bg-surface-3 text-vault focus:ring-2 focus:ring-vault/50"
      />
      <span>Use global default <span class="text-text-dim">({placeholder})</span></span>
    </label>
  {/if}

  {#if !useGlobal}
    <div class="flex items-center gap-2">
      <label for="retry-preset" class="text-xs font-medium text-text-muted whitespace-nowrap">Preset</label>
      <select
        id="retry-preset"
        value={preset}
        onchange={(e) => applyPreset(e.currentTarget.value)}
        class="flex-1 px-3 py-2 bg-surface-3 border border-border rounded-lg text-sm text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
      >
        {#each PRESETS as p (p.id)}
          <option value={p.id}>{p.label}</option>
        {/each}
      </select>
    </div>

    <ul class="space-y-2">
      {#each rows as row, idx (idx)}
        <li class="flex items-center gap-2">
          <span class="text-xs text-text-muted w-14 shrink-0">Retry {idx + 1}</span>
          <input
            type="number"
            min="1"
            value={row.value}
            oninput={(e) => updateRowValue(idx, e.currentTarget.value)}
            aria-label={`Retry ${idx + 1} value`}
            class="w-20 px-2 py-1.5 bg-surface-3 border border-border rounded text-sm text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
          />
          <select
            value={row.unit}
            onchange={(e) => updateRowUnit(idx, e.currentTarget.value)}
            aria-label={`Retry ${idx + 1} unit`}
            class="px-2 py-1.5 bg-surface-3 border border-border rounded text-sm text-text focus:outline-none focus:ring-2 focus:ring-vault/50 focus:border-vault"
          >
            {#each UNITS as u (u.id)}
              <option value={u.id}>{u.label}</option>
            {/each}
          </select>
          <button
            type="button"
            onclick={() => removeRow(idx)}
            disabled={rows.length <= 1}
            aria-label={`Remove retry ${idx + 1}`}
            title={rows.length <= 1 ? 'At least one retry is required' : 'Remove this retry'}
            class="ml-auto text-text-muted hover:text-danger disabled:opacity-30 disabled:cursor-not-allowed p-1 rounded focus:outline-none focus:ring-2 focus:ring-vault/50"
          >
            <svg aria-hidden="true" class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </li>
      {/each}
    </ul>

    <button
      type="button"
      onclick={addRow}
      class="inline-flex items-center gap-1 text-xs font-medium text-vault hover:text-vault-hover focus:outline-none focus:ring-2 focus:ring-vault/50 rounded px-1"
    >
      <svg aria-hidden="true" class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
        <path stroke-linecap="round" stroke-linejoin="round" d="M12 4v16m8-8H4" />
      </svg>
      Add retry
    </button>
  {/if}
</div>
