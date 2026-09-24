---
name: space-elevator-design
description: Design system for the space-elevator dashboard and any app that should look like it. Use when building or restyling HTML/CSS UI for this project or sibling apps — colors, type, layout, components, motion, icons, copy voice, and a copy-paste starter template.
---

# Design skill — "night-launch console"

The house style of **space-elevator**, reusable for any of my apps. One
sentence of identity:

> **Grotesk = chrome · Mono = data · amber = action.**

Dark launch-console aesthetic: near-black blue-tinted background, thin
hairline borders, a single amber accent reserved for *the thing you can
do*, green/red/yellow only for *state*. Everything is quiet until you
interact with it.

Source of truth in this repo: `internal/web/static/css/app.css` (all
tokens and components), `internal/web/pages/*.html` (markup recipes).

## 1. Color tokens

Copy this `:root` block into any new app verbatim. Never invent colors
outside this palette; use the semantic role, not the hue.

```css
:root {
  --bg: #0B101B;          /* page background */
  --surface: #121A2A;     /* cards, panels, inputs */
  --surface-2: #182236;   /* raised/hover surface, code chips */
  --well: #070B13;        /* recessed wells: logs, terminal output */
  --line: #263350;        /* strong border: controls, dividers */
  --line-soft: #1C2740;   /* quiet border: panels, table rules */
  --ink: #E8EDF7;         /* primary text */
  --muted: #93A0BC;       /* secondary text */
  --faint: #5D6B8A;       /* tertiary: labels, hints, placeholders */
  --amber: #FFB224;       /* THE action color: primary buttons, links, focus, current nav */
  --amber-soft: #FFC24D;  /* amber hover */
  --amber-ink: #201503;   /* text on amber */
  --ok: #41D189;          /* running / success */
  --warn: #E9B44C;        /* partial / caution */
  --bad: #EF6E6B;         /* error / destructive */
  --info: #79B8FF;        /* pending / neutral info */
  --radius: 10px;
  --sans: "Space Grotesk", system-ui, sans-serif;
  --mono: "IBM Plex Mono", ui-monospace, monospace;
  color-scheme: dark;
}
```

Rules:

- **Amber is scarce.** One primary amber button per view; everything
  else is `ghost`/`subtle`. Amber = "this is the action." If two things
  are amber, neither is.
- Semantic colors (`--ok`/`--warn`/`--bad`/`--info`) describe **state
  only**, never decoration. Destructive actions use `--bad`, not amber.
- Tinted backgrounds are always the semantic color at ~7–12% alpha, e.g.
  `rgba(255, 178, 36, 0.07)`, `rgba(239, 110, 107, 0.1)`.
- `theme-color` meta and favicon background = `#0B101B`.

## 2. Typography

Self-hosted woff2, `font-display: swap` — never a webfont CDN.

| Role | Font | Spec |
|---|---|---|
| Body | Space Grotesk 400 | `14px/1.55` |
| H1 | Space Grotesk 700 | `23px/1.2`, `letter-spacing: -0.01em` |
| H2 (section/panel title) | Space Grotesk 500 | `15px/1.3`, color `--muted` |
| Labels, buttons, nav | Space Grotesk 500 | `13.5px/1` |
| Hints, table headers, micro | Space Grotesk 400/500 | `12–12.5px`, color `--faint` |
| Data (values, dates, domains, logs, code) | IBM Plex Mono 400/500 | `12.5px/1.6`, color `--muted` |

The split is the whole identity:

- **Sans = chrome**: anything the *app* says — labels, buttons, nav,
  headings, prose.
- **Mono = data**: anything *the machine* says — timestamps, refs,
  domains, env values, logs, IDs, file paths, `code`.

Apply with a `.mono` class or `font-family: var(--mono)`. Inputs use
mono (`13.5px/1.4`) because they mostly take data; search inputs use
sans. Mobile: H1 drops to `20px`.

## 3. Layout & spacing

- Page containers: `.container` `max-width: 1080px`, padding
  `32px 28px 64px`; `.narrow` (forms/wizard) `max-width: 600px`.
- Top bar: sticky, `height: 56px`, `padding: 0 28px`, bottom hairline
  `--line-soft`, background `rgba(11,16,27,0.85)` + `backdrop-filter:
  blur(8px)`, `z-index: 20`.
- Page header: `.page-head` — flex row, H1 + `.page-sub` (13.5px
  `--faint`) on the left, `.row-actions` buttons right, `margin-bottom:
  28px`, wraps on narrow.
