---
name: Vault
description: The night-shift console for Unraid backups — flat, dense, and calm until something is wrong.
colors:
  signal-amber: "#f59e0b"
  signal-amber-deep: "#d97706"
  signal-amber-text: "#b45309"
  surface: "#ffffff"
  surface-2: "#f9fafb"
  surface-3: "#f3f4f6"
  surface-4: "#e5e7eb"
  surface-5: "#d1d5db"
  border: "#e5e7eb"
  border-hover: "#d1d5db"
  text: "#111827"
  text-muted: "#6b7280"
  text-dim: "#9ca3af"
  success: "#16a34a"
  danger: "#dc2626"
  warning: "#d97706"
  info: "#2563eb"
typography:
  display:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "1.5rem"
    fontWeight: 700
    lineHeight: 2rem
    letterSpacing: "normal"
  headline:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: 1.75rem
    letterSpacing: "normal"
  title:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "1rem"
    fontWeight: 500
    lineHeight: 1.5rem
    letterSpacing: "normal"
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.25rem
    letterSpacing: "normal"
  label:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: 1rem
    letterSpacing: "normal"
  label-section:
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 600
    lineHeight: 1rem
    letterSpacing: "0.05em"
rounded:
  md: "6px"
  lg: "8px"
  xl: "12px"
  full: "9999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "24px"
components:
  button-primary:
    backgroundColor: "{colors.signal-amber}"
    textColor: "#ffffff"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "8px 16px"
  button-primary-hover:
    backgroundColor: "{colors.signal-amber-deep}"
    textColor: "#ffffff"
  button-secondary:
    backgroundColor: "{colors.surface-3}"
    textColor: "{colors.text-muted}"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "8px 16px"
  button-secondary-hover:
    backgroundColor: "{colors.surface-4}"
    textColor: "{colors.text}"
  button-danger:
    backgroundColor: "{colors.danger}"
    textColor: "#ffffff"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "8px 16px"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.text-muted}"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "8px 16px"
  button-sm:
    typography: "{typography.label}"
    rounded: "{rounded.lg}"
    padding: "6px 12px"
  card:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.text}"
    rounded: "{rounded.xl}"
    padding: "20px"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "8px 12px"
  badge-neutral:
    backgroundColor: "{colors.surface-3}"
    textColor: "{colors.text-muted}"
    typography: "{typography.label}"
    rounded: "{rounded.full}"
    padding: "2px 10px"
  nav-item-active:
    backgroundColor: "rgba(245, 158, 11, 0.10)"
    textColor: "{colors.signal-amber}"
    typography: "{typography.body}"
    rounded: "{rounded.lg}"
    padding: "10px 12px"
---

# Design System: Vault

## Overview

**Creative North Star: "The Night-Shift Console"**

Vault is an instrument panel built for the session nobody wants to have. Most of the time it is glanced at — is it green, did it run, how much space is left — and it should answer in under two seconds from a phone in a dark room. Occasionally it is read with total attention, by someone whose array just died, who has never opened the Restore page before, and who is about to overwrite live data. The console has to serve both without changing character. It stays quiet, dense, and legible; it raises its voice only when something is actually wrong.

The material is flat. Depth comes from tonal layering — five stepped surface values and a hairline border — never from decoration. Type is small and functional: 14px body, 12px labels, and a single 24px page title per screen. Everything is a readout. There is exactly one accent, an amber that behaves like an indicator lamp rather than a brand color: it tells you where you are and what you would click next, and nothing else is permitted to borrow it. Because the whole surface is neutral, that one warm point of light does all the work.

Vault is also a guest. It lives inside Unraid's plugin chrome and must sit credibly next to Unraid's own pages in both light and dark. That rules out a house style loud enough to fight the host. The identity lives instead in precision: the border weight, the tonal steps, the restraint of the accent, and the honesty of the status vocabulary. On top of that core sits a separate personality layer — three optional retro renderings (1-bit, 8-bit, 16-bit) that reskin the same panel as older hardware. They are delight, not doctrine; the Default style is what new design work is measured against.

**Key Characteristics:**

