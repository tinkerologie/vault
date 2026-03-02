// Theme management — supports 'light', 'dark', and 'system' modes.
// Persists to localStorage.'vault-theme'. Applies .dark class on <html>.

const STORAGE_KEY = 'vault-theme'

/** @type {'light' | 'dark' | 'system'} */
let mode = $state('system')

/** @type {boolean} */
let isDark = $state(true)

/** @type {MediaQueryList | null} */
let mediaQuery = null

function applyTheme() {
  const prefersDark = mediaQuery?.matches ?? true
  isDark = mode === 'dark' || (mode === 'system' && prefersDark)
  document.documentElement.classList.toggle('dark', isDark)
}

export function initTheme() {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark' || stored === 'system') {
    mode = stored
  }
  mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
  mediaQuery.addEventListener('change', applyTheme)
  applyTheme()
}

/** @returns {'light' | 'dark' | 'system'} */
export function getTheme() {
  return mode
}

/** @returns {boolean} */
export function getIsDark() {
  return isDark
}

/** @param {'light' | 'dark' | 'system'} newMode */
export function setTheme(newMode) {
  mode = newMode
  localStorage.setItem(STORAGE_KEY, newMode)
  applyTheme()
}
