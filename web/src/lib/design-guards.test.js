import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

// Source guards for the design system (#698, #692). Each scans every .svelte
// file, so a new component can't quietly bring back what the sweep removed.
// The Token-Only rule (raw palette classes) lives in palette-contrast.test.js.

const SRC = new URL('..', import.meta.url).pathname

function svelteFiles(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return svelteFiles(path)
    return name.endsWith('.svelte') ? [path] : []
  })
}

const FILES = svelteFiles(SRC).map((path) => ({ name: relative(SRC, path), text: readFileSync(path, 'utf8') }))

/** [file:line, snippet] for every match of `re` in the sources. */
function findAll(re, keep = () => true) {
  const hits = []
  for (const { name, text } of FILES) {
    for (const m of text.matchAll(re)) {
      if (!keep(m[0], name)) continue
      const line = text.slice(0, m.index).split('\n').length
      hits.push(`${name}:${line} ${m[0].slice(0, 100).replace(/\s+/g, ' ')}`)
    }
  }
  return hits
}

/** True when an <svg> start tag names itself or is hidden from assistive tech. */
export function svgIsNamedOrHidden(tag) {
  return /\saria-hidden=("true"|\{true\})/.test(tag) || /\saria-label(ledby)?=/.test(tag) || /\srole="img"/.test(tag)
}

describe('svgIsNamedOrHidden', () => {
  it('accepts hidden and named icons and rejects bare ones', () => {
    expect(svgIsNamedOrHidden('<svg aria-hidden="true" class="w-4 h-4">')).toBe(true)
    expect(svgIsNamedOrHidden('<svg role="img" aria-label="Size over time">')).toBe(true)
    expect(svgIsNamedOrHidden('<svg aria-labelledby="t1">')).toBe(true)
    expect(svgIsNamedOrHidden('<svg class="w-4 h-4" fill="none">')).toBe(false)
    expect(svgIsNamedOrHidden('<svg aria-hidden="false">')).toBe(false)
  })
})

describe('design-system source guards', () => {
  it('gives every <svg> aria-hidden="true" or an accessible name (#692)', () => {
    expect(findAll(/<svg\b[^>]*>/gs, (tag) => !svgIsNamedOrHidden(tag))).toEqual([])
  })

  it('sets no text size in pixels, so the 12px floor and browser zoom hold (#698)', () => {
    expect(findAll(/\btext-\[\d+(\.\d+)?px\]/g)).toEqual([])
    // CSS pixel sizes and SVG font-size attributes (user units scale with the chart).
    expect(findAll(/font-size:\s*\d+(\.\d+)?px|font-size=["']?\d/g)).toEqual([])
  })

  it('draws busy rings only in Spinner and InlineSpinner (#698)', () => {
    expect(findAll(/\banimate-spin\b/g, (_m, name) => !/(^|\/)(Inline)?Spinner\.svelte$/.test(name))).toEqual([])
  })

  it('renders every switch from the Switch component (#698)', () => {
    expect(findAll(/role="switch"/g, (_m, name) => !name.endsWith('Switch.svelte'))).toEqual([])
    expect(findAll(/class="sr-only peer"/g, (_m, name) => !name.endsWith('Switch.svelte'))).toEqual([])
  })

  it('keeps every busy ConfirmDialog worded with a busyLabel (#698)', () => {
    const missing = findAll(/<ConfirmDialog\b[\s\S]*?\/>|<ConfirmDialog\b[\s\S]*?<\/ConfirmDialog>/g, (block) => /\sbusy=/.test(block) && !/\sbusyLabel=/.test(block))
    expect(missing).toEqual([])
  })
})