- Flat by default — tonal layering and 1px borders carry depth, not shadow
- One accent (Signal Amber) with a strict semantic job; every other color is status or neutral
- Dense small type: 14px body, 12px label, one 24px title per page
- Eight valid renderings — 4 styles × light/dark — every component must survive all of them
- Status is a reading, never a mood
- Calm at rest; loud only when something is wrong

## Colors

A near-monochrome neutral field with a single warm indicator and a conventional four-color status vocabulary. Every value flips under `.dark`; the token name is the contract, the hex is not.

### Primary

- **Signal Amber** (`#f59e0b`): The only accent in the system. It marks orientation (active nav item, focus ring, brand mark) and intent (the one primary action on screen, in-flight progress). It is an indicator lamp — the eye should find it instantly because nothing else competes.
- **Signal Amber Deep** (`#d97706`): The pressed and hovered state of anything amber. Never used as a resting fill.
- **Readable Amber** (`#b45309`): Amber for small text, links, and captions **on light surfaces only** — `#f59e0b` fails contrast at body size on white, `#b45309` clears ~5.3:1. Under `.dark` and every retro style this token collapses back to the style's own accent, so those themes are untouched.

### Neutral

Five stepped surfaces plus two border weights and three text weights. Light values shown; each has a dark counterpart.

- **Console Surface** (`#ffffff` light / `#111827` dark): The page floor. The scroll region and the base of everything.
- **Panel** (`#f9fafb` / `#1f2937`): Every card, sidebar, modal, toast, and mobile bar. If it is a container, it is Panel.
- **Recess** (`#f3f4f6` / `#374151`): Inset and secondary fills — secondary buttons, neutral badges, hover backgrounds on nav and rows.
- **Recess Deep** (`#e5e7eb` / `#4b5563`): Hovered state of Recess; the deepest routinely used fill.
- **Rule** (`#d1d5db` / `#6b7280`): Scrollbar thumbs, dividers inside dense components, disabled fills.
- **Hairline** (`#e5e7eb` / `#374151`): The default 1px border on every panel, table cell, input, and divider. Present on essentially every container in the app.
- **Hairline Lit** (`#d1d5db` / `#4b5563`): The border on hover, before the amber affordance takes over.
- **Readout** (`#111827` / `#f9fafb`): Primary text — values, names, headings.
- **Readout Muted** (`#6b7280` / `#9ca3af`): Labels, secondary text, inactive nav, icon strokes.
- **Readout Dim** (`#9ca3af` / `#6b7280`): Timestamps, units, hints, placeholder text. The floor of legibility — nothing below this weight exists.

### Tertiary

The status vocabulary. These are readings, not decoration, and they are the only colors besides amber allowed to appear.

- **Verified Green** (`#16a34a` / `#22c55e`): A run that completed and verified. Never used for "saved", "enabled", or any non-backup success.
- **Failure Red** (`#dc2626` / `#ef4444`): Failed runs and destructive actions.
- **Drift Orange** (`#d97706` / `#f59e0b`): Warnings, anomalies, stale items, degraded-but-running. Deliberately adjacent to Signal Amber in hue — see the Warning Adjacency Rule.
- **Notice Blue** (`#2563eb` / `#3b82f6`): Neutral information, in-progress states with no health implication.

### Named Rules

**The One Amber Per Decision Rule.** On any screen, Signal Amber appears on exactly one primary action and on the orientation markers (active nav, focus ring, brand mark). If two things on screen are amber and both look clickable, one of them is wrong. Amber is not for emphasis, not for headings, not for icons, and not for decorating cards you want people to notice.

**The Readable Amber Rule.** Never render `#f59e0b` as text below 18px on a light surface. Use the `--color-vault-text` token, which resolves to `#b45309` on light and to the full accent everywhere else. This is a contrast requirement, not a preference.

**The Warning Adjacency Rule.** Drift Orange and Signal Amber are close in hue and cannot be told apart at a glance. Therefore a warning never relies on color alone: it carries an icon, a word, or a badge shape. This applies doubly under 1-bit, where every status color collapses to a single value.

**The Earned Green Rule.** Verified Green means a run completed _and_ its data verified. It may not be used to make a screen feel reassuring. An unverified or unknown state is neutral, not green.

## Typography

