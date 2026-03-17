<script>
  let { value = $bindable('0 2 * * *'), onchange = () => {} } = $props()

  // Parse current cron into structured state
  let frequency = $state('daily')
  let hour = $state(2)
  let minute = $state(0)
  let weekday = $state(0) // 0=Sun
  let monthday = $state(1)
  let month = $state(1) // 1=Jan for yearly
  let weekdays = $state([1, 2, 3, 4, 5]) // Mon-Fri default for daily

  const frequencyOptions = [
    { value: 'daily', label: 'Daily' },
    { value: 'weekly', label: 'Weekly' },
    { value: 'monthly', label: 'Monthly' },
    { value: 'yearly', label: 'Yearly' },
  ]

  const dayNames = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
  const dayNamesFull = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const monthNames = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']

  // Generate 30-min time slots in 12h format
  const timeSlots = []
  for (let h = 0; h < 24; h++) {
    for (const m of [0, 30]) {
      const h12 = h === 0 ? 12 : h > 12 ? h - 12 : h
      const ampm = h < 12 ? 'AM' : 'PM'
      const label = `${h12}:${m.toString().padStart(2, '0')} ${ampm}`
      timeSlots.push({ label, hour: h, minute: m })
    }
  }

  let selectedTimeIndex = $state(4) // default 2:00 AM

  // Build cron from UI state
  function buildCron() {
    const slot = timeSlots[selectedTimeIndex]
    const h = slot?.hour ?? hour
    const min = slot?.minute ?? minute
    hour = h
    minute = min

    let cron
    if (frequency === 'daily') {
      if (weekdays.length === 7 || weekdays.length === 0) {
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
    } else {
      cron = `${min} ${h} * * *`
    }
    value = cron
    onchange(cron)
  }

  // Parse initial cron value into UI state
  function parseCron(cron) {
    if (!cron) return
    const parts = cron.trim().split(/\s+/)
    if (parts.length !== 5) return

    const [min, hr, dom, mon, dow] = parts
    minute = parseInt(min) || 0
    hour = parseInt(hr) || 0

    // Find matching time slot
    const idx = timeSlots.findIndex(s => s.hour === hour && s.minute === minute)
    if (idx >= 0) selectedTimeIndex = idx
    else {
      // Find nearest 30-min slot
      const nearest = timeSlots.reduce((best, slot, i) => {
        const diff = Math.abs(slot.hour * 60 + slot.minute - (hour * 60 + minute))
        return diff < best.diff ? { diff, i } : best
      }, { diff: Infinity, i: 0 })
      selectedTimeIndex = nearest.i
    }

    if (mon !== '*' && dom !== '*') {
      // Yearly: specific month and day
      frequency = 'yearly'
      month = parseInt(mon) || 1
      monthday = parseInt(dom) || 1
    } else if (dom !== '*' && dow === '*') {
      // Monthly: specific day of month
      frequency = 'monthly'
      monthday = parseInt(dom) || 1
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
      weekdays = weekdays.filter((d) => d !== day)
    } else {
      weekdays = [...weekdays, day]
    }
    buildCron()
  }

  function formatTime(h, m) {
    const h12 = h === 0 ? 12 : h > 12 ? h - 12 : h
    const ampm = h < 12 ? 'AM' : 'PM'
    return `${h12}:${m.toString().padStart(2, '0')} ${ampm}`
  }

  // Human-readable description
  let description = $derived.by(() => {
    const time = formatTime(hour, minute)
    if (frequency === 'daily') {
      if (weekdays.length === 7 || weekdays.length === 0) return `Every day at ${time}`
      if (weekdays.length === 5 && [1,2,3,4,5].every(d => weekdays.includes(d))) return `Every weekday at ${time}`
      if (weekdays.length === 2 && [0,6].every(d => weekdays.includes(d))) return `Every weekend at ${time}`
      return `Every ${weekdays.sort().map(d => dayNames[d]).join(', ')} at ${time}`
    }
    if (frequency === 'weekly') return `Every ${dayNamesFull[weekday]} at ${time}`
    if (frequency === 'monthly') return `Monthly on the ${ordinal(monthday)} at ${time}`
    if (frequency === 'yearly') return `Yearly on ${monthNames[month - 1]} ${ordinal(monthday)} at ${time}`
    return ''
  })

  function ordinal(n) {
    const s = ['th', 'st', 'nd', 'rd']
    const v = n % 100
    return n + (s[(v - 20) % 10] || s[v] || s[0])
  }
</script>

<div class="space-y-4">
  <!-- Frequency selector -->
  <div>
    <span class="block text-sm font-medium text-text-muted mb-1.5">Frequency</span>
    <div class="grid grid-cols-4 gap-1 bg-surface-3 rounded-lg p-1">
      {#each frequencyOptions as opt (opt.value)}
        <button
          type="button"
          onclick={() => { frequency = opt.value; buildCron() }}
          class="px-3 py-2 text-sm rounded-md transition-all {frequency === opt.value ? 'bg-vault text-white font-medium shadow-sm' : 'text-text-muted hover:text-text'}"
        >
          {opt.label}
        </button>
      {/each}
    </div>
  </div>

  {#if frequency === 'daily'}
    <!-- Time picker -->
    <div>
      <label class="block text-xs font-medium text-text-muted mb-1" for="sched-time-d">Time</label>
      <select id="sched-time-d" bind:value={selectedTimeIndex} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
        {#each timeSlots as slot, i (i)}
          <option value={i}>{slot.label}</option>
        {/each}
      </select>
    </div>

    <!-- Weekday toggles -->
    <div>
      <span class="block text-xs font-medium text-text-muted mb-1.5">Days</span>
      <div class="flex gap-1">
        {#each dayNames as day, i (i)}
          <button
            type="button"
            onclick={() => toggleWeekday(i)}
            class="flex-1 py-2 text-xs font-medium rounded-lg transition-all {weekdays.includes(i) ? 'bg-vault text-white' : 'bg-surface-3 text-text-muted hover:text-text border border-border'}"
          >
            {day}
          </button>
        {/each}
      </div>
    </div>

  {:else if frequency === 'weekly'}
    <div class="grid grid-cols-2 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-weekday">Day of Week</label>
        <select id="sched-weekday" bind:value={weekday} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each dayNamesFull as day, i (i)}
            <option value={i}>{day}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-time-w">Time</label>
        <select id="sched-time-w" bind:value={selectedTimeIndex} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each timeSlots as slot, i (i)}
            <option value={i}>{slot.label}</option>
          {/each}
        </select>
      </div>
    </div>

  {:else if frequency === 'monthly'}
    <div class="grid grid-cols-2 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-monthday">Day of Month</label>
        <select id="sched-monthday" bind:value={monthday} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each Array(28) as _s, d (d)}
            <option value={d + 1}>{d + 1}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-time-m">Time</label>
        <select id="sched-time-m" bind:value={selectedTimeIndex} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each timeSlots as slot, i (i)}
            <option value={i}>{slot.label}</option>
          {/each}
        </select>
      </div>
    </div>

  {:else if frequency === 'yearly'}
    <div class="grid grid-cols-3 gap-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-month">Month</label>
        <select id="sched-month" bind:value={month} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each monthNames as name, i (i)}
            <option value={i + 1}>{name}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-monthday-y">Day</label>
        <select id="sched-monthday-y" bind:value={monthday} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each Array(28) as _s, d (d)}
            <option value={d + 1}>{d + 1}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="sched-time-y">Time</label>
        <select id="sched-time-y" bind:value={selectedTimeIndex} onchange={buildCron} class="w-full bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text">
          {#each timeSlots as slot, i (i)}
            <option value={i}>{slot.label}</option>
          {/each}
        </select>
      </div>
    </div>
  {/if}

  <!-- Human-readable preview -->
  <div class="flex items-center gap-2 px-3 py-2 bg-vault/5 border border-vault/20 rounded-lg">
    <svg aria-hidden="true" class="w-4 h-4 text-vault shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
    <span class="text-sm text-text">{description}</span>
  </div>
</div>
