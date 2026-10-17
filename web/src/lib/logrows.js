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

// Render window (#507). The console used to put every loaded row on the page;
// with the full history loaded that was ~8,600 rows and ~78,000 DOM nodes,
// and leaving the Logs page (or anything else that walks the document, such
// as Chrome's accessibility tree) stalled the UI for seconds. Only the newest
// part of `rows` is rendered; the window grows as the user scrolls towards
// older lines.
//
// The window's bottom edge is an anchor `{ key, count }`: the key of the
// oldest rendered row, plus how many rows were rendered when it was set. New
// lines arrive at the TOP, so anchoring on a key means they never push rows
// out from under a reader. The count is the fallback when the key vanishes
// (a collapse toggle re-keys rows) so the reader keeps roughly their depth.
// A null anchor is the default window of `base` rows.
export const RENDER_WINDOW_BASE = 200
export const RENDER_WINDOW_STEP = 200

// How many of `rows` to render for `anchor`. Never fewer than `base` (or all
// rows, when there are fewer).
export function renderedCount(rows, anchor, base = RENDER_WINDOW_BASE) {
  const floor = Math.min(rows.length, base)
  if (!anchor) return floor
  const idx = rows.findIndex((r) => r.key === anchor.key)
  const end = idx >= 0 ? idx + 1 : Math.min(rows.length, anchor.count)
  return Math.max(floor, end)
}

// The anchor for a window `step` rows deeper than `count`, or null when there
// are no rows. Extending past the end clamps to the last row.
export function extendWindow(rows, count, step = RENDER_WINDOW_STEP) {
  if (rows.length === 0) return null
  const end = Math.min(rows.length, Math.max(count, 0) + step)
  return { key: rows[end - 1].key, count: end }
}

// The window's TOP edge, for "Jump to oldest" (#507): rendering everything
// down to the oldest line would put the whole history back on the page, so
// the jump renders only the oldest `base` rows and the reader scrolls back up
// through newer ones. `top` is `{ key, index }` for the newest rendered row,
// or null when the window starts at the newest row (the live edge). Like the
// bottom anchor, it is keyed so lines arriving at the top do not move it.
export function renderedStart(rows, top) {
  if (!top) return 0
  const idx = rows.findIndex((r) => r.key === top.key)
  if (idx >= 0) return idx
  // The key is gone (a collapse toggle re-keys rows): keep roughly the same
  // depth, but never past the start of the oldest base window, or the
  // window would be empty.
  return Math.max(0, Math.min(top.index, rows.length - RENDER_WINDOW_BASE))
}

// The top anchor `step` rows newer than `start`; null once that reaches the
// newest row, i.e. the window is back at the live edge.
export function raiseWindow(rows, start, step = RENDER_WINDOW_STEP) {
  const next = Math.max(0, start - step)
  return next === 0 || rows.length === 0 ? null : { key: rows[next].key, index: next }
}

// The window for "Jump to oldest": the oldest `base` rows, as `{ top, bottom }`
// anchors (top null when that already covers every row).
export function oldestWindow(rows, base = RENDER_WINDOW_BASE) {
  if (rows.length === 0) return { top: null, bottom: null }
  const start = Math.max(0, rows.length - base)
  return {
    top: start === 0 ? null : { key: rows[start].key, index: start },
    bottom: { key: rows[rows.length - 1].key, count: rows.length },
  }
}

// The most rows the window holds while the reader scrolls through history
// (#507): growing at one edge drops rows past this from the other, so a long
// traversal never rebuilds the whole history on the page.
export const RENDER_WINDOW_MAX = 600

// After growing DOWN to `count`: the top anchor that keeps at most `max` rows,
// or `top` unchanged when the window is within bounds.
export function trimTop(rows, top, start, count, max = RENDER_WINDOW_MAX) {
  if (count - start <= max) return top
  const next = count - max
  return { key: rows[next].key, index: next }
}

// After growing UP to `start`: the bottom anchor that keeps at most `max`
// rows, or `bottom` unchanged when the window is within bounds.
export function trimBottom(rows, bottom, start, count, max = RENDER_WINDOW_MAX) {
  if (count - start <= max) return bottom
  const end = start + max
  return { key: rows[end - 1].key, count: end }
}