- Panels: `--surface` bg, `1px --line-soft` border, `--radius` (10px),
  padding `18px 20px`. Detail pages: grid `minmax(0,1fr) 340px`, gap
  `16px`.
- Spacing rhythm (gaps/padding): 7 · 8 · 10 · 12 · 14 · 16 · 18 · 20 ·
  28. No arbitrary values.
- Radius ladder: **10px** containers (panels, tables-as-cards, modal,
  dropzone) → **8px** controls (buttons, inputs, nav items) → **5–6px**
  chips (code, copy button) → **999px** pills (badges, step chips) →
  **14px** auth card.
- Borders, not shadows: depth comes from `--line`/`--line-soft`
  hairlines. Shadows appear only on floating toasts
  (`0 12px 32px rgba(0,0,0,.45)`) and the modal backdrop.

## 4. Components (recipes)

### Buttons

Base (all buttons are amber by default): `padding: 8px 15px`,
`border-radius: 8px`, `font: 500 13.5px/1 var(--sans)`, inline-flex
with `gap: 7px` icon.

| Variant | Look | Use |
|---|---|---|
| *(default)* | amber bg, `--amber-ink` text | the single primary action |
| `.ghost` | transparent, `1px --line`, `--ink` text | secondary (Cancel) |
| `.danger` | transparent, `--line`, `--bad` text; hover = red tint + red border | destructive confirm |
| `.subtle` | invisible (transparent border/text `--muted`); hover reveals `--surface-2` bg | dense table row actions |
| `.sm` | `padding: 6px 11px`, `13px`, icon 14px | inside tables/toolbars |
| `.link` | no chrome, `--amber` text, underline on hover | tertiary inline action |

- Hover: default → `--amber-soft`; ghost → `--surface-2` +
  border `--faint`.
- Disabled: `opacity: 0.55` (`.5` + `cursor: wait` when submitting).
- **Busy state**: `aria-busy="true"` hides the icon and shows a 12px
  `currentColor` spinner `::before`, keeps the label. Use `data-busy` on
  forms to trigger it.
- Icon buttons always keep a **visible text label**; tooltips
  (`data-tip`) are enhancement only.

### Forms

```html
<form class="form">            <!-- grid, gap 18px -->
  <label>Name                  <!-- 13.5px/500, grid gap 7px -->
    <input type="text" ...>
    <span class="hint">…</span><!-- 12.5px --faint -->
  </label>
</form>
```

Inputs/selects/textarea: `--surface` bg, `1px --line`, radius 8px,
mono 13.5px, `padding: 9px 12px`. Focus: **border `--amber` +
background `--surface-2`** (no outline). Placeholder `--faint`.
Readonly: `--muted` text + *dashed* border. Two-column fields:
`.form-grid-2` (`1fr 1fr`, gap 12px, collapses at 720px). Optional
groups live in `details.advanced` (dashed `--line` border).

Choice cards (wizard/radios): `.choice` — `--surface-2` bg, `1px
--line`, radius 8, cursor pointer; hover border `--faint`;
**checked: border `--amber` + `rgba(255,178,36,.07)` bg** (via
`:has(input:checked)`), `accent-color: var(--amber)`. Title 13.5/500 +
description 12px `--muted` inside `.choice-body`.

### Status

Lamp (state dot + word, always both):

```html
<span class="lamp lamp-running"><i></i>running</span>
```

`8px` round dot, mono 12.5px `--muted` text. States:
`running` = ok green, dot glows + `breathe 2.6s`; `stopped` = faint
static; `partial` = warn; `error` = bad, glowing; `pending` = info,
fast breathe 1.2s.

Badges (pill, `--surface-2` bg, `1px --line-soft`, radius 999px,
12px/11.5px, padding `3px 9–10px`): neutral = `--muted`; tinted
variants pair a semantic text color with a 35–40% alpha border of the
same hue (`.src-git` amber, `.src-drop` info blue, `.kind-function`
green, `.kind-custom` amber). One-line, `white-space: nowrap`.

Flash / toast: left `3px` accent bar, tinted bg, 13.5px, radius 8 —
`.flash` (bad) and `.flash-ok`. JS promotes it to a fixed toast (top
`68px` right `20px`, `flash-in 0.18s`, auto-dismiss). `role="alert"`,
dismissible.

### Overlays

- Modal: `<dialog class="modal">`, `min(440px, 100vw-32px)`, `--surface`
  bg, radius `--radius`, backdrop `rgba(4,7,13,.65)` + blur 2px.
  Actions right-aligned (`.modal-actions`, gap 8): ghost Cancel +
  amber Confirm. `.confirm-danger` = red border + red title.
