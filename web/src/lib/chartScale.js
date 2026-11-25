// Geometry shared by the History trend charts (#690). The plot is drawn in a
// 100×100 SVG box stretched to its container, so every coordinate here is a
// percentage, and the axis labels are HTML positioned with the same numbers.
// That keeps labels at their real CSS size at any width, instead of SVG text
// that shrank to about 3px on a phone.

/** Tailwind height of the plot area; the y-axis column uses the same. */
export const CHART_HEIGHT_CLASS = 'h-40'

/**
 * Round y-axis ticks: the first step that splits the range into at most five
 * intervals, with the top rounded up to a whole step.
 * @param {number} max
 * @param {number[]} steps Ascending candidate steps.
 */
export function niceTicks(max, steps) {
  let step = steps[steps.length - 1]
  for (const candidate of steps) {
    if (max / candidate <= 5) { step = candidate; break }
  }
  const ceil = Math.ceil(max / step) * step
  const ticks = []
  for (let value = 0; value <= ceil + 1e-9; value += step) ticks.push(value)
  return { step, ceil, ticks }
}

/** Where bar `index` of `count` sits, in percent of the plot width. */
export function barSlot(index, count) {
  if (count <= 0) return { x: 0, width: 0, center: 50 }
  const slot = 100 / count
  const width = slot * 0.7
  const center = slot * (index + 0.5)
  return { x: center - width / 2, width, center }
}

/** The buckets that get an x-axis date: first, middle and last. */
export function xLabelIndexes(count) {
  if (count <= 0) return []
  if (count === 1) return [0]
  const indexes = [0]
  if (count > 2) indexes.push(Math.floor((count - 1) / 2))
  indexes.push(count - 1)
  return indexes
}
