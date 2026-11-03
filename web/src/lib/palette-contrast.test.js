import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

// Colour-only styles (#590 Dracula, #591 Catppuccin): reads the palette blocks
// from app.css and checks every text/surface pair the components use against
// WCAG 2.2 AA – 4.5:1 for text, 3:1 for UI parts. axe in tests/contrast.spec.ts
// measures rendered text; this covers the pairs axe cannot see (borders, dots,
// fills, focus rings) and fails at the token, before any page renders.

const SRC = new URL('..', import.meta.url).pathname
const CSS = readFileSync(join(SRC, 'app.css'), 'utf8')

/** @returns {Map<string, string>} custom properties declared in one top-level block */
function block(selector) {
  const head = selector === '@theme' ? '@theme' : selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = CSS.match(new RegExp(`^${head} \\{([^}]*)\\}`, 'm'))
  if (!match) throw new Error(`app.css has no ${selector} block`)
  const vars = new Map()
  for (const [, name, value] of match[1].replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    vars.set(name, value.trim())
  }
  return vars
}

// The cascade each style sees on <html>, lowest precedence first. The last
// block is the style's own and must define every token by itself.
const THEMES = {
  dracula: ['@theme', '.dark', '.theme-dracula'],
  'catppuccin latte': ['@theme', '.theme-catppuccin'],
  'catppuccin mocha': ['@theme', '.dark', '.theme-catppuccin', '.theme-catppuccin.dark'],
}

function palette(chain) {
  const merged = new Map()
  for (const sel of chain) for (const [k, v] of block(sel)) merged.set(k, v)
  const resolve = (name, seen = []) => {
    if (seen.includes(name)) throw new Error(`cycle: ${[...seen, name].join(' -> ')}`)
    const value = merged.get(name)
    if (value === undefined) throw new Error(`${name} is not defined`)
    const ref = value.match(/^var\((--[\w-]+)\)$/)
    if (ref) return resolve(ref[1], [...seen, name])
    if (!/^#[0-9a-f]{6}$/i.test(value)) throw new Error(`${name} is ${value}, expected a #rrggbb hex`)
    return value.toLowerCase()
  }
  return (name) => resolve(`--color-${name}`)
}

const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
const luminance = (hex) => {
  const [r, g, b] = rgb(hex).map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
/** `fg` at `alpha` over an opaque `bg`, as bg-x/15 paints it. */
const over = (fg, alpha, bg) =>
  '#' + rgb(fg).map((v, i) => Math.round(v * alpha + rgb(bg)[i] * (1 - alpha)).toString(16).padStart(2, '0')).join('')

const SEMANTIC = [
  'vault', 'vault-dark', 'vault-text', 'vault-light', 'on-vault', 'on-status', 'placeholder',
  'surface', 'surface-2', 'surface-3', 'surface-4', 'surface-5', 'border', 'border-hover',
  'text', 'text-muted', 'text-dim', 'success', 'danger', 'warning', 'info',
  ...['blue', 'purple', 'emerald', 'cyan', 'amber', 'rose', 'orange'].map((h) => `hue-${h}`),
]

const FAMILIES = 'slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose'
const UTILITY = new RegExp(`(?<![\\w-])(?:[\\w-]+:)*(?:bg|text|border|ring|outline|fill|stroke|from|via|to|accent|divide|decoration|shadow)-(${FAMILIES})-(\\d{2,3})\\b`, 'g')

function sources(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    return /\.(svelte|js)$/.test(name) && !/\.test\.js$/.test(name) ? [path] : []
  })
}

/** Every Tailwind palette colour a component paints with, e.g. bg-amber-500/15 -> amber-500. */
function paletteShadesInUse() {
  const shades = new Set()
  for (const file of sources(SRC)) {
    const text = readFileSync(file, 'utf8')
    for (const [, family, shade] of text.matchAll(UTILITY)) shades.add(`${family}-${shade}`)
    if (/(?<![\w-])(?:[\w-]+:)*bg-white\b/.test(text)) shades.add('white')
  }
  return [...shades].sort()
}

