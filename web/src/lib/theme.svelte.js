// Theme management — two-axis system: style (default/1bit/8bit/16bit) × mode (light/system/dark).
// Persists to localStorage 'vault-style' + 'vault-mode'.
// Applies CSS classes on <html>: .theme-{style} + .dark

const STYLE_KEY = 'vault-style'
const MODE_KEY = 'vault-mode'

/** @type {'default' | '1bit' | '8bit' | '16bit'} */
let style = $state('default')

/** @type {'light' | 'system' | 'dark'} */
let mode = $state('system')

/** @type {boolean} */
let isDark = $state(true)

/** @type {boolean} */
let isThemed = $state(false)

/** @type {MediaQueryList | null} */
let mediaQuery = null

const STYLE_CLASSES = ['theme-1bit', 'theme-8bit', 'theme-16bit']
const VALID_STYLES = /** @type {const} */ (['default', '1bit', '8bit', '16bit'])
const VALID_MODES = /** @type {const} */ (['light', 'system', 'dark'])
const RETRO_FONT_ID = 'vault-retro-fonts'
const RETRO_STYLES = /** @type {const} */ (['8bit', '16bit'])

function applyTheme() {
  const prefersDark = mediaQuery?.matches ?? true
  isThemed = style !== 'default'
  isDark = mode === 'dark' || (mode === 'system' && prefersDark)

  // Apply dark class
  document.documentElement.classList.toggle('dark', isDark)

  // Apply style class — remove all, add the current one
  for (const cls of STYLE_CLASSES) {
    document.documentElement.classList.remove(cls)
  }
  if (style !== 'default') {
    document.documentElement.classList.add(`theme-${style}`)
  }

  // Lazy-load retro fonts only when 8bit/16bit theme is active
  const needsRetroFonts = RETRO_STYLES.includes(/** @type {any} */ (style))
  const existing = document.getElementById(RETRO_FONT_ID)
  if (needsRetroFonts && !existing) {
    const link = document.createElement('link')
    link.id = RETRO_FONT_ID
    link.rel = 'stylesheet'
    link.href = 'https://fonts.googleapis.com/css2?family=Press+Start+2P&family=VT323&display=swap'
    document.head.appendChild(link)
  } else if (!needsRetroFonts && existing) {
    existing.remove()
  }
}

/** Migrate from old single-key 'vault-theme' to two-key system */
function migrateOldTheme() {
  const old = localStorage.getItem('vault-theme')
  if (!old) return
  /** @type {Record<string, {style: string, mode: string}>} */
  const map = {
    'light': { style: 'default', mode: 'light' },
    'dark': { style: 'default', mode: 'dark' },
    'system': { style: 'default', mode: 'system' },
    'retro': { style: '16bit', mode: 'light' },
    'retro-dark': { style: '16bit', mode: 'dark' },
  }
  const mapped = map[old]
  if (mapped) {
    localStorage.setItem(STYLE_KEY, mapped.style)
    localStorage.setItem(MODE_KEY, mapped.mode)
  }
  localStorage.removeItem('vault-theme')
}

export function initTheme() {
  migrateOldTheme()
  const storedStyle = localStorage.getItem(STYLE_KEY)
  const storedMode = localStorage.getItem(MODE_KEY)
  if (storedStyle && VALID_STYLES.includes(/** @type {any} */ (storedStyle))) {
    style = /** @type {'default' | '1bit' | '8bit' | '16bit'} */ (storedStyle)
  }
  if (storedMode && VALID_MODES.includes(/** @type {any} */ (storedMode))) {
    mode = /** @type {'light' | 'system' | 'dark'} */ (storedMode)
  }
  mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
  mediaQuery.addEventListener('change', applyTheme)
  applyTheme()
}

/** @returns {'default' | '1bit' | '8bit' | '16bit'} */
export function getStyle() {
  return style
}

/** @returns {'light' | 'system' | 'dark'} */
export function getMode() {
  return mode
}

/** @returns {boolean} */
export function getIsDark() {
  return isDark
}

/** @returns {boolean} — true when any non-default style is active */
export function getIsThemed() {
  return isThemed
}

// Backward compat alias
export const getIsRetro = getIsThemed

/** @param {'default' | '1bit' | '8bit' | '16bit'} newStyle */
export function setStyle(newStyle) {
  style = newStyle
  localStorage.setItem(STYLE_KEY, newStyle)
  applyTheme()
}

/** @param {'light' | 'system' | 'dark'} newMode */
export function setMode(newMode) {
  mode = newMode
  localStorage.setItem(MODE_KEY, newMode)
  applyTheme()
}