- Tooltip: `[data-tip]::after`, `--surface-2` bg, `1px --line`, radius
  6, 12px, fades in above the element in `0.12s`. Disabled on mobile.

### Data displays

- Table: `th` 12.5/500 `--faint`, bottom hairline `--line-soft`; `td`
  padding `13px 14px`, hairline rows, hover
  `rgba(24,34,54,.5)`. Row actions right-aligned, `.subtle` buttons,
  `.sep` hairline divider before destructive. **At ≤720px each row
  becomes a card** with `td::before { content: attr(data-label) }`.
- Key/value: `dl.kv` — 150px label col (`--faint` 13px) / mono 12.5px
  values (`overflow-wrap: anywhere`), links amber.
- Meta strip: `--surface` card, flex wrap, each cell = tiny `--faint`
  key (12px) over value (13px).
- Logs: recessed `--well` block, radius 8, padding `14px 16px`, mono
  `12.5px/1.6`, color `#B9C4D8`, `white-space: pre-wrap`, custom
  hairline scrollbars.
- Progress steps: pill chips, mono 12px, dot before label — pending =
  faint, **active = amber border/tint + breathing amber dot**, done =
  muted text + green dot.
- Inline code: `--surface-2` bg, `1px --line-soft`, radius 5, mono
  0.92em, `overflow-wrap: anywhere`.
- Copy button: `.copy-btn` — 12px, `--surface-2` bg, hairline border;
  copied state = green text/border.
- Dropzone: dashed `1.5px --line`, radius 10, centered icon + title +
  hint; hover border `--faint`; drag-over = amber border +
  `rgba(255,178,36,.06)` bg, icon amber. `.compact` variant (row,
  small) once content exists so lists stay above the fold.

### Page shells

- **Empty state** (`.empty-orbit`): two-column card — themed headline
  (19px/700), one-sentence body (`--muted`, max 46ch), primary button,
  and a decorative line-art SVG (stroke `currentColor`/token vars,
  dashed orbit, single amber element) on the right. Collapses to one
  column on mobile.
- **Auth** (`main.auth`): full-viewport centered, card `max-width:
  380px`, radius 14px, padding `34px 32px 30px`, centered glyph + H1
  20px + muted sub, full-width primary button.
- **Nav**: brand (glyph 18px with amber accent + name 15/500) left;
  topnav right — items `13.5/500` `--muted`, hover `--ink` +
  `--surface-2`, **current page = amber, no background**. ≤720px: nav
  hidden, fixed bottom bar (4 columns, blurred bg, icon-over-label
  11px, safe-area padding), current = amber.

## 5. Motion & focus

Motion is quiet and purposeful: it confirms an interaction, never
decorates. Two speeds — **`0.13s`** for state changes, **`0.16–0.22s`**
for entrances.

- Micro-transitions `0.12–0.15s` on border/background/color, plus
  **transform** for direct interaction feedback (button lift/press) and
  **opacity** for state swaps. Nothing else animates on hover.
- Entrances (modal, toast, route, rows) are keyframes using opacity +
  a small transform; ambient loops use `breathe`.
- `spin 0.7–0.9s linear infinite` for spinners; `breathe
  0↔0.35 opacity` (2.6s slow / 1.2s urgent) for live states.
- Toast: `translateY(-8px)` + fade in `0.18s ease-out`; dismiss
  `0.18s`.
- `@media (prefers-reduced-motion: reduce) { * { transition: none
  !important; animation: none !important; } }` — keep this verbatim.
  Every interaction below stays usable with motion off.
- Focus-visible: `2px solid var(--amber)`, offset 2px, radius 4px.
- `::selection { background: rgba(255,178,36,.28) }`.

### Microinteractions

Every interactive affordance gets one, while flatness is preserved: no
shadows, no gradients, no bounce.

| Element | Behaviour |
|---|---|
| Buttons (not `.subtle`/`.link`) | hover lifts `translateY(-1px) scale(1.02)`; press returns to `translateY(0)` |
| Dense row actions (`.subtle`) | colour only — never lift, so tables stay calm |
| Inputs / selects | hover border `--faint`; focus border `--amber`; `:user-invalid` border `--bad` only after engagement |
| Table rows | background tint + a 2px left hairline arriving via `border-color` |
| Row entrance | `row-in` keyframe, `20ms × row index`, capped at 12 |
| Modal | panel + backdrop fade in together (`0.16s`); panel lifts 8px |
| Toast / flash | enters from `-8px`; exits the same way on dismiss |
| Copy button | glyph crossfades Copy→Check, green, resets after 1.6s |
| Nav | current page gets an amber underline; hover shows a faint one |
| Empty state | satellite dot drifts the orbit (28s); the amber core breathes |

