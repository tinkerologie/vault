// logline.js — uniform `message | metadata` rendering for the Logs console.
// Every log line — activity, summary, and run-log rows alike — renders as
//
//   <human-readable message> | <key=value metadata>
//
// The runner emits every line as `phrase, key=value, key=value…` (message
// convention #328), so the metadata is the key=value tail of the message
// itself: the split point is the first `, key=` boundary. Lines without a
// kv tail render bare. For activity rows, structured details fields whose
// key is not already in the message tail are appended (type, destination,
// schedule), so nothing that used to be visible is lost and nothing
// renders twice.

import { lineTimestamp } from './tsformat.js'
import { normalizeLevel } from './loglevel.js'

// Splits a log message at the first `, key=` boundary into its
// human-readable part and its key=value metadata tail.
export function splitMessageMeta(message) {
  if (!message) return { message: '', meta: '' }
  const m = /,\s*[A-Za-z_][A-Za-z0-9_]*=/.exec(message)
  if (!m) return { message, meta: '' }
  return {
    message: message.slice(0, m.index),
    meta: message.slice(m.index + 1).replace(/^\s+/, ''),
  }
}

// Keys present in a rendered meta tail, so structured extras can be
// deduped against what the message itself already carries.
function metaKeys(meta) {
  const keys = {}
  for (const m of meta.matchAll(/(?:^|,\s*)([A-Za-z_][A-Za-z0-9_]*)=/g)) keys[m[1]] = true
  return keys
}

function capitalize(s) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : ''
}

// Structured details fields rendered as metadata for activity rows.
// [source key in entry.meta, display key]; job falls back to entry.jobName.
const DETAIL_FIELDS = [
  ['job', 'job'],
  ['backup_type', 'type'],
  ['destination', 'destination'],
  ['items', 'items'],
  ['schedule', 'schedule'],
]

// Returns { message, meta } for a unified log entry: the split message and
// its metadata tail, with activity-only structured extras appended.
export function formatLine(entry) {
  const { message, meta } = splitMessageMeta(entry.message)
  if (entry.type !== 'activity' || !entry.meta) return { message, meta }
  const present = metaKeys(meta)
  const extras = []
  for (const [srcKey, displayKey] of DETAIL_FIELDS) {
    if (present[displayKey]) continue
    const val = srcKey === 'job' ? (entry.meta.job || entry.jobName) : entry.meta[srcKey]
    if (val == null || val === '') continue
    if (displayKey === 'schedule') extras.push(`schedule="${val}"`)
    else extras.push(`${displayKey}=${displayKey === 'type' ? capitalize(val) : val}`)
  }
  const combined = extras.length > 0 ? [meta, extras.join(', ')].filter(Boolean).join(', ') : meta
  return { message, meta: combined }
}

// Single-line log text shared by copy, copy-all, and export:
// `YYYY-MM-DD HH:MM:SS  level  category  message | metadata`.
// Matches the console's visual layout; the raw JSON meta blob is dropped.
export function logLine(entry) {
  const { message, meta } = formatLine(entry)
  const level = entry.level === 'warning' ? 'warn' : (entry.level || 'info')
  return `${lineTimestamp(entry.ts)}  ${level}  ${entry.category}  ${message}${meta ? ` | ${meta}` : ''}`
}

// Reads the `key=value` pairs of a message tail (splitMessageMeta's `meta`) for
// display. Producers format values with %s or %q, so a value is either quoted
// or runs to the next `, key=`. An unquoted `error` keeps the rest of the tail:
// error text routinely holds commas and things that look like `key=`.
export function parseInlineMeta(meta) {
  const fields = {}
  let rest = meta || ''
  while (rest) {
    const head = /^([A-Za-z_][A-Za-z0-9_]*)=/.exec(rest)
    if (!head) break
    const key = head[1]
    rest = rest.slice(head[0].length)
    let value
    const quoted = /^"((?:[^"\\]|\\.)*)"(?=$|,\s*)/.exec(rest)
    if (quoted && (key !== 'error' || quoted[0].length === rest.length)) {
      value = quoted[1].replace(/\\(["\\])/g, '$1')
      rest = rest.slice(quoted[0].length)
    } else if (key === 'error') {
      value = rest
      rest = ''
    } else {
      const next = /,\s*[A-Za-z_][A-Za-z0-9_]*=/.exec(rest)
      value = next ? rest.slice(0, next.index) : rest
      rest = next ? rest.slice(next.index) : ''
    }
    if (value !== '' && !(key in fields)) fields[key] = value
    rest = rest.replace(/^,\s*/, '')
  }
  return fields
}

function isProblem(entry) {
  const level = normalizeLevel(entry.level)
  return level === 'error' || level === 'warn'
}

const scalar = (v) => ((typeof v === 'string' && v !== '') || typeof v === 'number' ? String(v) : '')

// What a warning or error is about. Item and status prefer the structured
// details, which cannot be mis-split. Error text comes ONLY from the message
// tail: the structured `error` and `message` fields are free text that Details
// never shows (a post-script's failure, say), and this must not surface
// anything that Details and the exported line do not already show.
export function problemFields(entry) {
  const inline = parseInlineMeta(splitMessageMeta(entry.message).meta)
  const meta = entry.meta && typeof entry.meta === 'object' ? entry.meta : {}
  return {
    item: scalar(meta.container_name) || inline.container || inline.destination || inline.job || scalar(meta.storage_dest_id),
    status: scalar(meta.status) || inline.status || '',
    error: inline.error || '',
  }
}

// The row text for a warning or error with Details off, such as
// "Health check · plex · unhealthy · Timed out"; null for every other level.
// A problem with no recognised fields keeps its whole tail, because that tail
// is the only place the cause can be. Display only: copy and export use logLine.
export function problemSummary(entry) {
  if (!isProblem(entry)) return null
  const { message, meta } = formatLine(entry)
  const { item, status, error } = problemFields(entry)
  if (item || status) return [message, item, status, error].filter(Boolean).join(' · ')
  return meta ? `${message} · ${meta}` : message
}

// The part of a problem row's identity that folding must respect: different
// containers, statuses or causes are different problems, and folding them would
// hide all but one cause. It mirrors problemSummary: with no item or status the
// whole tail is what the row shows. Empty for other levels.
export function problemFoldKey(entry) {
  if (!isProblem(entry)) return ''
  const { item, status, error } = problemFields(entry)
  if (item || status) return `${item}\u0000${status}\u0000${error}`
  return formatLine(entry).meta
}

// A row's expand button. Rows often share a message, so the item and time tell
// them apart for anyone who can't see the row around the button.
export function rowDetailsLabel(entry, time, repeat = 1) {
  const { message } = formatLine(entry)
  const { item } = problemFields(entry)
  const parts = [item ? `${message} · ${item}` : message, time]
  if (repeat > 1) parts.push(`${repeat} entries`)
  return `Details for ${parts.filter(Boolean).join(', ')}`
}
