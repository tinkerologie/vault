// logrows.js — turns the store's chronological entry buffer into the rows the
// console actually renders.
//
// Three jobs, all pure so they can be tested without a DOM:
//
//   1. Reverse to newest-first. The store keeps entries oldest→newest because
//      its cursor paging and prune logic depend on that order; only the
//      display is inverted.
//   2. Insert a date separator wherever the calendar day changes, so each row
//      can drop the date and show only a time.
//   3. Collapse runs of consecutive identical entries into one row with a
//      repeat count. A health sweep that logs the same line forty times
//      becomes one row that says so.
//
// Collapsing is deliberately conservative: same level, same category, and the
// exact same message text. Entries that merely look similar ("Health check:
// plex" vs "Health check: sonarr") stay separate rows, because the differing
// part is the part worth reading.

import { dayKey, dayLabel } from './tsformat.js'
import { splitMessageMeta } from './logline.js'

// Rows fold on what the user actually SEES: level, category, and the
// human-readable part of the message. The `key=value` tail is deliberately
// excluded — eight "Uploaded recyclarr, file=…" lines differ only in the tail,
// read as one repeated line, and are exactly the noise collapsing exists to
// absorb. Nothing is lost: the folded entries stay on the row, so copy and
// export still yield every line verbatim. The caller turns collapsing off when
// the metadata tail is on screen, since that is the part being folded over.
function collapseKey(entry) {
  const { message } = splitMessageMeta(entry.message)
  return `${entry.level || 'info'} ${entry.category || ''} ${message}`
}

// Build the display rows.
//
// entries: chronological (oldest→newest), as the store holds them.
// collapse: when false, every entry gets its own row.
// now: injectable clock for the Today/Yesterday separator labels.
//
// Returns rows of two kinds:
//   { kind: 'date',  key, label, count }
//   { kind: 'entry', key, entry, repeat, entries }
// where `entry` is the newest of a collapsed group and `repeat` is how many
// entries it stands for (1 when nothing was collapsed).
export function buildRows(entries, { collapse = true, now = new Date() } = {}) {
  const rows = []
  if (!entries || entries.length === 0) return rows

  let currentDay = null
  let dateRow = null
  // Collapse key of the row at the tail of `rows`, carried forward so each
  // entry's message is split exactly once instead of re-splitting the previous
  // row's on every comparison.
  let prevKey = null

  // Walk newest-first without copying the array.
  for (let i = entries.length - 1; i >= 0; i--) {
    const entry = entries[i]
    const day = dayKey(entry.ts)
    if (day !== currentDay) {
      currentDay = day
      dateRow = { kind: 'date', key: `day-${day}`, label: dayLabel(entry.ts, now), count: 0 }
      rows.push(dateRow)
    }

    const key = collapseKey(entry)
    const prev = rows.length > 0 ? rows[rows.length - 1] : null
    if (collapse && prev && prev.kind === 'entry' && prevKey === key) {
      // Same line again. The row's representative stays the newest entry —
      // it is the one whose timestamp the user sees — and the rest are kept
      // so copy and export still yield every line.
      prev.repeat++
      prev.entries.push(entry)
      if (dateRow) dateRow.count++
      continue
    }

    rows.push({ kind: 'entry', key: String(entry.id), entry, repeat: 1, entries: [entry] })
    prevKey = key
    if (dateRow) dateRow.count++
  }

  return rows
}

// Every entry a row stands for, newest-first — what copying that row yields.
export function rowEntries(row) {
  return row.kind === 'entry' ? row.entries : []
}
