// The schedule picker must never change a schedule the user didn't touch
// (#682): manual-only stays empty, an off-grid minute survives edits, two
// builders on one page get their own ids, and the preview lists the next runs
// on the server's clock.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  previewSchedule: vi.fn(async () => ({ runs: ['2026-10-07T02:15:00+02:00', '2026-10-08T02:15:00+02:00'], timezone: 'CEST', utc_offset: '+02:00' })),
}))
vi.mock('../lib/api.js', () => ({ api }))

import ScheduleBuilder from './ScheduleBuilder.svelte'

afterEach(() => {
  vi.useRealTimers()
  api.previewSchedule.mockClear()
})

function pressed(name) {
  return screen.getByRole('button', { name }).getAttribute('aria-pressed')
}

describe('ScheduleBuilder manual only', () => {
  it('shows an empty schedule as Manual only and leaves it empty', async () => {
    const onchange = vi.fn()
    render(ScheduleBuilder, { props: { value: '', allowManual: true, onchange } })
    expect(pressed('Manual only')).toBe('true')
    expect(screen.getByText('Runs only when started with Run Now')).toBeTruthy()
    expect(onchange).not.toHaveBeenCalled()
  })

  it('switches between manual and a preset', async () => {
    const onchange = vi.fn()
    render(ScheduleBuilder, { props: { value: '0 2 * * *', allowManual: true, onchange } })
    await fireEvent.click(screen.getByRole('button', { name: 'Manual only' }))
    expect(onchange).toHaveBeenLastCalledWith('')
    await fireEvent.click(screen.getByRole('button', { name: 'Daily' }))
    expect(onchange).toHaveBeenLastCalledWith('0 2 * * *')
  })

  it('is not offered unless the host allows it', () => {
    render(ScheduleBuilder, { props: { value: '0 4 * * 0' } })
    expect(screen.queryByRole('button', { name: 'Manual only' })).toBeNull()
  })
})

describe('ScheduleBuilder off-grid minutes', () => {
  it('shows the exact time and keeps the minute when another control changes', async () => {
    const onchange = vi.fn()
    render(ScheduleBuilder, { props: { value: '15 2 * * 1,2,3,4,5', onchange } })
    const select = screen.getByLabelText('Time')
    expect(select.value).toBe('2:15')
    expect(select.selectedOptions[0].textContent).toMatch(/2:15/)
    await fireEvent.click(screen.getByRole('button', { name: 'Saturday' }))
    expect(onchange).toHaveBeenLastCalledWith('15 2 * * 1,2,3,4,5,6')
    await fireEvent.click(screen.getByRole('button', { name: 'Weekly' }))
    expect(onchange).toHaveBeenLastCalledWith('15 2 * * 0')
  })

  it('writes a picked half-hour slot', async () => {
    const onchange = vi.fn()
    render(ScheduleBuilder, { props: { value: '0 2 * * *', onchange } })
    await fireEvent.change(screen.getByLabelText('Time'), { target: { value: '23:30' } })
    expect(onchange).toHaveBeenLastCalledWith('30 23 * * *')
  })
})

describe('ScheduleBuilder state and ids', () => {
  it('exposes the chosen frequency and days as pressed', async () => {
    render(ScheduleBuilder, { props: { value: '0 2 * * 1,3' } })
    expect(pressed('Daily')).toBe('true')
    expect(pressed('Weekly')).toBe('false')
    expect(pressed('Monday')).toBe('true')
    expect(pressed('Tuesday')).toBe('false')
    await fireEvent.click(screen.getByRole('button', { name: 'Tuesday' }))
    expect(pressed('Tuesday')).toBe('true')
  })

  it('keeps at least one day instead of silently meaning every day', async () => {
    const onchange = vi.fn()
    render(ScheduleBuilder, { props: { value: '0 2 * * 1,3', onchange } })
    await fireEvent.click(screen.getByRole('button', { name: 'Monday' }))
    expect(onchange).toHaveBeenLastCalledWith('0 2 * * 3')
    await fireEvent.click(screen.getByRole('button', { name: 'Wednesday' }))
    expect(pressed('Wednesday')).toBe('true')
    expect(onchange).toHaveBeenLastCalledWith('0 2 * * 3')
    expect(screen.getByText(/Keep at least one day/)).toBeTruthy()
  })

  it('gives two instances distinct ids', () => {
    render(ScheduleBuilder, { props: { value: '0 2 * * 0' } })
    render(ScheduleBuilder, { props: { value: '0 4 * * 0' } })
    const ids = [...document.querySelectorAll('[id]')].map((el) => el.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const label of document.querySelectorAll('label[for]')) {
      expect(document.getElementById(label.getAttribute('for'))).toBeTruthy()
    }
  })
})

describe('ScheduleBuilder upcoming runs', () => {
  it('lists the server runs with the server timezone', async () => {
    render(ScheduleBuilder, { props: { value: '15 2 * * *' } })
    const list = await screen.findByTestId('schedule-next-runs', {}, { timeout: 2000 })
    expect(api.previewSchedule).toHaveBeenCalledWith('15 2 * * *', 3)
    expect(list.textContent).toMatch(/server time, CEST \+02:00/)
    expect(list.querySelectorAll('li')).toHaveLength(2)
    expect(list.textContent).toMatch(/2:15/)
  })

  it('asks for nothing when the job is manual only', async () => {
    vi.useFakeTimers()
    render(ScheduleBuilder, { props: { value: '', allowManual: true } })
    await vi.advanceTimersByTimeAsync(500)
    expect(api.previewSchedule).not.toHaveBeenCalled()
  })

  it('says so when the preview cannot be loaded', async () => {
    api.previewSchedule.mockRejectedValueOnce(new Error('offline'))
    render(ScheduleBuilder, { props: { value: '0 2 * * *' } })
    expect(await screen.findByText(/Upcoming run times are unavailable/, {}, { timeout: 2000 })).toBeTruthy()
  })
})
