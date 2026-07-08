// Customizable Dashboard layout state (issue: Dashboard 5A / E13).
//
// Manages the ordered list of visible tile ids, per-tile width (span) overrides,
// and edit mode, persisting to localStorage under our own key. The 5A layout is
// simply the default order. Unknown / duplicate ids are dropped on load so a
// stale or hand-edited value can never render a broken tile.

const KEY = 'vault:dash:layout:v1'

// Default-on tiles = the fixed "5A" layout, in order.
export const DEFAULT_LAYOUT = [
  'health', 'protected', 'nextrun', 'lastbackup',
  'threetwoone', 'progress', 'activity', 'jobs', 'protection',
]

// Allowed tile widths on the 12-col grid (user resize cycles through these).
export const SPAN_OPTIONS = [3, 4, 6, 12]

/**
 * @param {string[]} validIds - every tile id the catalog knows about; ids not
 *   in this list are ignored when loading persisted state.
 */
export function createDashboardLayout(validIds) {
  const isValid = (id) => validIds.includes(id)

  function load() {
    try {
      const raw = localStorage.getItem(KEY)
      if (!raw) return { order: [...DEFAULT_LAYOUT], spans: {} }
      const parsed = JSON.parse(raw)
      // v1 stored a bare array; v1.1 stores { order, spans }. Accept both.
      const arr = Array.isArray(parsed) ? parsed : parsed?.order
      const rawSpans = Array.isArray(parsed) ? {} : (parsed?.spans || {})
      const order = []
      for (const id of Array.isArray(arr) ? arr : []) {
        if (typeof id === 'string' && isValid(id) && !order.includes(id)) order.push(id)
      }
      const spans = {}
      for (const [id, span] of Object.entries(rawSpans)) {
        if (isValid(id) && SPAN_OPTIONS.includes(span)) spans[id] = span
      }
      return { order: order.length ? order : [...DEFAULT_LAYOUT], spans }
    } catch {
      return { order: [...DEFAULT_LAYOUT], spans: {} }
    }
  }

  const initial = load()
  let order = $state(initial.order)
  let spans = $state(initial.spans)
  let editMode = $state(false)

  function persist() {
    try { localStorage.setItem(KEY, JSON.stringify({ order, spans })) } catch { /* ignore */ }
  }

  return {
    get order() { return order },
    get spans() { return spans },
    get editMode() { return editMode },

    toggleEdit() { editMode = !editMode },

    add(id) {
      if (isValid(id) && !order.includes(id)) {
        order = [...order, id]
        persist()
      }
    },

    remove(id) {
      order = order.filter((x) => x !== id)
      if (spans[id] != null) { const next = { ...spans }; delete next[id]; spans = next }
      persist()
    },

    // Move the tile at index `from` to index `to` (drag & drop).
    move(from, to) {
      if (from === to || from < 0 || to < 0 || from >= order.length || to >= order.length) return
      const next = [...order]
      const [moved] = next.splice(from, 1)
      next.splice(to, 0, moved)
      order = next
      persist()
    },

    // Nudge a tile one slot up (-1) or down (+1) — the keyboard / touch path.
    moveBy(id, dir) {
      const i = order.indexOf(id)
      if (i < 0) return
      const j = i + dir
      if (j < 0 || j >= order.length) return
      const next = [...order]
      ;[next[i], next[j]] = [next[j], next[i]]
      order = next
      persist()
    },

    // Resize a tile by cycling its width one step narrower (-1) or wider (+1)
    // through SPAN_OPTIONS, starting from its current effective span.
    resize(id, currentSpan, dir) {
      const i = SPAN_OPTIONS.indexOf(currentSpan)
      const from = i < 0 ? SPAN_OPTIONS.indexOf(SPAN_OPTIONS.find(s => s >= currentSpan) ?? currentSpan) : i
      const next = Math.min(SPAN_OPTIONS.length - 1, Math.max(0, (from < 0 ? 0 : from) + dir))
      spans = { ...spans, [id]: SPAN_OPTIONS[next] }
      persist()
    },

    reset() {
      order = [...DEFAULT_LAYOUT]
      spans = {}
      persist()
    },
  }
}
