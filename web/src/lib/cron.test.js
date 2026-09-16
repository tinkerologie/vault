import { describe, it, expect } from 'vitest'
import { isCustomCron, cronError } from './cron.js'

describe('isCustomCron', () => {
  it('treats an empty schedule as manual, not custom', () => {
    expect(isCustomCron('')).toBe(false)
    expect(isCustomCron(null)).toBe(false)
    expect(isCustomCron(undefined)).toBe(false)
  })

  it('recognises every shape the presets build', () => {
    const preset = [
      '0 2 * * *', // daily, every day
      '30 23 * * 1,2,3,4,5', // daily, weekdays
      '0 2 * * 0', // weekly
      '0 2 1 * *', // monthly on the 1st
      '0 2 L * *', // monthly on the last day
      '0 2 15 6 *', // yearly
      '0 2 L 12 *', // yearly on the last day of December
    ]
    for (const cron of preset) {
      expect(isCustomCron(cron), cron).toBe(false)
    }
  })

  it('recognises expressions no preset can represent', () => {
    const custom = [
      '0 */3 */2 * *', // the expression from issue #309
      '*/15 * * * *',
      '0 2 * * 1-5', // a range, not a list
      '0 2-4 * * *',
      '0 2 1,15 * *', // two days of the month
      '0 2 * 6 *', // a month without a day
      '0 2 1 * 1', // day-of-month AND day-of-week
      '@daily',
      '@hourly',
      '0 2 * *', // four fields
      '0 2 * * * *', // six fields
    ]
    for (const cron of custom) {
      expect(isCustomCron(cron), cron).toBe(true)
    }
  })

  it('ignores surrounding whitespace', () => {
    expect(isCustomCron('  0 2 * * *  ')).toBe(false)
    expect(isCustomCron('  */5 * * * *  ')).toBe(true)
  })
})

describe('cronError', () => {
  it('accepts valid expressions', () => {
    const valid = ['0 2 * * *', '*/15 * * * *', '0 */3 */2 * *', '0 2 L * *', '0 2 * * 1-5', '0 0,12 1,15 * *', '@daily', '@MIDNIGHT']
    for (const cron of valid) {
      expect(cronError(cron), cron).toBeNull()
    }
  })

  it('asks for an expression when the box is empty', () => {
    expect(cronError('')).toMatch(/Enter a cron expression/)
    expect(cronError('   ')).toMatch(/Enter a cron expression/)
  })

  it('reports the field count', () => {
    expect(cronError('0 2 * *')).toMatch(/has 4/)
    expect(cronError('0 2 * * * *')).toMatch(/has 6/)
  })

  it('reports out-of-range and malformed fields by name', () => {
    expect(cronError('99 2 * * *')).toMatch(/minute field accepts 0-59/)
    expect(cronError('0 99 * * *')).toMatch(/hour field accepts 0-23/)
    expect(cronError('0 2 40 * *')).toMatch(/day of month field accepts 1-31/)
    expect(cronError('0 2 * 13 *')).toMatch(/month field accepts 1-12/)
    expect(cronError('0 2 * * 9')).toMatch(/day of week field accepts 0-7/)
    expect(cronError('not a cron at all')).toMatch(/minute field/)
    expect(cronError('0 2 1,, * *')).toMatch(/Empty value in the day of month/)
    expect(cronError('0 2 1/2/3 * *')).toMatch(/Too many "\/"/)
    expect(cronError('0 2 1-2-3 * *')).toMatch(/Too many "-"/)
    expect(cronError('*/x * * * *')).toMatch(/Step value in the minute field must be a number/)
    expect(cronError('*/0 * * * *')).toMatch(/Step in the minute field must be between 1 and 59/)
    expect(cronError('*/99 * * * *')).toMatch(/Step in the minute field must be between 1 and 59/)
  })

  it('rejects an unknown descriptor', () => {
    expect(cronError('@bogus')).toMatch(/Unknown descriptor/)
  })

  it('accepts the Vault last-day token only in the day-of-month field', () => {
    expect(cronError('0 2 L * *')).toBeNull()
    expect(cronError('0 2 * L *')).toMatch(/month field/)
  })
})
