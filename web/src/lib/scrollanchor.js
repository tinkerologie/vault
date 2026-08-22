// scrollanchor.js — pure arithmetic for scroll anchoring (#328 r3 #6, #8).
//
// When content is prepended (load-older) or reflowed (word-wrap toggle)
// above the viewport, keeping the same element pinned at the same visual
// position requires shifting scrollTop by the element's change in offset
// from the container's top edge. The Logs page measures the offsets with
// getBoundingClientRect and passes them here; this module holds only the
// math so it is unit-testable without a DOM.

export function anchoredScrollTop(prevScrollTop, elTopBefore, elTopAfter) {
  return prevScrollTop + (elTopAfter - elTopBefore)
}
