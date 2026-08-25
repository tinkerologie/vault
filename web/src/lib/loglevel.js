// loglevel.js — one place that decides how a log level looks.
//
// The console previously repeated the same switch four times (row color, meta
// color, left-border color, display label), so a level could be styled
// consistently in three of them and forgotten in the fourth. Levels are
// normalised here too: the API emits both "warn" and "warning" for the same
// thing.
//
// Colour is never the only signal. Every row also carries a text label, so the
// console stays readable in the 1-bit and 8-bit themes — where success,
// danger, warning and info all resolve to the same ink — and for anyone who
// cannot distinguish the hues.

// Canonical level for a raw API value. Unknown levels fall back to info rather
// than rendering unstyled.
export function normalizeLevel(level) {
  const l = (level || '').toLowerCase()
  if (l === 'warning') return 'warn'
  if (l === 'error' || l === 'warn' || l === 'debug' || l === 'info') return l
  if (l === 'success') return 'success'
  return 'info'
}

const STYLES = {
  error: {
    label: 'error',
    text: 'text-danger',
    meta: 'text-danger/70',
    border: 'border-l-danger',
    row: 'bg-danger/[0.07]',
    pill: 'bg-danger/15 text-danger',
  },
  warn: {
    label: 'warn',
    text: 'text-text',
    meta: 'text-text-dim',
    border: 'border-l-warning',
    row: 'bg-warning/[0.05]',
    pill: 'bg-warning/15 text-warning',
  },
  success: {
    label: 'ok',
    text: 'text-text',
    meta: 'text-text-dim',
    border: 'border-l-success',
    row: '',
    pill: 'bg-success/15 text-success',
  },
  info: {
    label: 'info',
    text: 'text-text',
    meta: 'text-text-dim',
    border: 'border-l-info/50',
    row: '',
    pill: 'bg-info/12 text-info',
  },
  debug: {
    label: 'debug',
    text: 'text-text-dim',
    meta: 'text-text-dim/70',
    border: 'border-l-text-dim/30',
    row: '',
    pill: 'bg-text-dim/15 text-text-dim',
  },
}

// Full style set for a level: text, metadata, left border, row tint, pill.
export function levelStyle(level) {
  return STYLES[normalizeLevel(level)]
}

// Short label shown in the level pill.
export function levelLabel(level) {
  return STYLES[normalizeLevel(level)].label
}
