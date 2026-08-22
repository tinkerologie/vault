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
