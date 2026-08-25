// tsformat.js — pure timestamp formatting for the unified log console
// (#328 r4 #6). Produces a compact, fixed-width "YYYY-MM-DD HH:MM:SS" string
// (24h) so every line carries a date, not just a time. Deterministic
// (manual padding), so it is unit-testable without a locale/timezone
// dependency.

// Full "YYYY-MM-DD HH:MM:SS" form, composed from the day key and the row time
// so there is exactly one place that pads a date and one that pads a clock.
// Used for the row tooltip and for exported/copied lines, where the date has
// no separator above it to supply the context.
export function lineTimestamp(ts) {
  const day = dayKey(ts)
  if (!day) return '--:--:--'
  return `${day} ${rowTime(ts)}`
}

// Time-only form for a console row. The console groups rows under a date
// separator, so repeating the date on all 2,000 rows is pure noise — it cost
// ~11 characters of every line and pushed the message off the right edge.
// The full date stays available as the row's title attribute.
export function rowTime(ts) {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return '--:--:--'
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  const ss = String(d.getSeconds()).padStart(2, '0')
  return `${hh}:${mm}:${ss}`
}

// Stable local-calendar-day key, used to decide where a date separator goes.
// Local rather than UTC: a separator must change when the user's day changes,
// not when UTC's does.
export function dayKey(ts) {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ''
  const yyyy = d.getFullYear()
  const mo = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${yyyy}-${mo}-${dd}`
}

// Human label for a date separator. `now` is injectable so the relative
// labels are testable without mocking the clock.
export function dayLabel(ts, now = new Date()) {
  const key = dayKey(ts)
  if (!key) return ''
  const today = dayKey(now)
  if (key === today) return 'Today'
  const yest = new Date(now)
  yest.setDate(yest.getDate() - 1)
  if (key === dayKey(yest)) return 'Yesterday'
  return key
}