**Display / Body Font:** system UI stack (`-apple-system`, `BlinkMacSystemFont`, `Segoe UI`, `Roboto`, `Helvetica Neue`, `Arial`, sans-serif)
**Mono Font (1-bit style):** `ui-monospace`, `SF Mono`, `Cascadia Code`, `Fira Code`, `Consolas`, monospace
**Retro Fonts (8-bit / 16-bit styles only):** `VT323` for body, `Press Start 2P` for headings — self-hosted in the embedded bundle (SIL OFL 1.1), fetched by the browser only when a retro style actually selects the family

**Character:** Deliberately voiceless. The system stack means Vault renders in whatever the operator's OS already uses, so it reads as part of the machine rather than as a designed artifact — which is exactly right for a panel embedded in someone else's console. There is no display face and no web font in the Default style. Personality is carried by density and restraint, not by letterforms.

### Hierarchy

- **Display** (700, 1.5rem/24px, 2rem line): The page title. Exactly one per screen, top-left, no exceptions. This is the only type in the system that reads as a heading from across the room.
- **Headline** (600, 1.125rem/18px, 1.75rem line): Modal titles and empty-state titles. The heading level for anything that takes over the screen.
- **Title** (500, 1rem/16px, 1.5rem line): Card and section headers inside a page. Used sparingly — most cards label themselves with a Label instead.
- **Body** (400, 0.875rem/14px, 1.25rem line): The dominant size in the app by a wide margin. All values, table cells, form fields, button text, descriptions. If you are unsure what size something is, it is Body.
- **Label** (500, 0.75rem/12px, 1rem line): Field labels, badges, timestamps, units, metadata, mobile tab labels. The second most common size. Its high frequency is intentional — a console is mostly labelled readouts.
- **Section Label** (600, 0.75rem/12px, 0.05em tracking, uppercase): Sidebar group headers only (`Protect`, `Recover`, `System`). The one place uppercase is used in the Default style.

### Named Rules

**The One Title Rule.** One 24px Display per screen. Everything below it steps down to 18px or straight to 14px. Vault has no 20px, 30px, or 36px tier; adding one flattens the hierarchy rather than extending it.

**The Two-Size Rule.** 90% of the interface is 14px body and 12px label. Reach for anything else only for a page title, a modal title, or a single hero metric. Density is the point — an operator scanning nine pages should never scroll past whitespace to find a number.

**The No-Prose Rule.** Vault does not have a reading width. Nothing in the product surface is set as an article; the longest text is a two-line description under an empty-state title, capped at ~24rem. If a screen needs paragraphs, it belongs in the docs site, not here.

## Layout

**The shell.** A full-viewport flex row: a fixed 256px sidebar (`w-64`) and a single scrolling `<main>`. `html` and `body` are locked to `height: 100%; overflow: hidden` so `<main>` is the _only_ scroll surface in the application — this is deliberate and load-bearing. Without it a tall page produces a second scrollbar and scrolls the sidebar out of view.

**The content column.** Centered, capped at `max-w-6xl` (1152px), with `px-4` → `sm:px-6` → `lg:px-8` gutters and `py-6` vertical padding. Content never runs edge-to-edge on a wide monitor, and never gets narrower than the gutter on a phone.

**The one breakpoint.** `lg` (1024px) is the only structural breakpoint in the system. Above it: persistent sidebar, no top bar. Below it: a fixed 56px top bar with the brand and a hamburger, plus a fixed bottom tab bar of five destinations. `sm` (640px) exists only to step gutter padding. Everything else is fluid.

**Density and rhythm.** The spacing scale is 4/8/12/16/24px. Component internals cluster tightly at 8–12px (`gap-2`, `gap-3`, `px-3 py-2`); containers breathe at 16–24px (`p-5`, `px-6 py-4`). Cards in a grid use `gap-3`/`gap-4`. Vertical rhythm between page sections is 24px (`mb-6`), occasionally 32px (`mb-8`) before a major break.

**Touch.** Every interactive target below `lg` carries `min-w-[44px] min-h-[44px]`. The bottom tab bar pads with `pb-[max(0.5rem,env(safe-area-inset-bottom))]` so it clears the home indicator on notched phones. The mobile shell reserves `pt-14 pb-16` on `<main>` for the two fixed bars.

### Named Rules

