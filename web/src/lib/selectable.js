// One selected-card style (#710) for choice cards: backup type cards, storage
// type cards and radio cards. Selection is amber (DESIGN.md "Amber Means Act or
// Chosen"); a category hue stays on the card's icon as identity, never as the
// selection signal. The control still says it is selected with aria-pressed,
// aria-checked or a checked radio.

export const CARD_SELECTED = 'border-vault bg-vault/10'
export const CARD_IDLE = 'border-border bg-surface-3 hover:border-border-hover'

/**
 * Border and fill classes for a choice card.
 * @param {boolean} selected
 * @returns {string}
 */
export function cardClass(selected) {
  return selected ? CARD_SELECTED : CARD_IDLE
}