/** [label, foreground, background, minimum] for one palette. */
function pairs(c) {
  const list = []
  const add = (label, fg, bg, min) => list.push([label, fg, bg, min])
  const panels = ['surface', 'surface-2', 'surface-3']
  const textTokens = ['text', 'text-muted', 'text-dim', 'placeholder', 'vault-text', 'success', 'danger', 'warning', 'info',
    ...['blue', 'purple', 'emerald', 'cyan', 'amber', 'rose', 'orange'].map((h) => `hue-${h}`)]

  // Text on every panel it sits on; hover rows and secondary buttons use surface-4.
  for (const t of textTokens) for (const s of panels) add(`${t} on ${s}`, c(t), c(s), 4.5)
  for (const t of ['text', 'text-muted']) add(`${t} on surface-4`, c(t), c('surface-4'), 4.5)

  // Status and accent tints (alert panels at /5 and /10, badges at /15) carry
  // body text on any panel, and dim or coloured text on the page and on cards.
  const own = { success: 'success', danger: 'danger', warning: 'warning', info: 'info', vault: 'vault-text' }
  for (const [tint, fgOwn] of Object.entries(own)) {
    for (const a of [0.05, 0.1, 0.15]) {
      for (const s of panels) {
        for (const t of ['text', 'text-muted']) add(`${t} on ${tint}/${a * 100} over ${s}`, c(t), over(c(tint), a, c(s)), 4.5)
      }
      for (const s of ['surface', 'surface-2']) {
        for (const t of ['text-dim', fgOwn]) add(`${t} on ${tint}/${a * 100} over ${s}`, c(t), over(c(tint), a, c(s)), 4.5)
      }
    }
    // Logs: a level badge (/15) on its row's /7 tint.
    for (const s of ['surface', 'surface-2']) {
      add(`${fgOwn} badge on a ${tint} log row over ${s}`, c(fgOwn), over(c(tint), 0.15, over(c(tint), 0.07, c(s))), 4.5)
    }
  }
  // Selected tiles (bg-vault/10, /20) with their label.
  for (const a of [0.1, 0.2]) for (const s of ['surface', 'surface-2']) add(`vault-text on vault/${a * 100} tile over ${s}`, c('vault-text'), over(c('vault'), a, c(s)), 4.5)

  // Categorical chips: hue text on its palette tint, and other fixed palette tints.
  const chips = [
    ...['blue', 'purple', 'emerald', 'cyan', 'amber', 'orange'].map((h) => [`hue-${h}`, `${h}-500`, 0.15]),
    ['hue-purple', 'purple-500', 0.1], ['hue-rose', 'rose-500', 0.1], ['hue-amber', 'amber-500', 0.25],
    ['text', 'amber-500', 0.1], ['text-muted', 'amber-500', 0.1],
    ['success', 'green-500', 0.15], ['success', 'green-500', 0.1], ['warning', 'yellow-500', 0.15],
    ['text-muted', 'gray-500', 0.15], ['danger', 'red-500', 0.1],
  ]
  for (const [t, tint, a] of chips) {
    for (const s of ['surface', 'surface-2']) add(`${t} on ${tint}/${a * 100} over ${s}`, c(t), over(c(tint), a, c(s)), 4.5)
  }
  // TypePicker: the type's badge on its selected card's /5 tint.
  for (const h of ['blue', 'purple', 'amber', 'emerald', 'cyan']) {
    add(`hue-${h} badge on a selected ${h} card`, c(`hue-${h}`), over(c(`${h}-500`), 0.15, over(c(`${h}-400`), 0.05, c('surface-2'))), 4.5)
  }

  // Labels on solid fills.
  add('on-vault on vault', c('on-vault'), c('vault'), 4.5)
  add('on-vault on vault-dark (hover)', c('on-vault'), c('vault-dark'), 4.5)
  for (const t of ['success', 'danger', 'warning', 'info']) add(`on-status on ${t}`, c('on-status'), c(t), 4.5)
  add('on-status on danger/90 (hover)', c('on-status'), over(c('danger'), 0.9, c('surface')), 4.5)
  add('connection banner text', c('amber-950'), over(c('amber-500'), 0.95, c('surface')), 4.5)

  // UI parts, 3:1 against what sits next to them.
  for (const s of ['surface', 'surface-2']) {
    add(`border on ${s}`, c('border'), c(s), 3)
    add(`focus ring and checked controls on ${s}`, c('vault'), c(s), 3)
  }
  add('unprotected status dot', c('surface-5'), c('surface-3'), 3)
  add('protected status dot', c('success'), over(c('success'), 0.05, c('surface-2')), 3)
  add('pending status dot', c('amber-500'), over(c('amber-500'), 0.05, c('surface-2')), 3)
  for (const fill of ['vault', 'amber-500', 'rose-500']) add(`capacity fill ${fill} on its track`, c(fill), c('surface-3'), 3)
  add('dedup fill on its track', c('vault'), over(c('emerald-500'), 0.2, c('surface-2')), 3)
  for (const h of ['blue', 'purple', 'amber', 'emerald', 'cyan']) {
    add(`selected ${h} card border`, c(`${h}-400`), over(c(`${h}-400`), 0.05, c('surface-2')), 3)
  }
  add('active-run dot', c('orange-400'), c('surface-2'), 3)
  return list
}

