// History trend charts (#690): readable HTML axis labels, keyboard-reachable
// bars, a data table for screen readers, and a message instead of nothing.
import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/svelte'
import SizeChart from './SizeChart.svelte'
import DurationChart from './DurationChart.svelte'

const GB = 1024 ** 3
const sizeBuckets = ['2026-10-01', '2026-10-02', '2026-10-03'].map((day, i) => ({
  start: `${day}T12:00:00Z`,
  total_bytes: (i + 2) * GB,
  categories: { containers: GB, vms: (i + 1) * GB },
}))
const durationBuckets = ['2026-10-01', '2026-10-02'].map((day, i) => ({
  start: `${day}T12:00:00Z`, avg_duration_seconds: 600 * (i + 1), run_count: i + 1,
}))

describe('SizeChart', () => {
  it('lets Tab reach each bar and shows its breakdown on focus', async () => {
    const { container } = render(SizeChart, { buckets: sizeBuckets })
    const bars = screen.getAllByRole('img')
    expect(bars).toHaveLength(3)
    for (const bar of bars) expect(bar.getAttribute('tabindex')).toBe('0')
    expect(bars[0].getAttribute('aria-label')).toMatch(/2 GB total \(Containers 1 GB, VMs 1 GB\)/)

    expect(container.querySelector('[data-chart-tooltip]')).toBeNull()
    await fireEvent.focus(bars[1])
    const tooltip = /** @type {HTMLElement} */ (container.querySelector('[data-chart-tooltip]'))
    expect(tooltip.textContent).toContain('3 GB total')
    await fireEvent.blur(bars[1])
    expect(container.querySelector('[data-chart-tooltip]')).toBeNull()
  })

  it('exposes a table with date, total and category columns', () => {
    render(SizeChart, { buckets: sizeBuckets })
    const table = screen.getByRole('table', { name: 'Backup size by date' })
    const headers = within(table).getAllByRole('columnheader').map((th) => th.textContent)
    expect(headers).toEqual(['Date', 'Total', 'Containers', 'VMs'])
    expect(within(table).getAllByRole('row')).toHaveLength(4)
  })

  it('draws axis labels as HTML text, not scaled SVG text', () => {
    const { container } = render(SizeChart, { buckets: sizeBuckets })
    expect(container.querySelector('svg text')).toBeNull()
    expect(container.querySelectorAll('[data-axis-label]').length).toBeGreaterThan(3)
  })

  it('does not hide the bars behind aria-hidden', () => {
    const { container } = render(SizeChart, { buckets: sizeBuckets })
    const svg = /** @type {Element} */ (container.querySelector('svg[role="group"]'))
    expect(svg.getAttribute('aria-hidden')).toBeNull()
    expect(svg.closest('[role="img"]')).toBeNull()
  })

  it('says when the period has too little data', () => {
    render(SizeChart, { buckets: sizeBuckets.slice(0, 1) })
    expect(screen.getByText(/Not enough data for this period/)).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
  })
})

describe('DurationChart', () => {
  it('keeps focusable bars and its table', async () => {
    const { container } = render(DurationChart, { buckets: durationBuckets })
    const bars = screen.getAllByRole('img')
    expect(bars).toHaveLength(2)
    await fireEvent.focus(bars[1])
    expect(container.querySelector('[data-chart-tooltip]')?.textContent).toContain('2 runs')
    expect(screen.getByRole('table', { name: 'Job duration trend by date' })).toBeTruthy()
    expect(container.querySelector('svg text')).toBeNull()
  })

  it('says when the period has too little data', () => {
    render(DurationChart, { buckets: [] })
    expect(screen.getByText(/Not enough data for this period/)).toBeTruthy()
  })
})
