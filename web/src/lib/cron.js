// Cron helpers shared by the schedule picker and the schedule summaries.
//
// The server accepts any expression `scheduler.ValidateSchedule` accepts —
// standard 5-field cron, the @daily-style descriptors, and Vault's own `L`
// (last day of month) token in the day-of-month field. The UI, however, only
// ever *built* the handful of shapes its Daily/Weekly/Monthly/Yearly presets
// produce, and it also *assumed* every schedule it was shown was one of them.
// So a job whose schedule came from anywhere else (the API, a hand-edited
// database, a backup restored from another install) was mislabelled and then
// silently overwritten the first time the picker rebuilt its cron.
//
// These helpers answer the one question both places need: is this expression
// something a preset can represent? Issue #309.

/** Descriptors robfig/cron's standard parser understands. */
const DESCRIPTORS = new Set(['@yearly', '@annually', '@monthly', '@weekly', '@daily', '@midnight', '@hourly'])

/** A plain, non-negative integer field — no step, range or list syntax. */
function isPlainInt(field) {
  return /^\d+$/.test(field)
}

function inRange(field, min, max) {
  if (!isPlainInt(field)) return false
  const n = Number(field)
  return n >= min && n <= max
}

/**
 * True when cron is a valid expression that none of the picker's presets can
 * represent, so it has to be shown and edited as raw text.
 *
 * An empty expression is not custom — it means "manual only".
 */
export function isCustomCron(cron) {
  const spec = (cron || '').trim()
  if (!spec) return false
  // A descriptor is perfectly valid and no preset builds one.
  if (spec.startsWith('@')) return true

  const parts = spec.split(/\s+/)
  if (parts.length !== 5) return true
  const [min, hr, dom, mon, dow] = parts

  // Every preset pins an exact minute and hour.
  if (!inRange(min, 0, 59) || !inRange(hr, 0, 23)) return true
  // Day of month: any single day, or the last-day token.
  if (dom !== '*' && dom !== 'L' && !inRange(dom, 1, 31)) return true
  // Month: the yearly preset pins one, everything else leaves it open.
  if (mon !== '*' && !inRange(mon, 1, 12)) return true
  // Day of week: a list of plain days at most.
  if (dow !== '*' && !dow.split(',').every((d) => inRange(d, 0, 6))) return true

  // Shapes the presets never build: a specific month without a specific day
  // (the yearly preset always pins both), and a day-of-month combined with a
  // day-of-week (cron ORs them, which no preset means to express).
  if (mon !== '*' && dom === '*') return true
  if (dom !== '*' && dow !== '*') return true

  return false
}

/**
 * Client-side sanity check for a hand-typed expression. It mirrors what
 * `scheduler.ValidateSchedule` will accept closely enough to catch typos while
 * the user is still in the form; the server stays the authority and rejects
 * anything this misses.
 *
 * Returns null when the expression looks fine, or a short message naming the
 * problem.
 */
export function cronError(cron) {
  const spec = (cron || '').trim()
  if (!spec) return 'Enter a cron expression, or pick one of the presets.'
  if (spec.startsWith('@')) {
    return DESCRIPTORS.has(spec.toLowerCase())
      ? null
      : `Unknown descriptor — try ${[...DESCRIPTORS].slice(0, 4).join(', ')}.`
  }

  const parts = spec.split(/\s+/)
  if (parts.length !== 5) {
    return `A cron expression has 5 fields (minute hour day-of-month month day-of-week) — this has ${parts.length}.`
  }

  const bounds = [
    { name: 'minute', min: 0, max: 59 },
    { name: 'hour', min: 0, max: 23 },
    { name: 'day of month', min: 1, max: 31 },
    { name: 'month', min: 1, max: 12 },
    { name: 'day of week', min: 0, max: 7 },
  ]
  for (let i = 0; i < 5; i++) {
    // The day-of-month field also accepts Vault's last-day token.
    if (i === 2 && parts[i] === 'L') continue
    const err = fieldError(parts[i], bounds[i])
    if (err) return err
  }
  return null
}

// One field, which may be a comma-separated list of terms; each term is "*",
// a number, or a range, any of which may carry a "/step" suffix.
function fieldError(field, { name, min, max }) {
  for (const term of field.split(',')) {
    if (term === '') return `Empty value in the ${name} field.`
    const [base, step, ...rest] = term.split('/')
    if (rest.length > 0) return `Too many "/" in the ${name} field.`
    if (step !== undefined) {
      if (!isPlainInt(step)) {
        return `Step value in the ${name} field must be a number.`
      }
      const s = Number(step)
      if (s < 1 || s > max) {
        return `Step in the ${name} field must be between 1 and ${max} — got "${step}".`
      }
    }
    if (base === '*') continue
    const [from, to, ...extra] = base.split('-')
    if (extra.length > 0) return `Too many "-" in the ${name} field.`
    for (const bound of to === undefined ? [from] : [from, to]) {
      if (!inRange(bound, min, max)) {
        return `The ${name} field accepts ${min}-${max} — got "${bound}".`
      }
    }
  }
  return null
}

/**
 * The picker's time choices: every half hour, plus the exact time of the
 * schedule being edited when it falls between them. Without the extra entry a
 * job at 02:15 showed 02:00 in the list, and any edit rewrote the minute to 0
 * (issue #682). Values are "h:m" strings so the list can change without
 * shifting the selection.
 *
 * @param {number} [hour] @param {number} [minute]
 * @returns {{ value: string, hour: number, minute: number }[]}
 */
export function timeSlots(hour, minute) {
  const slots = []
  for (let h = 0; h < 24; h++) {
    for (const m of [0, 30]) slots.push({ value: `${h}:${m}`, hour: h, minute: m })
  }
  const valid = Number.isInteger(hour) && Number.isInteger(minute) && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
  if (valid && minute !== 0 && minute !== 30) {
    slots.push({ value: `${hour}:${minute}`, hour, minute })
    slots.sort((a, b) => a.hour * 60 + a.minute - (b.hour * 60 + b.minute))
  }
  return slots
}

/**
 * Reads the wall-clock fields of an RFC 3339 timestamp as written, without
 * converting to the browser's timezone: a run the server lists as 02:15+02:00
 * happens at 02:15 on the server's clock, wherever the browser is.
 *
 * @param {string} iso
 * @returns {{ year: number, month: number, day: number, hour: number, minute: number } | null}
 */
export function serverWallClock(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(iso || '')
  if (!m) return null
  const [year, month, day, hour, minute] = m.slice(1).map(Number)
  return { year, month, day, hour, minute }
}

/**
 * The date part of a server run, e.g. "Tue, 7 Oct", in the browser's locale.
 * @param {{ year: number, month: number, day: number }} wall
 */
export function formatRunDate(wall) {
  const d = new Date(Date.UTC(wall.year, wall.month - 1, wall.day))
  return d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' })
}
