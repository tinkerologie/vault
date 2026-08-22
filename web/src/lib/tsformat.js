// tsformat.js — pure timestamp formatting for the unified log console
// (#328 r4 #6). Produces a compact, fixed-width "YYYY-MM-DD HH:MM:SS" string
// (24h) so every line carries a date, not just a time. Deterministic
// (manual padding), so it is unit-testable without a locale/timezone
// dependency.

export function lineTimestamp(ts) {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return '--:--:--'
  const yyyy = d.getFullYear()
  const mo = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  const ss = String(d.getSeconds()).padStart(2, '0')
  return `${yyyy}-${mo}-${dd} ${hh}:${mm}:${ss}`
}