**The Single Scroll Rule.** `<main>` scrolls; nothing else does. Never introduce a second scroll container in the page body, and never remove `overflow: hidden` from `html`/`body`.

**The One Breakpoint Rule.** Design for two layouts — desktop with a sidebar, and mobile with two fixed bars. Do not invent a tablet tier. If a component needs a third arrangement, it is probably two components.

## Elevation & Depth

The system is flat. Depth is built from five stepped tonal surfaces and a 1px hairline border, and this carries virtually the entire interface: cards, tables, inputs, sidebar, and mobile bars are all flat panels distinguished by tone and edge, not by lift.

Shadow has exactly two jobs. First, **detachment**: anything genuinely floating above the document — modal panels, toasts, popovers — casts a shadow to say so, paired with a `black/60 backdrop-blur-sm` scrim in the modal case. Second, **interactive lift on hover**: a card or row may take a subtle shadow on hover alongside its border shift, to acknowledge the pointer. Nothing at rest in the document flow is ever shadowed.

Under the retro styles, shadow is abolished entirely and replaced by drawn borders — a 2px inset pixel bevel at 8-bit, a 3px double-stroked RPG dialog frame at 16-bit. Depth becomes literal drawn geometry.

### Shadow Vocabulary

- **Detached** (`box-shadow: 0 25px 50px -12px rgb(0 0 0 / 0.25)` — Tailwind `shadow-2xl`): Modal panels and command palette. The heaviest shadow in the system, used only for full takeovers.
- **Floating** (`box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.1), 0 4px 6px -4px rgb(0 0 0 / 0.1)` — `shadow-lg`): Toasts and popovers. Present but not dramatic.
- **Lift** (`box-shadow: 0 1px 2px 0 rgb(0 0 0 / 0.05)` — `shadow-sm`): Hover response on interactive cards and rows, alongside the border-color shift. Optional; the border shift alone is also correct.

### Named Rules

**The Flat-At-Rest Rule.** If it is in the document flow and nobody is pointing at it, it has no shadow. Depth at rest comes from `bg-surface-2` + `border-border`. A shadow on a resting card is the single fastest way to make Vault look like a marketing dashboard.

**The Border-First Hover Rule.** The primary hover affordance for a card is `hover:border-vault/40` — the hairline warms toward amber. A `shadow-sm` lift may accompany it. Reaching for scale or translate transforms on cards is not part of this system.

## Shapes

Rectilinear with softened corners, and a hard split between containers and content.

- **12px (`rounded-xl`)** is the container radius: cards, panels, modals, tiles. Any bordered box that holds other things.
- **8px (`rounded-lg`)** is the control radius and the most-used value in the codebase: buttons, inputs, selects, nav items, icon buttons, the skip link. Any single interactive element.
- **9999px (`rounded-full`)** is reserved for status badges, dots, indicators, and avatars — things that read as tokens rather than surfaces.
- **6px (`rounded-md`)** is a rare inner-detail radius. Do not introduce it as a new tier.

Borders are 1px hairlines by default, present on essentially every container. Border color is a state channel: `border-border` at rest, `border-border-hover` on generic hover, `border-vault/40` when the element is a choice you can make.

Icons are inline 24×24 stroke SVG paths — no icon library, no icon font, no sprite sheet. Stroke width is 1.5 for navigation and decorative icons, 2 for action and alert icons. Rendered sizes are 20px (`w-5 h-5`) in nav and headers, 16px (`w-4 h-4`) inline with text.

### Named Rules

**The Two-Radius Rule.** Containers are 12px, controls are 8px, badges are pills. There is no third container radius. Do not add `rounded-2xl`; uniform heavy rounding is exactly the generic-app-shell look this system rejects.

**The Drawn-Icon Rule.** Icons are inline SVG path data committed in the component. No package import, no CDN, no `<img>`. The product must render identically on a machine with no internet.

**The Offline Parity Rule.** Every asset the interface needs — fonts, icons, images, stylesheets — ships inside the embedded bundle and is served by the daemon. A Vault install on an air-gapped Unraid box must be visually indistinguishable from one with internet. If a design decision requires fetching something at runtime, the decision is wrong.

## Components

### Buttons

Compact, flat, 8px radius, no shadow, `transition-colors` only. All variants share `px-4 py-2 text-sm font-medium` with `disabled:opacity-50 disabled:cursor-not-allowed`.

