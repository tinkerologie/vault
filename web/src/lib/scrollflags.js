// scrollflags.js — pure scroll-position predicates for the unified console
// (#328 r5 #5). Extracting the predicates here keeps the boundary behavior
// deterministic and unit-testable.

const EDGE = 48        // px: "at top" / "near bottom" trigger band
const LOAD_OLDER_EDGE = 200  // px: load-older triggers before the exact top

export function atTop(scrollTop) {
  return scrollTop < EDGE
}

export function nearTop(scrollTop) {
  return scrollTop < LOAD_OLDER_EDGE
}

export function nearBottom(scrollTop, scrollHeight, clientHeight) {
  return scrollHeight - scrollTop - clientHeight < EDGE
}
