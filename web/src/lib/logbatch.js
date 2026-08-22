// planBatch decides how many RUN-LOG lines one load step may insert,
// bounding the total to `budget` even when a single terminal activity
// entry's run-log expansion alone exceeds it (#328 r4 #5).
//
// sizes: RUN-LOG line count contributed by each activity entry (0 for a
//        plain activity row, or a terminal whose expansion produced no
//        lines), newest-first — the API returns the page newest-first, and
//        continuity with the loaded scrollback starts at the page's newest
//        entry.
// budget: max RUN-LOG lines per step. Plain activity rows are never
//         budgeted: they cost one line each and always materialize, so an
//         oversized terminal expansion can't defer the rest of the page
//         out of the first paint (#328).
//
// Returns { fullCount, split }:
//   - fullCount = number of leading activity entries to consume IN FULL.
//   - split = number of RUN-LOG lines to consume from the NEXT entry (the
//     one whose expansion would exceed the remaining budget), or 0 when no
//     entry needs splitting.
//
// split only ever applies to terminal entries (sizes > 0); a plain activity
// entry (size 0) never exceeds the budget and is always consumed. Unlike
// the old selectBatchCount, this NEVER lets a single entry's expansion blow
// past the budget — an oversized terminal entry is split so its run-log
// lines are inserted across multiple bounded steps.
export function planBatch(sizes, budget) {
  let total = 0
  let fullCount = 0
  for (let i = 0; i < sizes.length; i++) {
    const size = sizes[i]
    if (total + size > budget) {
      const remaining = budget - total
      // remaining <= 0 means the budget is exactly filled: stop, no split.
      return { fullCount, split: remaining > 0 ? remaining : 0 }
    }
    total += size
    fullCount++
  }
  return { fullCount, split: 0 }
}
