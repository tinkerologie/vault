import { describe, it, expect } from 'vitest'
import { normalizeLevel, levelStyle, levelLabel } from './loglevel.js'

describe('normalizeLevel', () => {
  it('folds the API’s two spellings of warn onto one level', () => {
    expect(normalizeLevel('warning')).toBe('warn')
    expect(normalizeLevel('warn')).toBe('warn')
  })

  it('is case-insensitive', () => {
    expect(normalizeLevel('ERROR')).toBe('error')
    expect(normalizeLevel('Warning')).toBe('warn')
  })

  it('falls back to info rather than leaving a row unstyled', () => {
    for (const junk of ['', null, undefined, 'trace', 'notice']) {
      expect(normalizeLevel(junk)).toBe('info')
    }
  })

  it('passes the known levels through unchanged', () => {
    for (const level of ['error', 'info', 'debug', 'success']) {
      expect(normalizeLevel(level)).toBe(level)
    }
  })
})

describe('levelStyle', () => {
  it('returns a complete style set for every level, including unknown ones', () => {
    for (const level of ['error', 'warn', 'warning', 'info', 'debug', 'success', 'nonsense', null]) {
      const s = levelStyle(level)
      expect(s).toBeDefined()
      for (const key of ['label', 'text', 'meta', 'border', 'row', 'pill']) {
        expect(s[key], `${level}.${key}`).toBeTypeOf('string')
      }
    }
  })

  it('gives each level a distinct left border so rows scan apart', () => {
    const borders = ['error', 'warn', 'info', 'debug', 'success'].map(l => levelStyle(l).border)
    expect(new Set(borders).size).toBe(borders.length)
  })

  it('tints the row only for the levels that demand attention', () => {
    expect(levelStyle('error').row).not.toBe('')
    expect(levelStyle('warn').row).not.toBe('')
    expect(levelStyle('info').row).toBe('')
    expect(levelStyle('debug').row).toBe('')
  })

  it('styles the two warn spellings identically', () => {
    expect(levelStyle('warning')).toEqual(levelStyle('warn'))
  })
})

describe('levelLabel', () => {
  it('always yields a text label, so colour is never the only signal', () => {
    for (const level of ['error', 'warn', 'warning', 'info', 'debug', 'success', '', null]) {
      expect(levelLabel(level)).toBeTruthy()
    }
  })

  it('shows warn for both spellings', () => {
    expect(levelLabel('warning')).toBe('warn')
    expect(levelLabel('warn')).toBe('warn')
  })
})
