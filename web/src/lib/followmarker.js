// followmarker.js — pure logic for the Logs page "new entries" indicator.
//
// When the user scrolls away from the live tail (follow off), a marker
// snapshots the buffer: the newest entry's timestamp and the id set of every
// entry present at that moment. countNewEntries then reports only genuinely
// new entries — any entry not older than the marker's timestamp whose id was
// NOT in the snapshot. The id-set guard applies even to strictly-newer
// timestamps: a WS run_log can replace an already-seen row (same id) with a
// newer timestamp, and counting that by timestamp alone produced the phantom
// "1 new entry" badge (#328).
//
// The old implementation tie-broke on a single id (`t === mt && e.id !==
// marker.id`). Timestamps are often second-precision, so a same-second burst
// at the buffer tail made pre-existing entries look "new" whenever the
// marker's identity changed between scroll-ups (WS/dedupe tail replacements,
// or a marker landing on a different same-second entry) — the #328 off-by-one
// ("1 new entry" with nothing new). Snapshotting the id set fixes it.
//
// Plain object used as a set (key = id) to match the store's lint-friendly
// convention for .svelte.js files.

export function createFollowMarker(entries) {
  if (entries.length === 0) return null
  const ids = {}
  for (const e of entries) ids[e.id] = true
  return { ts: entries[entries.length - 1].ts, ids }
}

export function countNewEntries(entries, marker) {
  if (!marker) return 0
  const mt = Date.parse(marker.ts) || 0
  let n = 0
  for (const e of entries) {
    const t = Date.parse(e.ts) || 0
    // Count only ids not already snapshotted and not older than the marker.
    // Guarding ids on BOTH branches (not just the same-second tie-break) is
    // what stops an already-seen row whose ts advanced past the marker from
    // reading as "new" (#328).
    if (!marker.ids[e.id] && t >= mt) n++
  }
  return n
}
