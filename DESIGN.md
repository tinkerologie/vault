---
name: Vault
description: Backup and restore for Unraid; calm until it matters.
colors:
  guardian-amber: "#f59e0b"
  guardian-amber-deep: "#d97706"
  amber-ink: "#92400e"
  on-amber: "#111827"
  surface: "#ffffff"
  surface-raised: "#f9fafb"
  surface-sunken: "#f3f4f6"
  surface-hover: "#e5e7eb"
  surface-strong: "#d1d5db"
  border: "#e5e7eb"
  border-hover: "#d1d5db"
  ink: "#111827"
  ink-muted: "#4b5563"
  ink-dim: "#5f6673"
  status-success: "#166534"
  status-danger: "#991b1b"
  status-warning: "#92400e"
  status-info: "#1d4ed8"
  night-surface: "#111827"
  night-surface-raised: "#1f2937"
  night-surface-sunken: "#374151"
  night-ink: "#f9fafb"
  night-ink-muted: "#d1d5db"
  night-ink-dim: "#aeb4be"
  night-success: "#4ade80"
  night-danger: "#fca5a5"
  night-warning: "#fbbf24"
  night-info: "#93c5fd"
typography:
  headline:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "1.5rem"
    fontWeight: 700
    lineHeight: "2rem"
  title:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: "1.75rem"
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: "1.25rem"
  label:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: "1rem"
  mono:
    fontFamily: "ui-monospace, 'SF Mono', 'Cascadia Code', 'Fira Code', Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1rem"
rounded:
  control: "8px"
  container: "12px"
  pill: "9999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
components:
  button-primary:
    backgroundColor: "{colors.guardian-amber}"
    textColor: "{colors.on-amber}"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
  button-primary-hover:
    backgroundColor: "{colors.guardian-amber-deep}"
  button-secondary:
    backgroundColor: "{colors.surface-sunken}"
    textColor: "{colors.ink-muted}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
  button-secondary-hover:
    backgroundColor: "{colors.surface-hover}"
    textColor: "{colors.ink}"
  button-danger:
    backgroundColor: "{colors.status-danger}"
    textColor: "{colors.surface}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
  button-ghost:
    textColor: "{colors.ink-muted}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
  button-ghost-hover:
    backgroundColor: "{colors.surface-sunken}"
    textColor: "{colors.ink}"
  input:
    backgroundColor: "{colors.surface-sunken}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "8px 12px"
  card:
    backgroundColor: "{colors.surface-raised}"
    rounded: "{rounded.container}"
    padding: "16px"
  badge-success:
    textColor: "{colors.status-success}"
    rounded: "{rounded.pill}"
    padding: "2px 10px"
  nav-item-active:
    textColor: "{colors.amber-ink}"
    rounded: "{rounded.control}"
    padding: "10px 12px"
---

# Design System: Vault

## Overview

**Creative North Star: "The Quiet Guardian"**

Vault watches over a server most owners would rather not think about. The interface is calm and steady, and stays out of the way while everything is protected. When something needs attention, it says so plainly and with colour. Most of the time the screen is neutral grey and white (or deep slate at night), set in the system font, with one warm amber for what you can act on and what you have chosen: buttons, Run actions, selected filters, days and modes, switches when on, and the active page. Amber never decorates. Headings, borders, icons and backgrounds stay neutral.

The guardian is warm rather than severe. Corners are gently rounded, status uses soft tints rather than loud fills, and empty states are friendly. It is also exact: sizes, times, counts and paths are shown precisely, in tabular figures, with no rounding off of what will change. It lives inside Unraid, so it uses the host's conventions (system type, ordinary controls, the host's light or dark mode) and doesn't try to stand out from the pages around it.

Density sits in the middle. A set-and-forget owner gets the answer to "am I protected?" first. A power user finds every option one disclosure away, never crammed onto the first screen.

**Key Characteristics:**
- Neutral tonal surfaces with 1px borders; one amber accent for action and selection.
- The system font stack at a compact 14px body; hierarchy from weight and size, never from colour alone.
- Flat at rest; only overlays (dialogs, toasts, the command palette) float, with a shadow and a blurred backdrop.
- Gently rounded: 8px controls, 12px containers, full pills for badges and switches.
- Every visual decision is a semantic token, so five theme styles (Default, 1-Bit, Retro, Dracula, Catppuccin) can re-skin the whole UI by overriding tokens alone.

## Colors

A neutral grey world with one warm amber voice and four status colours that always appear with words or icons.