- **Primary** (`.btn-primary`): Signal Amber fill, white text, deepens to `#d97706` on hover. One per screen — see the One Amber Per Decision Rule.
- **Secondary** (`.btn-secondary`): Recess fill, muted text; deepens to Recess Deep with full-strength text on hover. The default for everything that isn't the primary move.
- **Danger** (`.btn-danger`): Failure Red fill, white text, 90% opacity on hover. Destructive confirmations only, and never the auto-focused control in a dialog.
- **Ghost** (`.btn-ghost`): No fill; muted text that gains a Recess background on hover. Table row actions, dismissals, tertiary moves.
- **Icon** (`.btn-icon`): 8px square padding, muted stroke, Recess background on hover. Below `lg` it must still meet the 44px touch minimum.
- **Small** (`.btn-sm`): `px-3 py-1.5 text-xs`. For dense rows and table cells only.

### Cards / Panels

- **Corner Style:** 12px (`rounded-xl`)
- **Background:** Panel (`bg-surface-2`)
- **Border:** 1px Hairline — always present, not optional
- **Shadow Strategy:** None at rest. `shadow-sm` permitted on hover for interactive cards.
- **Internal Padding:** 20px (`p-5`) for standard cards, 14px (`p-3.5`) for dense tiles, 0 for cards that wrap a table (the table owns its own cell padding)
- **Interactive variant:** `cursor-pointer hover:border-vault/40 transition-colors`, and a `min-h-[104px]` floor so a grid of tiles stays even when content lengths differ

### Inputs / Fields

- **Style:** Console Surface fill, 1px Hairline border, 8px radius, `px-3 py-2 text-sm`
- **Focus:** The global 2px Signal Amber outline at 2px offset. Inputs do not get a bespoke focus treatment.
- **Disabled:** 50% opacity, not-allowed cursor
- **Error:** Failure Red border plus a 12px Failure Red message below the field. Never color alone.

### Badges

Pill-shaped status tokens: `px-2.5 py-0.5 text-xs font-medium rounded-full`, filled at 15% of the status color with the status color as text (`bg-success/15 text-success`). Variants: success, danger, warning, info, vault, neutral. Under the retro styles they become square with a 1px `currentColor` border.

### Navigation

- **Desktop sidebar** (256px, Panel, right hairline): brand block at top, then nav grouped under Section Labels (`Protect` / `Recover` / `System`) with Dashboard sitting ungrouped above them. Items are 8px-radius rows at `px-3 py-2.5 text-sm font-medium` with a 20px icon. Inactive: muted text, Recess on hover. Active: `bg-vault/10 text-vault` plus a 200ms `scaleX(0.92) → 1` pill animation and `aria-current="page"`.
- **Mobile** (below 1024px): a fixed 56px top bar with the brand and a hamburger that expands the full nav, plus a fixed bottom tab bar of five destinations. Active tab is amber with a 4px amber dot beneath the icon; label is 12px.
- **Empty sections never render their header** — a group header only appears when at least one child is visible, so feature flags and replica mode can hide routes without leaving orphaned labels.

### Modals

Fixed inset overlay: `bg-black/60 backdrop-blur-sm` scrim fading in over 200ms, panel rising 16px with a `scale(0.98) → 1` on a `cubic-bezier(0.16, 1, 0.3, 1)` curve over 250ms. Panel is a Panel-toned card with 12px radius, `shadow-2xl`, `max-h-[90vh]`, and a three-part flex column: a bordered header with an 18px title and a close button, an optional bordered stepper strip, and a scrolling body at `px-6 py-4`. Widths: `max-w-lg` default, `max-w-2xl` large. Focus is trapped and Tab cycles within; Escape closes; the first focusable element is auto-focused on open.

### Empty States

Centered, `py-16`, with the icon at 30% opacity above an 18px muted title, an optional 12px dim subtitle, a 14px dim description capped at 24rem, and — only when there is a genuine next step — a single amber action button. Vault's empty states are quiet and factual. They do not illustrate, apologize, or celebrate.

### Signature Component: The Style Layer

