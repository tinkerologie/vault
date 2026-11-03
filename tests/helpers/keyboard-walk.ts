import type { Frame, Page } from '@playwright/test';

/** Elements a screen reader announces as a control. A widget inside another is a nesting defect. */
export const WIDGETS = [
  'a[href]', 'button', 'input:not([type="hidden"])', 'select', 'textarea', 'summary',
  '[role="button"]', '[role="link"]', '[role="tab"]', '[role="switch"]', '[role="checkbox"]',
  '[role="radio"]', '[role="menuitem"]', '[role="option"]', '[role="slider"]',
].join(', ');

export interface TabStop {
  /** First line of the element's ARIA snapshot, e.g. `button "Edit job"`. */
  announced: string;
  /** The element has a role and a non-empty accessible name. */
  named: boolean;
  /** Some focus indicator (outline, shadow, border, background) differs from the unfocused state. */
  visibleFocus: boolean;
  /** The element sits inside another widget. */
  nestedIn: string | null;
}

export interface Walk {
  stops: TabStop[];
  /** Visible, enabled widgets in the page that Tab never reached and that aren't part of a roving group. */
  unreached: string[];
}

type WalkWindow = Window & { __walk?: Element[] };

/**
 * The role and name from the first line of an ARIA snapshot. The line may be
 * YAML-quoted (a name with a colon) and may end in the element's own text when
 * that differs from its name: `'button "Dismiss: Size spike"': Dismiss`.
 */
export function announcement(snapshot: string): string {
  const line = (snapshot.split('\n')[0] ?? '').replace(/^- /, '');
  const quoted = line.match(/^'((?:[^']|'')*)'(?::.*)?$/);
  if (quoted) return quoted[1].replace(/''/g, "'");
  return line.replace(/^([\w-]+ "[^"]*"(?: \[[^\]]*\])*):(?: .*)?$/, '$1');
}

/**
 * Presses Tab from the start of the document until focus wraps, leaves the frame
 * or `limit` stops pass, and describes each stop as a screen reader would meet it.
 */
export async function tabWalk(page: Page, frame: Frame, limit = 300): Promise<Walk> {
  await frame.evaluate(() => {
    (document.activeElement as HTMLElement | null)?.blur?.();
    (window as WalkWindow).__walk = [];
  });
  const stops: TabStop[] = [];
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press('Tab');
    const state = await frame.evaluate((widgets) => {
      const el = document.activeElement;
      if (!el || el === document.body || !document.hasFocus()) return 'left';
      const seen = (window as WalkWindow).__walk!;
      if (seen.includes(el)) return 'wrapped';
      seen.push(el);
      const outer = el.parentElement?.closest(widgets);
      return outer ? outer.outerHTML.slice(0, 120) : null;
    }, WIDGETS);
    if (state === 'left' || state === 'wrapped') break;
    const snapshot = await frame.locator(':focus').ariaSnapshot().catch(() => '');
    const announced = announcement(snapshot);
    stops.push({ announced, named: /^[\w-]+ "[^"]*\S[^"]*"/.test(announced), visibleFocus: true, nestedIn: state });
  }

  // Compare each stop's focused look with its resting look.
  const looks = await frame.evaluate(async () => {
    const props = ['outline-style', 'outline-width', 'outline-color', 'box-shadow', 'border-color', 'background-color', 'text-decoration-line'];
    const look = (el: Element) => { const cs = getComputedStyle(el); return props.map((p) => cs.getPropertyValue(p)).join('|'); };
    const seen = (window as WalkWindow).__walk!;
    const focused: string[] = [];
    for (const el of seen) {
      (el as HTMLElement).focus({ preventScroll: true });
      focused.push(el.isConnected ? look(el) : '');
    }
    (document.activeElement as HTMLElement | null)?.blur?.();
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    return seen.map((el, i) => !el.isConnected || focused[i] !== look(el));
  });
  // Programmatic focus doesn't always match :focus-visible, so a stop counts as
  // visible if either the keyboard pass or the replay shows a difference.
  looks.forEach((ok, i) => { if (stops[i]) stops[i].visibleFocus = ok; });

  // A control with a negative tabIndex still counts as unreached unless it is
  // deliberately out of the Tab order and usable another way.
  const unreached = await frame.evaluate((widgets) => {
    const seen = new Set((window as WalkWindow).__walk!);
    const ROVING = '[role="tablist"], [role="radiogroup"], [role="toolbar"], [role="menu"], [role="menubar"], [role="listbox"], [role="grid"], [role="treegrid"], [role="tree"]';
    // A roving-tabindex member whose group has a member Tab reached: arrows move between them.
    const rovingWithReachedMember = (el: HTMLElement) => {
      const group = el.closest(ROVING);
      return !!group && [...seen].some((s) => s !== el && group.contains(s));
    };
    // A native radio whose group (same name, same form) has a member Tab reached.
    const radioWithReachedMember = (el: HTMLElement) => el instanceof HTMLInputElement && el.type === 'radio' && !!el.name
      && [...seen].some((s) => s instanceof HTMLInputElement && s.type === 'radio' && s.name === el.name && s.form === el.form);
    return [...document.querySelectorAll<HTMLElement>(widgets)]
      .filter((el) => !seen.has(el))
      .filter((el) => el.checkVisibility() && el.getBoundingClientRect().width > 0)
      .filter((el) => !el.closest('[inert], [aria-hidden="true"]') && !(el as HTMLButtonElement).disabled && el.getAttribute('aria-disabled') !== 'true')
      .filter((el) => !rovingWithReachedMember(el) && !radioWithReachedMember(el))
      // Content inside a closed <details> is skipped by Tab; its summary is reached instead.
      .filter((el) => !el.closest('details:not([open]) > :not(summary)'))
      .map((el) => el.outerHTML.slice(0, 120));
  }, WIDGETS);

  return { stops, unreached };
}
