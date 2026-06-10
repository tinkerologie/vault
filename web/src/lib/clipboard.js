/** Shared clipboard helper */

/**
 * Copy text to the clipboard, returning true on success.
 *
 * navigator.clipboard only exists in secure contexts, so when the UI is
 * served over plain HTTP (e.g. the Unraid plugin proxy on a LAN address)
 * Chrome leaves it undefined and Safari rejects the write with a permission
 * error (issue #129). When the modern API is unavailable or fails, fall back
 * to the legacy execCommand('copy') path via an off-screen textarea, which
 * works in non-secure contexts as long as the call is triggered by a user
 * gesture (e.g. a button click).
 */
export async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission denied or transient failure — try the legacy path below.
    }
  }

  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  // Off-screen but still selectable; display:none would break select().
  textarea.style.position = 'fixed'
  textarea.style.top = '-9999px'
  textarea.style.left = '-9999px'
  document.body.appendChild(textarea)

  const selection = document.getSelection()
  const previousRange = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null

  textarea.select()
  textarea.setSelectionRange(0, textarea.value.length)

  let copied = false
  try {
    copied = document.execCommand('copy')
  } catch {
    copied = false
  }

  textarea.remove()

  // Restore whatever the user had selected before the copy.
  if (selection && previousRange) {
    selection.removeAllRanges()
    selection.addRange(previousRange)
  }

  return copied
}
