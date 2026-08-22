// logsearch.js — pure search filter for the unified log console (#328 r4 #9).
// Case-insensitive substring match across the fields a user would expect to
// search: message, level, category, job name, and the meta fields rendered
// on the row (type=…, destination=…, items=…, schedule=…). An
// empty/whitespace query matches everything.

export function matchesSearch(entry, query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) return true
  // The meta (parsed details JSON) is part of what the console displays —
  // e.g. `| type=Differential, destination=hdd, items=15` on activity rows —
  // so a term visible on the line must match even when the message text
  // itself does not mention it ("diff" must find "Backup started … |
  // type=Differential") (#328).
  const metaText = entry.meta ? JSON.stringify(entry.meta) : ''
  const haystack = [entry.message, entry.level, entry.category, entry.jobName, metaText]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
  return haystack.includes(q)
}
