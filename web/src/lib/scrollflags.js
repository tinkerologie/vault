// scrollflags.js — pure scroll-position predicates for the unified console
// (#328 r5 #5). Extracting the predicates here keeps the boundary behavior
// deterministic and unit-testable.

const EDGE = 48        // px: "at top" / "near bottom" trigger band
const LOAD_OLDER_EDGE = 200  // px: load-older triggers before the exact oldest edge

export function atTop(scrollTop) {
  return scrollTop < EDGE
}

// The console renders newest-first, so the OLDEST entries live at the bottom
// edge and that is where the next older batch has to be fetched. Triggering a
// band before the exact edge means the batch is already streaming in by the
// time the user scrolls that far.
export function nearOldestEdge(scrollTop, scrollHeight, clientHeight) {
  return scrollHeight - scrollTop - clientHeight < LOAD_OLDER_EDGE
}

export function nearBottom(scrollTop, scrollHeight, clientHeight) {
  return scrollHeight - scrollTop - clientHeight < EDGE
}
