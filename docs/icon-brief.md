# The Moment — Icon Design Brief for ChatGPT

## How to use this document

Read all sections before generating anything. Present **five numbered concept options** as short descriptions (no images yet). Wait for the user to pick a number, then ask any clarifying questions before generating the final image. After the user approves a direction, generate the final icon and verify it against the **Technical Constraints** checklist at the end of this document before delivering it.

---

## What is The Moment?

**The Moment** is a self-hosted Go microservice that acts as the missing link between 3D printers and filament inventory. It runs 24/7 on a home server and does the work no one wants to do manually:

- Watches multiple 3D printers (Prusa, Ender, Bambu) for print start/finish events
- Logs every print to a history database with duration, filament used, and cost
- Deducts filament weight from spools in Spoolman (open-source filament inventory)
- Assigns physical spools to specific printer toolheads via NFC tag scanning on iPhone
- Calculates per-print cost and lifetime filament spend
- Syncs spool locations bidirectionally with Spoolman

The tagline is: **"The missing link between printers, filament inventory, and cost."**

The four sections of the app (reflected in navigation tabs) are:
- **Print History** — "Relive the Moment"
- **Print Ops** — "Shape the Moment"
- **NFC** — "Define the Moment"
- **Settings** — "Configure the Moment"

The name "The Moment" captures the precise instant a print starts or finishes — that inflection point where time, material, and cost converge. It also echoes the developer's philosophy: track every moment that matters, waste nothing.

---

## Visual Design Language

The UI uses a **dark purple/violet theme** throughout. Every surface, gradient, and accent is drawn from this palette — nothing clashes with it.

### Colour Tokens

| Role | Hex | Usage |
|---|---|---|
| `--brand-deep` | `#1a0e3d` | Darkest purple; body background gradient end |
| `--brand-mid` | `#2d1b69` | Page background gradient start |
| `--brand` | `#6c3aaa` | Header gradient start; primary accents |
| `--brand-bright` | `#7c5cfc` | Bright violet; interactive elements, highlights |
| `--brand-light` | `#c8b8ff` | Pale lavender; secondary text, glows |
| `--text-primary` | `#f0ecff` | Near-white with purple tint; headings |
| `--surface-1` | `#16131f` | Card surfaces |

### Where the logo lives

The logo sits in the **page header** — a horizontal bar with a diagonal gradient from `#6c3aaa` (top-left) to `#1a0e3d` (bottom-right). It is displayed at **52 px tall** (width auto) on the left side of the header, next to the text "The Moment Dashboard" in a thin-weight Inter font at ~2.5em. The header is full-width, rounded at the top corners (15 px radius).

The logo must look **native on this dark purple gradient** — not bolted on. It should feel like it belongs to the same design family as the rest of the UI.

### Style reference

- Typography: Inter, system-ui — geometric, clean, modern
- Corners: rounded throughout (15 px containers, 8–10 px buttons)
- Aesthetic: dark-mode-first, tech-precision, not game-y or retro
- No drop shadows, no bevels, no glossy 3D effects
- Thin strokes and geometric forms preferred over heavy filled shapes
- Gradients are welcome — use the brand palette

---

## What the icon should communicate

The icon does not need to include the text "The Moment" — that appears next to it as a heading.

The icon should evoke **one or more** of:

1. **A filament spool** — the physical object being tracked; the raw material of every print
2. **A moment in time** — the precise instant of a print event; a timestamp, a flash, a snapshot
3. **Connection / bridging** — linking printers to inventory; the "missing link" idea
4. **Precision / measurement** — cost tracking, weight deduction, data logging
5. **NFC / wireless** — physical tags that identify spools in the real world (a subtle, not dominant, idea)

It should feel like a **product icon** — clean enough to work at 52 px, distinctive enough to be recognisable at a glance.

---

## Five Concept Options

Present these five options to the user as numbered descriptions. Do not generate images until the user picks a number and you have confirmed any open questions.

1. **Spool + Moment mark** — A filament spool viewed face-on, rendered in thin-stroke geometry. The spool's spindle is replaced by a single bold clock hand (or a simple radial arc) pointing at the "12 o'clock" position, suggesting a precise moment frozen in time. Brand violet (`#7c5cfc`) spool body, pale lavender (`#c8b8ff`) accent arc, transparent background.

2. **Layered M monogram** — A bold "M" whose strokes are drawn as stacked print layers — each stroke is a horizontal band, slightly offset, like cross-sections of a 3D print. The bottom-most layer glows faintly violet as if freshly deposited. Clean geometric letterform, transparent background, white-to-lavender gradient strokes on the dark-purple header.

3. **Filament node bridge** — A minimal two-node diagram: on the left, a printer cube icon; on the right, a spool circle; connected by a single arc of filament that forms a link or chain-link shape. The arc glows with the brand-bright violet. Transparent background, thin-stroke, icon-grid proportions.

4. **Precision drop** — A single filament droplet or nozzle-tip shape, perfectly circular at top and tapering to a point at the bottom, like a cross-section of a nozzle extruding at the exact right moment. Inside the droplet, a tiny crosshair or target circle. Uses the full brand gradient from `#7c5cfc` to `#1a0e3d`, with a transparent outer background.

5. **Infinity spool** — A filament spool whose two reels are merged into a figure-eight / infinity symbol (∞), representing endless tracking and the continuous loop of print→log→deduct→print. Rendered as a single continuous stroke in brand violet with a soft inner glow in pale lavender. Transparent background, works as a clean silhouette at small sizes.

---

## Technical Constraints

Before delivering the final file, verify every item on this checklist:

| Constraint | Requirement |
|---|---|
| **Format** | PNG with full alpha channel transparency (no white or black background fill) |
| **Canvas size** | 512 × 512 px minimum; 1024 × 1024 px preferred for retina sharpness |
| **Aspect ratio** | Square (1:1) |
| **Background** | Fully transparent — the icon must composite cleanly onto the header gradient (`#6c3aaa` → `#1a0e3d`) |
| **Readable at 52 px** | The design must be legible and recognisable at 52 px height; avoid fine detail that disappears at small size |
| **Colour palette** | Restricted to the brand palette above; no orange, red, blue, or green elements |
| **No text** | The icon must contain no letterforms or words (the heading supplies the name) |
| **No outer glow / halo on background** | Because the background is transparent, any soft-edge glow must fade to full transparency, not to a solid colour |

---

## How to drop the icon into The Moment

Once the final PNG is approved and passes the checklist:

1. Save the file as `the-moment-logo.png`
2. Replace `/static/images/the-moment-logo.png` in the project root
3. No code changes needed — [base.html:27](../templates/base.html#L27) already renders it as:
   ```html
   <img src="/static/images/the-moment-logo.png" alt="The Moment"
        style="height:52px;width:auto;flex-shrink:0;">
   ```
4. If running the dev server, refresh the browser — no rebuild required (static files are served directly by Gin)
5. If running in Docker, the static directory is baked into the image at build time — rebuild with `make dev-rebuild` or `docker compose build` and restart

---

## Questions for the user before generating

If anything is ambiguous after reading this brief, ask the user before generating. Specifically:

- Does the icon need to work on light backgrounds as well, or only on the dark purple header?
- Should the icon include any hint of the NFC/wireless motif, or is filament + time sufficient?
- Is a fully abstract/geometric mark acceptable, or should it include a recognisable real-world object (spool, printer nozzle)?
- Is there a concept not listed above that you would like to explore?

---

*Brief written for ChatGPT image generation. The Moment is GPL-3.0 by ThetaSigma Labs.*