describe.each(Object.entries(THEMES))('%s palette', (name, chain) => {
  const own = block(chain.at(-1))
  const c = palette(chain)

  it('defines every semantic token itself, so no Default value leaks through', () => {
    expect(SEMANTIC.filter((t) => !own.has(`--color-${t}`))).toEqual([])
  })

  it('defines every Tailwind palette shade a component uses', () => {
    const missing = paletteShadesInUse().filter((shade) => !own.has(`--color-${shade}`))
    expect(missing, 'add these to the style block in app.css').toEqual([])
  })

  it('resolves every token to a hard-coded hex value', () => {
    for (const t of SEMANTIC) expect(() => c(t), t).not.toThrow()
  })

  it('meets WCAG AA for every text, fill and UI-part pair', () => {
    const failures = pairs(c)
      .map(([label, fg, bg, min]) => ({ label, fg, bg, ratio: +ratio(fg, bg).toFixed(2), min }))
      .filter((p) => p.ratio < p.min)
    expect(failures).toEqual([])
  })
})

describe('Latte switch thumbs', () => {
  it('ring the pale thumb at 3:1 against the thumb and the off track', () => {
    const c = palette(THEMES['catppuccin latte'])
    expect(CSS).toMatch(/\.theme-catppuccin:not\(\.dark\) \[class~="bg-white"\][\s\S]*?box-shadow: 0 0 0 1px var\(--color-text-dim\)/)
    expect(ratio(c('text-dim'), c('white'))).toBeGreaterThanOrEqual(3)
    expect(ratio(c('text-dim'), c('surface-4'))).toBeGreaterThanOrEqual(3)
  })
})

describe.each([['dracula'], ['catppuccin mocha']])('%s switch thumbs', (name) => {
  it('stand out from the off track at 3:1', () => {
    const c = palette(THEMES[name])
    expect(ratio(c('white'), c('surface-4'))).toBeGreaterThanOrEqual(3)
  })
})

describe('contrast helpers', () => {
  it('match the WCAG reference values', () => {
    expect(ratio('#000000', '#ffffff')).toBeCloseTo(21, 5)
    expect(ratio('#ffffff', '#ffffff')).toBe(1)
    // From #590: Dracula comment on background.
    expect(ratio('#6272a4', '#282a36')).toBeCloseTo(3.03, 2)
    expect(over('#ffffff', 0.5, '#000000')).toBe('#808080')
  })

  it('fail a pair below the threshold', () => {
    // Official Latte green on base is 2.96:1, which is why success is derived.
    expect(ratio('#40a02b', '#eff1f5')).toBeLessThan(4.5)
  })
})
