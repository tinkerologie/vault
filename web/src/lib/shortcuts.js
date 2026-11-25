/**
 * The modifier to show in a keyboard-shortcut hint (#691): "⌘" on Apple
 * devices, "Ctrl+" elsewhere. App.svelte accepts either key, so this only
 * changes what the hint says.
 * @returns {'⌘' | 'Ctrl+'}
 */
export function shortcutModifier() {
  if (typeof navigator === 'undefined' || !navigator) return 'Ctrl+'
  const platform = navigator.userAgentData?.platform || navigator.platform || navigator.userAgent || ''
  return /mac|iphone|ipad|ipod/i.test(platform) ? '⌘' : 'Ctrl+'
}
