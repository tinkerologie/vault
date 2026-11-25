// Colour a number only when its direction is clearly good or bad (#710).
// Green means verified good news (DESIGN.md "Unknown Is Not Green"), so a count
// or trend whose meaning depends on context stays neutral and lets its words
// carry it.

/**
 * Text colour for a run's "done" count in "done/total items".
 * Green only when a successful run finished every item; a cancelled, failed or
 * partial run's count is neutral (its failed count is shown in red beside it).
 * @param {{ status?: string, items_done?: number, items_total?: number, items_failed?: number } | null | undefined} run
 * @returns {string}
 */
export function doneCountClass(run) {
  const done = run?.items_done ?? 0
  const total = run?.items_total ?? 0
  if (run?.status === 'success' && total > 0 && done >= total && !(run.items_failed > 0)) return 'text-success'
  return 'text-text'
}

/**
 * Text colour for a backup size trend. Growth may be new data and shrinkage may
 * be lost data, so neither direction is good or bad on its own: both are
 * neutral, and the arrow and sign say which way it went.
 * @param {'up' | 'down' | 'flat' | string} _direction
 * @returns {string}
 */
export function sizeTrendClass(_direction) {
  return 'text-text-muted'
}