Vault ships four visual styles (`default`, `1bit`, `8bit`, `16bit`) on an axis independent of light/dark mode, giving eight valid renderings. The style class lands on `<html>` as `.theme-{style}` alongside `.dark`. The retro fonts are self-hosted `@font-face` rules in `app.css`, so the browser fetches them only when a retro style's CSS actually selects the family — the Default and 1-Bit styles download zero font bytes, and no style reaches the network.

Each retro style overrides the same token set and then applies a shared base layer that squares every radius, removes every shadow, draws borders on all four sides of tables and cards, uppercases buttons and table headers, and switches the focus ring to a 0-offset square outline. On top of that: 1-bit is pure monochrome in a system mono face with zero effects; 8-bit is a NES palette with `VT323` body text, `Press Start 2P` headings, `image-rendering: pixelated`, and a 2px inset pixel bevel on cards; 16-bit is a SNES-era RPG treatment with 3px double-stroked dialog frames, gradient panel title bars, a gradient cursor-style nav highlight, and a fixed 3% scanline overlay.

**This layer is personality, not doctrine.** Design new work against the Default style. But because the retro base layer neutralizes radius, shadow, and proportional type wholesale, a component that _depends_ on any of those to be understandable will break there — which is a useful signal in its own right.

### Motion

Motion is short, single-purpose, and never decorative. Route changes fade content up 8px over 250ms `ease-out`. Lists stagger children at 50ms intervals up to ten items, then stop. Modal panels use `cubic-bezier(0.16, 1, 0.3, 1)` over 250ms; backdrops fade over 200ms; toasts leave with a 200ms `ease-in` slide-out. In-flight progress bars run a 1.5s amber shimmer — the only looping animation in the product, and it must never run when nothing is actually transferring.

`prefers-reduced-motion: reduce` collapses every animation and transition app-wide to 0.01ms rather than removing them. This is deliberate: entrance keyframes start at `opacity: 0`, so `animation: none` would strand staggered content permanently invisible.

## Do's and Don'ts

### Do

- **Do** use `bg-surface-2 border border-border rounded-xl` for every card. It is the single most repeated pattern in the codebase and it is correct.
- **Do** keep one Signal Amber primary action per screen, and let everything else be Secondary or Ghost.
- **Do** use `--color-vault-text` for amber text under 18px, so light mode stays above 4.5:1.
- **Do** pair every status color with an icon, a word, or a badge shape, so the reading survives 1-bit and color-vision deficiency.
- **Do** set body copy at 14px and labels at 12px. Density is the design.
- **Do** give every interactive element below `lg` a 44px minimum touch target.
- **Do** hover cards with `hover:border-vault/40` — the hairline warming toward amber is the house affordance.
- **Do** commit icons as inline SVG path data. Nothing in the product may fetch an asset at runtime.
- **Do** keep `<main>` as the only scroll surface.
- **Do** show unknown and unverified states as neutral. Silence is more honest than green.

### Don't

- **Don't** put a shadow on a resting card, a gradient on a stat tile, or an illustration in an empty state. That is the consumer-SaaS dashboard look, and it is the wrong genre for a backup tool.
- **Don't** add a second 24px+ heading to a page, or invent a size between 14px and 18px. One title, two working sizes.
- **Don't** use amber for emphasis, headings, decorative icons, or "look at this" highlights. It means orientation or primary intent, and its rarity is the entire mechanism.
- **Don't** show Verified Green for anything that hasn't actually verified, and never ship an "All Systems Operational" style banner. Fake calm is the one unforgivable error in a backup product.
- **Don't** stack toolbars, tabs-within-tabs, or grey-on-grey chrome to fit more in. That is the enterprise-console failure mode: dense without hierarchy.
- **Don't** introduce `rounded-2xl`, glassmorphic panels, blurred blob backgrounds, indigo/violet accents, or emoji in section headers. Vault has one accent and two radii.
- **Don't** load a web font, icon package, or any asset from a CDN, in any style. Everything ships in the embedded bundle — see the Offline Parity Rule.
- **Don't** auto-focus a destructive button in a confirmation dialog, or let a restore proceed without naming exactly what will be overwritten.
- **Don't** add a tablet breakpoint. `lg` is the only structural one.
- **Don't** animate anything that isn't communicating a state change, and never loop an animation while the underlying process is idle.
