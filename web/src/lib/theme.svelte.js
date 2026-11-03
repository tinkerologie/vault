// Theme management – two-axis system: style (default/1bit/retro/dracula/catppuccin) × mode (light/system/dark).
// Persists to localStorage 'vault-style' + 'vault-mode'.
// Applies CSS classes on <html>: .theme-{style} + .dark

const STYLE_KEY = 'vault-style'
const MODE_KEY = 'vault-mode'

/** @type {'default' | '1bit' | 'retro' | 'dracula' | 'catppuccin'} */
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

const VALID_STYLES = /** @type {const} */ (['default', '1bit', 'retro', 'dracula', 'catppuccin'])
const STYLE_CLASSES = VALID_STYLES.filter((s) => s !== 'default').map((s) => `theme-${s}`)
// Styles that change fonts, layout and branding. Dracula and Catppuccin change
// colours only and keep the Default look (#590, #591).
const DECORATIVE_STYLES = ['1bit', 'retro']
// Styles with a single palette. The saved mode is kept, not applied, so it
// returns when the user picks another style (#590).
const DARK_ONLY_STYLES = ['dracula']
// Styles merged into Retro (#532). A Map, so a stored 'constructor' matches nothing.
const LEGACY_STYLES = new Map([['8bit', 'retro'], ['16bit', 'retro']])
const VALID_MODES = /** @type {const} */ (['light', 'system', 'dark'])

function applyTheme() {
  const prefersDark = mediaQuery?.matches ?? true
  isThemed = DECORATIVE_STYLES.includes(style)
  isDark = DARK_ONLY_STYLES.includes(style) || mode === 'dark' || (mode === 'system' && prefersDark)

  // Apply dark class
  document.documentElement.classList.toggle('dark', isDark)

  // Apply style class – remove all, add the current one
  for (const cls of STYLE_CLASSES) {
    document.documentElement.classList.remove(cls)
  }
  if (style !== 'default') {
    document.documentElement.classList.add(`theme-${style}`)
  }

  // Pixel fonts (Press Start 2P / VT323) are self-hosted @font-face rules in
  // app.css. The browser fetches them only when the Retro style's CSS actually
  // selects the family, so there is nothing to load or unload here.
}

/**
 * Migrate from old single-key 'vault-theme' to two-key system.
 * @returns {boolean} true when it carried over a preference
 */
function migrateOldTheme() {
  const old = safeGet('vault-theme')
  if (!old) return false
  const map = new Map([
    ['light', { style: 'default', mode: 'light' }],
    ['dark', { style: 'default', mode: 'dark' }],
    ['system', { style: 'default', mode: 'system' }],
    ['retro', { style: 'retro', mode: 'light' }],
    ['retro-dark', { style: 'retro', mode: 'dark' }],
  ])
  const mapped = map.get(old)
  if (mapped) {
    // Apply the migrated preference in memory immediately so it takes effect
    // this session even if persistence fails. initTheme reads the new keys
    // after this returns, but on a failed write those keys are null and won't
    // override what we set here.
    style = /** @type {'default' | '1bit' | 'retro' | 'dracula' | 'catppuccin'} */ (mapped.style)
    mode = /** @type {'light' | 'system' | 'dark'} */ (mapped.mode)
    // Only retire the legacy key once BOTH new keys are durably written, so a
    // partial write (e.g. quota exceeded mid-migration) can't drop the setting;
    // vault-theme is retained for a retry on the next boot.
    const wroteStyle = safeSet(STYLE_KEY, mapped.style)
    const wroteMode = safeSet(MODE_KEY, mapped.mode)
    if (wroteStyle && wroteMode) {
      safeRemove('vault-theme')
    }
    return true
  }
  // Unrecognised legacy value – nothing to preserve, drop it.
  safeRemove('vault-theme')
  return false
}

/**
 * @param {'light' | 'dark' | null} [hostDefault] the mode to start in when the
 * user has never chosen one. It's never persisted, so any stored choice wins,
 * including an explicit 'system'.
 */
export function initTheme(hostDefault = null) {
  const migrated = migrateOldTheme()
  const savedStyle = safeGet(STYLE_KEY)
  const storedStyle = (savedStyle && LEGACY_STYLES.get(savedStyle)) || savedStyle
  // A failed write keeps Retro for this session and retries on the next load.
  if (storedStyle !== savedStyle) safeSet(STYLE_KEY, storedStyle)
  const storedMode = safeGet(MODE_KEY)
  if (storedStyle && VALID_STYLES.includes(/** @type {any} */ (storedStyle))) {
    style = /** @type {'default' | '1bit' | 'retro' | 'dracula' | 'catppuccin'} */ (storedStyle)
  }
  if (storedMode && VALID_MODES.includes(/** @type {any} */ (storedMode))) {
    mode = /** @type {'light' | 'system' | 'dark'} */ (storedMode)
  } else if (!migrated && (hostDefault === 'light' || hostDefault === 'dark')) {
    mode = hostDefault
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

/** @returns {'default' | '1bit' | 'retro' | 'dracula' | 'catppuccin'} */
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

/** @returns {boolean} – true when a style with its own fonts and layout (1-Bit, Retro) is active */
export function getIsThemed() {
  return isThemed
}

/** @returns {boolean} – true when the style has one palette, so the colour mode has no effect */
export function getIsModeLocked() {
  return DARK_ONLY_STYLES.includes(style)
}

// Backward compat alias
export const getIsRetro = getIsThemed

/** @param {'default' | '1bit' | 'retro' | 'dracula' | 'catppuccin'} newStyle */
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