**Press is a state, not a flourish:** it resolves immediately on
`:active`; only the lift is transitioned.

## 6. Icons

Inline SVG only (no icon font/CDN):

- `viewBox="0 0 24 24"`, `fill="none"`, `stroke="currentColor"`,
  `stroke-width="1.7"` (2 for tiny 13px badge icons),
  `stroke-linecap/linejoin="round"` — Feather/Lucide geometry.
- Sizes: 13px badges · 14–15px buttons/nav (`.ico`) · 19–20px dropzone,
  mobile nav. Set explicit `width/height`; `flex: none`.
- `aria-hidden="true"` unless the icon is the only label.
- Brand glyph is custom line art with **one amber-filled shape**.

## 7. Voice & copy

- Sentence case, terse, operational: "New app", "Sign out", "Filter by
  name, source, or domain".
- Space metaphor **sparingly** and only in headline/empty moments:
  "Nothing in orbit yet", "quick deploy". Never in errors or form
  labels — those stay literal.
- Hints explain consequences, not syntax: "never shown again",
  "apply on the next redeploy".
- Empty states: playful headline + one actionable sentence + button.
- Errors state what failed and the next step; use the flash banner.

## 8. Accessibility floor

- Every input has a `<label>` (or `aria-label`); icon-only controls
  get `aria-label`; status is never color-only (lamp = dot **+ word**).
- `color-scheme: dark`, visible amber focus ring, `role="alert"` on
  flashes, `aria-busy` on in-flight buttons, `aria-current`/`.current`
  on active nav.
- Tooltips enhance but never replace visible labels.
- Respect safe-area insets for fixed bottom nav/toasts.

## 9. Do / Don't

**Do** use tokens everywhere; hairline borders for depth; amber once
per view; mono for machine data; label + icon on buttons and navigation
when the icon clarifies the destination/action; dashed borders for
"empty/editable" affordances; responsive card tables; a microinteraction
on every interactive affordance.

**Don't** add new hex values, gradients, light mode, heavy shadows,
unlabeled icon-only buttons, amber on non-interactive text (links only),
mono in prose, or animations without a reduced-motion escape hatch.
Don't lift `.subtle` row actions, and don't transition anything except
colour/border/background/opacity/transform.

## 10. Starter template (new app)

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="theme-color" content="#0B101B">
  <title>page · app-name</title>
  <link rel="stylesheet" href="/static/css/app.css">
</head>
<body>
  <header class="topbar">
    <a href="/" class="brand"><!-- 18px glyph w/ amber accent -->app-name</a>
    <nav class="topnav"><!-- items; current gets class="current" --></nav>
  </header>
  <main>
    <section class="container">
      <header class="page-head">
        <div><h1>Title</h1><p class="page-sub">Optional subtitle.</p></div>
        <div class="row-actions"><a class="btn" href="…">Primary</a></div>
      </header>
      <!-- panel / table / form here -->
    </section>
  </main>
</body>
</html>
```

Ship: `app.css` = `:root` tokens (§1) + base styles + only the
components you use (copy recipes from §4 verbatim), self-hosted
Space Grotesk (400/500/700) + IBM Plex Mono (400/500) woff2 files.

## 11. Review checklist

- [ ] Only §1 token colors; amber on ≤1 primary action; semantics =
      state only.
- [ ] Sans = chrome, mono = data; type scale matches §2.
- [ ] Containers 1080/600px; radius ladder 10/8/5/999; hairline
      borders; spacing from the §3 rhythm.
- [ ] Buttons carry labels + 1.7-stroke inline SVG; busy/disabled
      states present; subtle variants in dense tables.
- [ ] Inputs focus to amber border + raised bg; hints on every field.
- [ ] Status shown as lamp (dot + word); badges pill-shaped and tinted
      per §4.
- [ ] Empty/auth/modal/dropzone match §4 shells; copy voice per §7.
- [ ] Focus ring, reduced-motion block, labels/aria per §8.
- [ ] Microinteractions per §5: buttons lift, rows stagger, current nav
      is underlined, forms acknowledge hover/focus/invalid states, and
      copy/toast/modal transitions resolve cleanly.
- [ ] ≤720px: bottom nav, card tables, collapsed grids, no tooltips.