### Primary
- **Guardian Amber** (#f59e0b): the brand fill. Primary buttons, the active state of segmented controls and filter pills, switches when on, progress bars, the logo tile. The same value is used in light and dark mode.
- **Deep Amber** (#d97706): the hover and pressed state of Guardian Amber fills.
- **Amber Ink** (#92400e): amber for small text on light surfaces (active nav label, links, captions, `badge-vault`), about 6.3:1 on the raised surface. In dark mode this role maps back to Guardian Amber.
- **On Amber** (#111827): the label colour on any amber fill, in both modes. White on amber fails contrast and is not used.

### Neutral
- **Page White** (#ffffff) / **Night Slate** (#111827): the page background.
- **Raised Paper** (#f9fafb) / **Night Panel** (#1f2937): cards, the sidebar, dialog panels.
- **Sunken Grey** (#f3f4f6) / **Night Well** (#374151): inputs, secondary buttons, segmented-control tracks, hover rows.
- **Hover Grey** (#e5e7eb) and **Strong Grey** (#d1d5db): hover fills, switch tracks when off, dividers that need more weight.
- **Hairline** (#e5e7eb, hover #d1d5db; dark #374151 / #4b5563): every container's 1px border.
- **Ink** (#111827) / **Night Ink** (#f9fafb): headings and primary text.
- **Muted Ink** (#4b5563) / **Night Muted** (#d1d5db): secondary text, descriptions, inactive nav.
- **Dim Ink** (#5f6673) / **Night Dim** (#aeb4be): meta text such as timestamps and counts. It is still AA, but on the sunken surface use Muted Ink instead.

### Status
- **Protected Green** (#166534 / night #4ade80), **Alarm Red** (#991b1b / night #fca5a5), **Caution Amber** (#92400e / night #fbbf24), **Signal Blue** (#1d4ed8 / night #93c5fd). These are used as text, and as tinted badge backgrounds at 15% opacity. Use solid fills only for the danger button and toast icons.
- **Categorical hues** (`hue-blue`, `hue-purple`, `hue-emerald`, `hue-cyan`, `hue-amber`, `hue-rose`, `hue-orange`): tell item and storage types apart (container, VM, folder, ZFS, plugin; S3, SMB…). They are 800-weight in light mode and 300-weight in dark.

### Named Rules
**The Amber Means Act or Chosen Rule.** Amber fills are for things the user can act on (primary buttons, Save and Apply, Run Now) and for what they have chosen (selected filters, segments, day pills and type cards, switches when on, the active page). This is a deliberate decision (#709, declined 2026-11-25): screens may carry several amber controls. Amber is never decoration: headings, borders at rest, icons, backgrounds and status meaning use neutral or status tokens.

**The Status Speaks Twice Rule.** A status colour never appears alone. Pair it with a word ("Failed", "Protected") or an icon, so the meaning survives 1-Bit, colour blindness and screen readers.

**The Unknown Is Not Green Rule.** Green means verified protected. Loading, unknown or unavailable states use neutral ink and say what's unknown. Never default to a success colour or a zero.

**The Token-Only Rule.** Components use only semantic tokens (`surface-*`, `text*`, `border*`, `vault*`, status, `hue-*`). Raw palette classes (`amber-500`, `gray-700`) and hex values in components break the five theme styles.

## Typography

**Body Font:** the system UI stack (-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Helvetica Neue, Arial, sans-serif)
**Mono Font:** ui-monospace (SF Mono, Cascadia Code, Fira Code, Consolas) for paths, cron expressions, log lines and IDs
**Retro only:** Press Start 2P and VT323, self-hosted and loaded only when that style is selected

**Character:** a single native sans that reads as part of Unraid and the operating system. Hierarchy comes from weight (400 body, 500 labels, 600 titles, 700 page headings) and a short size ladder, not from decorative type.

### Hierarchy
- **Headline** (700, 24px, 32px line): the page `h1`, one per route ("Backup Jobs", "Restore"), with a muted one-line description beneath.
- **Title** (600, 18px, 28px line): dialog titles and major section headings inside a page.
- **Card title** (500–600, 14px): card and row names, item and job names.
- **Body** (400, 14px, 20px line): the default for UI text, descriptions and form values.
- **Label** (500, 12px, 16px line): buttons in small sizes, badges, form helper text, meta rows.
- **Eyebrow** (500–600, 12px, uppercase, wide tracking): tile and section captions such as "HEALTH SCORE" and "BACKUP JOBS". Use sparingly.

### Named Rules
**The 12px Floor Rule.** No readable text below 12px (0.75rem), and sizes are set in rem so the browser's font-size setting still works. The existing `text-[11px]`, `text-[10px]` and SVG labels below 12px are debt to remove (#698, #690), not a pattern to copy.

**The Tabular Truth Rule.** Counts, sizes, durations, percentages and times use `tabular-nums` and locale formatting, so live-updating values don't jitter and columns line up.

## Layout

- **Frame:** at 1024px and wider, a fixed 256px sidebar (Raised Paper, right hairline) holds the logo, grouped navigation (Protect, Recover, System) and the version. Below 1024px, a top bar and a five-item bottom tab bar (Home, Jobs, History, Restore, More) replace it, and More opens a sheet. When embedded in Unraid, all of these chrome elements are replaced by a single row of links.
- **Content column:** centred, 1152px max width, with 16 / 24 / 32px side padding at small, medium and large widths and 24px top padding. Each route starts with the same header: `h1` plus description on the left, the page's one primary action on the right.
- **Rhythm:** a 4px base step. 8px inside controls, 12–16px inside cards, 16–24px between cards and sections.
- **Grids:** dashboard tiles and restore item cards use responsive 1/2/3/4-column grids. Two-column form grids should collapse to one column below 640px.
- **Breakpoints:** Tailwind defaults (640, 768, 1024, 1280). 1024 is where the frame switches between sidebar and bottom bar.
- **Embedded:** the document takes its natural height and the host frame grows to fit. Fixed UI anchors to `--vault-visible-top` / `--vault-visible-height` rather than `vh`.

## Elevation & Depth

Flat by default. Structure comes from tonal steps (page, raised, sunken) and 1px hairline borders, never from shadow. Depth is used only for things that float above the page and need to separate from it: dialogs and sheets get a large soft shadow over a 60% black backdrop with an 8px blur, and toasts and tooltips get a small shadow. Hover on a clickable card changes its border towards amber (40% opacity), not its elevation.

### Shadow Vocabulary
- **Overlay** (`box-shadow: 0 25px 50px -12px rgb(0 0 0 / 0.25)`): dialog and sheet panels only.
- **Float** (`box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.1), 0 4px 6px -4px rgb(0 0 0 / 0.1)`): toasts and the sticky restore action bar.
- **Hint** (`box-shadow: 0 1px 3px 0 rgb(0 0 0 / 0.1), 0 1px 2px -1px rgb(0 0 0 / 0.1)`): tooltips.

### Named Rules
**The Flat-By-Default Rule.** Surfaces are flat at rest. A shadow means "this is floating above the page and can be dismissed". Never put one on a card to make it look more important.

## Shapes

Gently rounded and consistent. Controls (buttons, inputs, selects, nav items, icon buttons) are 8px. Containers (cards, tiles, dialogs, panels) are 12px; sheets round only their top corners. Badges, status dots, switches, filter pills and progress bars are full pills. Borders are always 1px hairlines in the border token; a heavier or coloured border signals a state (hover, selected, error), never decoration. The logo is an amber 6px-radius tile carrying a white shield. The shield is the only emblem, and it stays the mark and isn't used as ornament.

## Components

### Buttons
Quiet and plain: a small rounded rectangle, a medium-weight 14px label, and a colour transition on hover.
- **Shape:** gently rounded (8px), padding 8px × 16px; small size 6px × 12px at 12px.
- **Primary:** Guardian Amber fill with On Amber text; hover Deep Amber. Used for each section's Save or Apply, for Next and Finish, and for the main action of a dialog. Row-level Run Now uses the 15% amber tint with Amber Ink text.
- **Secondary:** Sunken Grey fill, Muted Ink text; hover Hover Grey with Ink text.
- **Danger:** Alarm Red fill with On Status text, used only for the confirm button of a destructive dialog. Destructive actions elsewhere stay ghost or secondary until confirmed.
- **Ghost:** no fill, Muted Ink; hover Sunken Grey. Also used for row actions and toolbars.
- **Focus:** a 2px outline in the accent with a 2px offset, global and outside any CSS layer so utilities can't remove it. In the Default light theme the amber ring is under 3:1 (#684), so give it a darker light-mode focus token.
- **Disabled:** 50% opacity and a not-allowed cursor. While busy, the label changes ("Saving…") and a spinner appears.

### Badges & Chips
- **Status badge:** a full pill, 12px medium text, padding 2px × 10px, the status colour as text over a 15% tint of itself. Always paired with a word.
- **Filter pills / segmented controls:** a Sunken Grey track; the selected segment is a Guardian Amber fill with On Amber text. They must expose `aria-pressed` or radio semantics, not colour alone.
- **Type tags:** the categorical `hue-*` text with a matching icon; singular nouns ("Container", not "CONTAINERS").

### Cards / Containers
- **Corner Style:** 12px.
- **Background:** Raised Paper on the page background; inner rows and wells use Sunken Grey.
- **Shadow Strategy:** none (see Elevation).
- **Border:** 1px Hairline; clickable cards move to amber at 40% on hover.
- **Internal Padding:** 14–16px for tiles and cards, 24px for dialog bodies; header rows use a bottom hairline.

### Inputs / Fields
- **Style:** Sunken Grey fill, 1px Hairline, 8px radius, 8px × 12px padding, 14px text, placeholder in Muted Ink. Visible label above in 14px medium; helper text below in 12px.
- **Focus:** the global accent outline.
- **Error:** a danger-coloured border and 12px danger message under the field, linked with `aria-describedby` and `aria-invalid` (being adopted, #688).
- **Checkboxes and radios:** native, tinted with `accent-vault`.
- **Switches:** a full-pill 36 × 20px track, amber when on; the off track needs a 3:1 boundary.

### Navigation
- **Sidebar item:** 14px medium, 8px radius, 20px outline icon plus label. Inactive items are Muted Ink, with Sunken Grey and Ink on hover. The active item gets a 10% amber tint, Amber Ink text and `aria-current="page"`, and its pill slides in over 200ms.
- **Section labels:** uppercase 12px eyebrows (PROTECT, RECOVER, SYSTEM).
- **Mobile:** a bottom tab bar of five icon-plus-label items, with the active item in Amber Ink. More opens a sheet listing the rest.

### Dialogs
One shell built on native `<dialog>` with `showModal()`. A Raised Paper panel with a 12px radius and the Overlay shadow, a header row with an 18px semibold title and a Close icon button, and a body that scrolls. It enters with a 250ms rise and fade on an ease-out-expo curve. The `sheet` placement (bottom-anchored, top corners rounded, backdrop click closes) is used by the mobile More menu; other dialogs, including the job wizard, stay centred on phones. Confirmations start focus on Cancel. Destructive and overwriting actions use typed confirmation (`PURGE`, `RESTORE`, `DELETE`) and list their consequences.

### Status Banner (signature)
The Dashboard opens with a single verdict banner: "All backups healthy", "Needs attention" or "Status unknown". It uses a tinted status surface, a status icon, a one-line reason and one action that links to the fix. It answers "am I protected?" before anything else. It announces only real changes of verdict, through its own polite live region.

### Motion
- Route content fades up 8px over 250ms (ease-out).
- Dialogs rise 16px from 98% scale over 250ms (`cubic-bezier(0.16, 1, 0.3, 1)`); the backdrop fades in over 200ms.
- List items stagger by 50ms each, capped at ten.
- Progress bars shimmer while running.
- Exits are faster than entrances (200ms ease-in).
- Under `prefers-reduced-motion`, every animation and transition completes instantly.

## Do's and Don'ts

### Do:
- **Do** answer "am I protected?" first on any status surface, using the Status Banner pattern.
- **Do** use Guardian Amber for actions and selected states, as Vault does today (The Amber Means Act or Chosen Rule).
- **Do** pair every status colour with a word or icon (The Status Speaks Twice Rule).
- **Do** use semantic tokens only, and check new UI in all five theme styles in light and dark (the Playwright contrast suite covers each).
- **Do** show exact values (paths, sizes, counts, times) in tabular figures, and wrap long paths and names rather than overflowing.
- **Do** keep surfaces flat with 1px hairlines, 8px control radius and 12px container radius.
- **Do** inherit Unraid's light or dark mode when embedded, until the user picks a Vault theme.
- **Do** meet WCAG 2.2 AA: 4.5:1 text, 3:1 for focus rings and control boundaries, 24px minimum targets (44px on touch), and visible labels.

### Don't:
- **Don't** use amber as decoration: not for headings, resting borders, icons, backgrounds or status meaning.
- **Don't** show green, "All clear" or a zero when data failed to load or is unknown.
- **Don't** put shadows on cards or add gradients or glassmorphism to surfaces. Backdrop blur is for the dialog backdrop only.
- **Don't** set text below 12px or in fixed pixel sizes.
- **Don't** use raw Tailwind palette classes or hex values in components.
- **Don't** use emoji as icons; use the 24px outline icon set with `aria-hidden` when decorative.
- **Don't** put white text on Guardian Amber.
- **Don't** add webfonts to the Default style; the system stack is the brand's typeface.
