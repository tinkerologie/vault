<script>
  import { formatClockTime } from '../lib/utils.js'
  import { isCustomCron, cronError, timeSlots, serverWallClock, formatRunDate } from '../lib/cron.js'
  import { api } from '../lib/api.js'
  import { ICONS } from '../lib/icons.js'

  // `allowManual` offers "Manual only", which is an empty schedule: the server
  // never runs the job by itself. Only a job's own schedule can be manual; the
  // verification and full-backup schedules are switched off by their checkbox.
  let { value = $bindable('0 2 * * *'), onchange = () => {}, allowManual = false } = $props()

  // Two builders can be mounted at once (verification and full backup), so
  // every id is derived from this instance's id.
  const uid = $props.id()

  // Parse current cron into structured state
  let frequency = $state('daily')
  let hour = $state(2)
  let minute = $state(0)
  let weekday = $state(0) // 0=Sun
  /** @type {number | string} */
  let monthday = $state(1) // numeric day or 'L' for last day
  let month = $state(1) // 1=Jan for yearly
  let weekdays = $state([1, 2, 3, 4, 5]) // Mon-Fri default for daily
  // The raw expression in custom mode. Kept exactly as typed: reformatting a
  // hand-written cron is a good way to change what it means.
  let customCron = $state('')
  // The expression this picker last wrote. When it comes back through the
  // binding it is not re-parsed: one day in Daily mode builds the same cron as
  // Weekly, and re-reading it would flip the frequency under the user.
  let lastBuilt = null
  // Set when the user tries to clear the last day in Daily mode.
  let lastDayBlocked = $state(false)

  let frequencyOptions = $derived([
    ...(allowManual ? [{ value: 'manual', label: 'Manual only' }] : []),
    { value: 'daily', label: 'Daily' },
    { value: 'weekly', label: 'Weekly' },
    { value: 'monthly', label: 'Monthly' },
    { value: 'yearly', label: 'Yearly' },
    { value: 'custom', label: 'Custom' },
  ])

  const dayNames = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
  const dayNamesFull = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const monthNames = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']

  // Half-hour slots, plus the schedule's own time when it falls between them,
  // so an off-grid minute is shown and kept rather than rounded (#682).
  let slots = $derived(timeSlots(hour, minute).map((s) => ({ ...s, label: formatClockTime(s.hour, s.minute) })))
  let timeValue = $derived(`${hour}:${minute}`)

  function onTimeChange(e) {
    const [h, m] = e.currentTarget.value.split(':').map(Number)
    hour = h
    minute = m
    buildCron()
  }

  // Build cron from UI state
  function buildCron() {
    const h = hour
    const min = minute

    let cron
    if (frequency === 'manual') {
      cron = ''
    } else if (frequency === 'daily') {
      if (weekdays.length === 7) {
        cron = `${min} ${h} * * *`
      } else {
        const days = [...weekdays].sort().join(',')
        cron = `${min} ${h} * * ${days}`
      }
    } else if (frequency === 'weekly') {
      cron = `${min} ${h} * * ${weekday}`
    } else if (frequency === 'monthly') {
      cron = `${min} ${h} ${monthday} * *`
    } else if (frequency === 'yearly') {
      cron = `${min} ${h} ${monthday} ${month} *`
    } else if (frequency === 'custom') {
      cron = customCron
    } else {
      cron = `${min} ${h} * * *`
    }
    lastBuilt = cron
    value = cron
    onchange(cron)
  }

  // Parse initial cron value into UI state
  function parseCron(cron) {
    // In custom mode the user may type an expression that happens to match
    // a preset shape, or clear the box; keep frequency as 'custom' so the
    // input doesn't vanish.
    if (frequency === 'custom') return
    if (cron === lastBuilt) return
    if (!(cron || '').trim()) {
      // An empty schedule is a manual-only job. Show it as such rather than
      // as the picker's default, which saving would then write back (#682).
      if (allowManual) frequency = 'manual'
      return
    }
    // Anything the presets cannot express is edited as raw text. Without this
    // the picker mislabelled such a schedule and then overwrote it with a
    // preset the moment anything was touched (issue #309).
    if (isCustomCron(cron)) {
      frequency = 'custom'
      customCron = cron.trim()
      return
    }
    const parts = cron.trim().split(/\s+/)
    if (parts.length !== 5) return

    const [min, hr, dom, mon, dow] = parts
    minute = parseInt(min) || 0
    hour = parseInt(hr) || 0

    if (mon !== '*' && dom !== '*') {
      // Yearly: specific month and day
      frequency = 'yearly'
      month = parseInt(mon) || 1
      monthday = dom === 'L' ? 'L' : (parseInt(dom) || 1)
    } else if (dom !== '*' && dow === '*') {
      // Monthly: specific day of month
      frequency = 'monthly'
      monthday = dom === 'L' ? 'L' : (parseInt(dom) || 1)
    } else if (dow !== '*' && dom === '*') {
      const dowParts = dow.split(',').map(Number)
      if (dowParts.length === 1) {
        // Weekly: single day
        frequency = 'weekly'
        weekday = dowParts[0]
      } else {
        // Daily with specific weekdays
        frequency = 'daily'
        weekdays = dowParts
      }
    } else if (dom === '*' && dow === '*') {
      // Daily, every day
      frequency = 'daily'
      weekdays = [0, 1, 2, 3, 4, 5, 6]
    }
  }

  // Initialize from prop
  $effect(() => {
    parseCron(value)
  })

  function toggleWeekday(day) {
    if (weekdays.includes(day)) {
      // An empty day list used to mean every day. Keep at least one instead.
      if (weekdays.length === 1) {
        lastDayBlocked = true
        return
      }
      weekdays = weekdays.filter((d) => d !== day)
    } else {
      weekdays = [...weekdays, day]
    }
    lastDayBlocked = false
    buildCron()
  }

  let customError = $derived(frequency === 'custom' ? cronError(customCron) : null)

  // Human-readable description
  let description = $derived.by(() => {
    const time = formatClockTime(hour, minute)
    if (frequency === 'manual') return 'Runs only when started with Run Now'
    if (frequency === 'daily') {
      if (weekdays.length === 7) return `Every day at ${time}`
      if (weekdays.length === 5 && [1,2,3,4,5].every(d => weekdays.includes(d))) return `Every weekday at ${time}`
      if (weekdays.length === 2 && [0,6].every(d => weekdays.includes(d))) return `Every weekend at ${time}`
      return `Every ${[...weekdays].sort().map(d => dayNames[d]).join(', ')} at ${time}`
    }
    if (frequency === 'weekly') return `Every ${dayNamesFull[weekday]} at ${time}`
    if (frequency === 'monthly') {
      if (monthday === 'L') return `Monthly on the last day at ${time}`
      return `Monthly on the ${ordinal(monthday)} at ${time}`
    }
    if (frequency === 'yearly') {
      if (monthday === 'L') return `Yearly on the last day of ${monthNames[month - 1]} at ${time}`
      return `Yearly on ${monthNames[month - 1]} ${ordinal(monthday)} at ${time}`
    }
    if (frequency === 'custom') {
      if (customError) return customError
      return `Runs on the schedule ${customCron.trim()}`
    }
    return ''
  })

  // Upcoming runs come from the server, on its clock and in its timezone,
  // because that is where cron fires. Debounced so typing a custom expression
  // doesn't send a request per keystroke; a stale response is dropped.
  /** @type {{ runs: string[], timezone: string, utc_offset: string } | null} */
  let preview = $state(null)
  let previewFailed = $state(false)
  let previewSeq = 0
  $effect(() => {
    const spec = (value || '').trim()
    const skip = !spec || (frequency === 'custom' && customError)
    const seq = ++previewSeq
    if (skip) {
      preview = null
      previewFailed = false
      return
    }
    const timer = setTimeout(async () => {
      try {
        const res = await api.previewSchedule(spec, 3)
        if (seq !== previewSeq) return
        preview = res && Array.isArray(res.runs) ? res : null
        previewFailed = !preview
      } catch {
        if (seq !== previewSeq) return
        preview = null
        previewFailed = true
      }
    }, 300)
    return () => clearTimeout(timer)
  })

  let upcoming = $derived(
    (preview?.runs || [])
      .map(serverWallClock)
      .filter(Boolean)
      .map((w) => ({ date: formatRunDate(w), time: formatClockTime(w.hour, w.minute) })),
  )

  // Switching to Custom seeds the box with the schedule the job already has,
  // so the mode starts from what is in force rather than clearing it — an
  // empty expression means "manual only" to the server.
  function selectFrequency(next) {
    if (next === 'custom' && !customCron.trim()) {
      customCron = (value || '').trim() || `${minute} ${hour} * * *`
    }
    frequency = next
    lastDayBlocked = false
    buildCron()
  }

  function onCustomInput(e) {
    customCron = e.currentTarget.value
    buildCron()
  }

  function ordinal(n) {
    const s = ['th', 'st', 'nd', 'rd']
    const v = n % 100
    return n + (s[(v - 20) % 10] || s[v] || s[0])
  }

  function daysInMonth(m) {
    return new Date(2024, m, 0).getDate()
  }

  let yearlyDayCount = $derived(daysInMonth(month))

  const selectClass = 'w-full bg-surface-3 border border-border rounded-lg px-3 py-2 max-lg:min-h-11 text-sm text-text'
