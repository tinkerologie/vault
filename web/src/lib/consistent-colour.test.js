import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { CARD_IDLE, CARD_SELECTED, cardClass } from './selectable.js'
import { doneCountClass, sizeTrendClass } from './outcome-colour.js'
import { PROTECTION_DOT, protectionDot } from './protection.js'

// One meaning per colour across pages (#710).
const SRC = new URL('..', import.meta.url).pathname
const read = (p) => readFileSync(SRC + p, 'utf8')

describe('selected cards', () => {
  it('select in amber and rest neutral', () => {
    expect(cardClass(true)).toBe(CARD_SELECTED)
    expect(cardClass(false)).toBe(CARD_IDLE)
    expect(CARD_SELECTED).toContain('border-vault')
    expect(CARD_IDLE).not.toMatch(/vault/)
  })

  it('are the one style for type, storage-type and radio cards', () => {
    for (const file of ['components/TypePicker.svelte', 'components/StorageForm.svelte', 'components/BackupModeSelector.svelte', 'pages/RecoverWizard.svelte']) {
      expect(read(file), file).toContain('cardClass(')
    }
  })
})

describe('run counts and trends', () => {
  it('colours a done count green only for a fully successful run', () => {
    expect(doneCountClass({ status: 'success', items_done: 10, items_total: 10 })).toBe('text-success')
    expect(doneCountClass({ status: 'cancelled', items_done: 0, items_total: 10 })).toBe('text-text')
    expect(doneCountClass({ status: 'partial', items_done: 8, items_total: 10, items_failed: 2 })).toBe('text-text')
    expect(doneCountClass({ status: 'success', items_done: 9, items_total: 10 })).toBe('text-text')
    expect(doneCountClass({ status: 'success', items_done: 0, items_total: 0 })).toBe('text-text')
    expect(doneCountClass(null)).toBe('text-text')
  })

  it('keeps a size trend neutral in both directions', () => {
    expect(sizeTrendClass('up')).toBe(sizeTrendClass('down'))
    expect(sizeTrendClass('down')).not.toMatch(/success|danger|warning/)
  })
})

describe('protection dots', () => {
  it('match the state on every page and never make unprotected red or green', () => {
    expect(protectionDot('protected')).toBe('bg-success')
    expect(protectionDot('pending')).toBe('bg-info')
    expect(protectionDot('unprotected')).toBe(PROTECTION_DOT.unprotected)
    expect(protectionDot('unprotected')).not.toMatch(/danger|success/)
    expect(protectionDot('nonsense')).toBe(PROTECTION_DOT.unknown)
    expect(read('pages/Dashboard.svelte')).toContain('protectionDot(it.state)')
    expect(read('pages/Recovery.svelte')).toContain("protectionDot('unprotected')")
  })
})

describe('links and trust badges', () => {
  it('draw text links in Amber Ink, not info blue', () => {
    for (const file of ['components/RestoreRequestList.svelte', 'components/RestoreRequestReview.svelte', 'components/AssistantKeys.svelte', 'pages/Settings.svelte']) {
      expect(read(file), file).not.toMatch(/class="[^"]*\btext-info\b[^"]*hover:underline/)
    }
  })

  it('show Verified as success and Recommended as neutral', () => {
    const t = read('components/RestorePointTimeline.svelte')
    expect(t).toMatch(/bg-success\/15 text-success font-medium">Verified/)
    expect(t).toMatch(/bg-surface-3 text-text-muted font-medium">Recommended/)
  })

  it('give Settings passphrase fields the shared input style', () => {
    const s = read('pages/Settings.svelte')
    for (const id of ['change-pass', 'change-confirm', 'enc-pass', 'enc-confirm']) {
      const at = s.indexOf(`id="${id}"`)
      const tag = s.slice(at, s.indexOf('/>', at))
      expect(tag, id).toContain('bg-surface-3')
      expect(tag, id).not.toContain('focus:ring')
    }
  })
})
