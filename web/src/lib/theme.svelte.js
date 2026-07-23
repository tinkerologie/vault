// Theme management – two-axis system: style (default/1bit/8bit/16bit) × mode (light/system/dark).
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

// Brave's aggressive Shields (and Safari private mode / sandboxed iframes) can
// throw SecurityError on any localStorage access. These helpers degrade to
// in-memory defaults instead of throwing, so a blocked storage API can never
// abort theme init and, in turn, the whole boot sequence (issue #250).
/** @param {string} key @returns {string | null} */
function safeGet(key) {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

/** @param {string} key @param {string} value @returns {boolean} true when persisted */
function safeSet(key, value) {
  try {
    localStorage.setItem(key, value)
    return true
  } catch {
    return false // storage blocked or quota exceeded – keep in-memory value only
  }
}

/** @param {string} key */
function safeRemove(key) {
  try {
    localStorage.removeItem(key)
  } catch { /* storage blocked – nothing to remove */ }
}

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

  // Apply style class – remove all, add the current one
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
  const old = safeGet('vault-theme')
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
    // Apply the migrated preference in memory immediately so it takes effect
    // this session even if persistence fails. initTheme reads the new keys
    // after this returns, but on a failed write those keys are null and won't
    // override what we set here.
    style = /** @type {'default' | '1bit' | '8bit' | '16bit'} */ (mapped.style)
    mode = /** @type {'light' | 'system' | 'dark'} */ (mapped.mode)
    // Only retire the legacy key once BOTH new keys are durably written, so a
    // partial write (e.g. quota exceeded mid-migration) can't drop the setting;
    // vault-theme is retained for a retry on the next boot.
    const wroteStyle = safeSet(STYLE_KEY, mapped.style)
    const wroteMode = safeSet(MODE_KEY, mapped.mode)
    if (wroteStyle && wroteMode) {
      safeRemove('vault-theme')
    }
    return
  }
  // Unrecognised legacy value – nothing to preserve, drop it.
  safeRemove('vault-theme')
}

export function initTheme() {
  migrateOldTheme()
  const storedStyle = safeGet(STYLE_KEY)
  const storedMode = safeGet(MODE_KEY)
  if (storedStyle && VALID_STYLES.includes(/** @type {any} */ (storedStyle))) {
    style = /** @type {'default' | '1bit' | '8bit' | '16bit'} */ (storedStyle)
  }
  if (storedMode && VALID_MODES.includes(/** @type {any} */ (storedMode))) {
    mode = /** @type {'light' | 'system' | 'dark'} */ (storedMode)
  }
  // Detach any handler from the previous MediaQueryList before replacing it
  // (e.g. under HMR). Operating on the *existing* mediaQuery is the only way
  // to actually unregister the old listener – a fresh matchMedia() returns a
  // brand-new object that has no listeners attached.
  if (mediaQuery) {
    try {
      mediaQuery.removeEventListener('change', applyTheme)
    } catch { /* ignore – listener may not be attachable in this browser */ }
  }
  // matchMedia is blocked by Brave's fingerprinting protection in some
  // configs; degrade to the dark default rather than throwing (issue #250).
  try {
    mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    mediaQuery.addEventListener('change', applyTheme)
  } catch {
    mediaQuery = null
  }
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

/** @returns {boolean} – true when any non-default style is active */
export function getIsThemed() {
  return isThemed
}

// Backward compat alias
export const getIsRetro = getIsThemed

/** @param {'default' | '1bit' | '8bit' | '16bit'} newStyle */
export function setStyle(newStyle) {
  style = newStyle
  safeSet(STYLE_KEY, newStyle)
  applyTheme()
}

/** @param {'light' | 'system' | 'dark'} newMode */
export function setMode(newMode) {
  mode = newMode
  safeSet(MODE_KEY, newMode)
  applyTheme()
}