</script>

{#snippet timeSelect(idSuffix)}
  <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-{idSuffix}">Time</label>
  <select id="{uid}-{idSuffix}" onchange={onTimeChange} class={selectClass}>
    {#each slots as slot (slot.value)}
      <option value={slot.value} selected={slot.value === timeValue}>{slot.label}</option>
    {/each}
  </select>
{/snippet}

<div class="space-y-4">
  <!-- Frequency selector -->
  <div>
    <span id="{uid}-frequency" class="block text-sm font-medium text-text-muted mb-1.5">Frequency</span>
    <div role="group" aria-labelledby="{uid}-frequency" class="grid grid-cols-3 {allowManual ? 'sm:grid-cols-6' : 'sm:grid-cols-5'} gap-1 bg-surface-3 rounded-lg p-1">
      {#each frequencyOptions as opt (opt.value)}
        <button
          type="button"
          aria-pressed={frequency === opt.value}
          onclick={() => selectFrequency(opt.value)}
          class="px-2 py-2 max-lg:min-h-11 text-sm rounded-md transition-all {frequency === opt.value ? 'bg-vault text-on-vault font-medium shadow-sm' : 'text-text-muted hover:text-text'}"
        >
          {opt.label}
        </button>
      {/each}
    </div>
  </div>

  {#if frequency === 'daily'}
    <div>
      {@render timeSelect('time-d')}
    </div>

    <!-- Weekday toggles -->
    <div>
      <span id="{uid}-days" class="block text-xs font-medium text-text-muted mb-1.5">Days</span>
      <div role="group" aria-labelledby="{uid}-days" aria-describedby={lastDayBlocked ? `${uid}-days-hint` : undefined} class="flex gap-1">
        {#each dayNames as day, i (i)}
          <button
            type="button"
            aria-pressed={weekdays.includes(i)}
            aria-label={dayNamesFull[i]}
            onclick={() => toggleWeekday(i)}
            class="flex-1 py-2 max-lg:min-h-11 text-xs font-medium rounded-lg transition-all {weekdays.includes(i) ? 'bg-vault text-on-vault' : 'bg-surface-3 text-text-muted hover:text-text border border-border'}"
          >
            {day}
          </button>
        {/each}
      </div>
      <p id="{uid}-days-hint" class="text-xs text-text-dim mt-1" role="status">
        {#if lastDayBlocked}
          Keep at least one day.{allowManual ? ' To stop scheduled runs, choose Manual only.' : ''}
        {/if}
      </p>
    </div>

  {:else if frequency === 'weekly'}
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-weekday">Day of Week</label>
        <select id="{uid}-weekday" bind:value={weekday} onchange={buildCron} class={selectClass}>
          {#each dayNamesFull as day, i (i)}
            <option value={i}>{day}</option>
          {/each}
        </select>
      </div>
      <div>
        {@render timeSelect('time-w')}
      </div>
    </div>

  {:else if frequency === 'monthly'}
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-monthday">Day of Month</label>
        <select id="{uid}-monthday" bind:value={monthday} onchange={buildCron} class={selectClass}>
          <option value={1}>First day of month</option>
          <option value="L">Last day of month</option>
          {#each Array(30) as _s, d (d)}
            <option value={d + 2}>{d + 2}</option>
          {/each}
        </select>
      </div>
      <div>
        {@render timeSelect('time-m')}
      </div>
    </div>

  {:else if frequency === 'yearly'}
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-month">Month</label>
        <select id="{uid}-month" bind:value={month} onchange={() => { if (typeof monthday === 'number' && monthday > daysInMonth(month)) monthday = daysInMonth(month); buildCron() }} class={selectClass}>
          {#each monthNames as name, i (i)}
            <option value={i + 1}>{name}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-monthday-y">Day</label>
        <select id="{uid}-monthday-y" bind:value={monthday} onchange={buildCron} class={selectClass}>
          <option value={1}>First day</option>
          <option value="L">Last day</option>
          {#each Array(Math.max(0, yearlyDayCount - 1)) as _s, d (d)}
            <option value={d + 2}>{d + 2}</option>
          {/each}
        </select>
      </div>
      <div>
        {@render timeSelect('time-y')}
      </div>
    </div>

  {:else if frequency === 'custom'}
    <div>
      <label class="block text-xs font-medium text-text-muted mb-1" for="{uid}-custom-cron">Cron expression</label>
      <input
        id="{uid}-custom-cron"
        type="text"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        value={customCron}
        oninput={onCustomInput}
        placeholder="e.g. 0 */3 */2 * *"
        aria-invalid={customError ? 'true' : undefined}
        aria-describedby="{uid}-custom-cron-help"
        class="w-full bg-surface-3 border rounded-lg px-3 py-2 max-lg:min-h-11 text-sm text-text font-mono {customError ? 'border-danger' : 'border-border'}"
      />
      <p id="{uid}-custom-cron-help" class="mt-1 text-xs {customError ? 'text-danger' : 'text-text-dim'}">
        {customError ??
          'Five fields: minute, hour, day of month, month, day of week. Use L in the day-of-month field for the last day.'}
      </p>
    </div>
  {/if}

  <!-- Human-readable preview, with the next runs on the server's clock -->
  <div class="px-3 py-2 bg-vault/5 border border-vault/20 rounded-lg space-y-1.5">
    <div class="flex items-center gap-2">
      <svg aria-hidden="true" class="w-4 h-4 text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d={ICONS.clock}/></svg>
      <span class="text-sm text-text">{description}</span>
    </div>
    {#if upcoming.length > 0}
      <div class="pl-6 text-xs text-text-muted" data-testid="schedule-next-runs">
        <span class="font-medium">Next runs</span>
        <span class="text-text-dim">(server time, {preview?.timezone || 'UTC'} {preview?.utc_offset})</span>
        <ul class="mt-0.5 tabular-nums">
          {#each upcoming as run, i (i)}
            <li>{run.date}, {run.time}</li>
          {/each}
        </ul>
      </div>
    {:else if previewFailed && frequency !== 'manual'}
      <p class="pl-6 text-xs text-text-dim">Upcoming run times are unavailable right now. The schedule still runs on the server's clock.</p>
    {/if}
  </div>
</div>
