---
name: TypeSafe AI Design System
version: 1.0.0
aesthetic: Tech-Editorial Neobrutalism / Code-Native Precision
tokens:
  colors:
    surface-parchment: "#F7F4EB"
    surface-paper: "#FFFFFF"
    surface-terminal: "#111214"
    ink-primary: "#121212"
    ink-secondary: "#52525B"
    ink-inverse: "#F7F4EB"
    accent-pink: "#FF66C4"
    accent-pink-soft: "#FFB3E1"
    accent-blue: "#2563EB"
    border-subtle: "#D4D1C7"
    border-strong: "#121212"
    status-confident: "#16A34A"
    status-uncertain: "#D97706"
  typography:
    font-display: "'Space Mono', monospace"
    font-body: "'Inter', -apple-system, BlinkMacSystemFont, sans-serif"
    font-mono: "'JetBrains Mono', 'Space Mono', monospace"
    scale:
      hero: "clamp(2.5rem, 5vw, 4.5rem)"
      h1: "2.25rem"
      h2: "1.5rem"
      body: "1rem"
      micro: "0.75rem"
  spacing:
    grid-unit: "8px"
    section-padding: "96px"
    card-padding: "24px"
  radii:
    none: "0px"
    sm: "2px"
    md: "4px"
  borders:
    hairline: "1px solid #121212"
    dashed: "1px dashed #121212"
    dotted: "1px dotted #121212"
---

# DESIGN.md — TypeSafe AI Web UI Specification

## 1. Brand & Visual Philosophy
TypeSafe AI rejects generic dark-mode neon AI tropes in favor of a **tactile, editorial, code-native aesthetic**. The interface should feel like a living technical specification sheet combined with retro OS windows. 
- **Core Mood:** Deterministic, architectural, playful yet mathematically precise.
- **Visual Contrast:** High-contrast warm parchment (`#F7F4EB`) paired with stark ink black (`#121212`), punctuated by bold duotone/bright pink (`#FF66C4`) feature bands.

## 2. Color & Surface Rules
- **Primary Canvas:** Always default page backgrounds to `surface-parchment` (`#F7F4EB`) rather than pure white or dark mode.
- **Editorial Highlight Sections:** Use `accent-pink` (`#FF66C4`) as a full-bleed background for high-impact product demos, benchmarks, or terminal showcases. Keep text on pink strictly `ink-primary` (`#121212`) for WCAG AAA contrast.
- **No Soft Gradients:** Never use blurred mesh gradients, glassmorphism, or drop-shadow glows. Depth is created strictly through crisp `1px` borders, solid offset shadows (`4px 4px 0px #121212`), and overlapping window frames.

## 3. Typography & Hierarchy
- **Headings (`Space Mono`):** All H1, H2, and section labels must use `font-display` (`Space Mono`) with tight tracking (`-0.03em`) and crisp line-height (`1.1`).
- **Body Copy (`Inter`):** Use clean geometric sans-serif for multi-line explanations and documentation to maintain high readability.
- **Data, Badges & Confidence Scores (`JetBrains Mono`):** Any numeric metric, probability threshold (e.g., `confidence: 0.984`), or schema key must be rendered in monospace with tabular numerals (`font-variant-numeric: tabular-nums`).

## 4. Layout & Grid Architecture
- **Visible Structural Grid:** Expose the underlying 12-column editorial grid using subtle `1px` rules (`border-subtle`) between major layout blocks.
- **Generous Whitespace:** Maintain wide outer margins and structured vertical rhythm (`96px` between sections).
- **Interactive Retro Windows:** Present code snippets, typed JSON outputs, and live decision pipelines inside framed OS-style window cards featuring:
  - A `28px` top title bar with `1px solid #121212` bottom border.
  - Window controls (minimal square/circle glyphs) and a monospace filename/endpoint label (e.g., `jev.decide(schema)`).

## 5. Component Specifications

### Buttons
- **Primary CTA:** Solid `#121212` fill, `#F7F4EB` text, `Space Mono` uppercase or lowercase label, `2px` border-radius, `12px 20px` padding. Hover state inverts to `#FF66C4` background with `#121212` text and a `1px solid #121212` border.
- **Secondary CTA:** Transparent background, `1px solid #121212` border, `#121212` text.

### Interactive Accordions / Q&A Rows
- **Collapsed State:** Left border uses a `2px dotted #121212` rule; title in `Space Mono`.
- **Expanded State:** Left border transitions smoothly to a `2px solid #121212` rule; body text reveals in `Inter`.

### Typed Decision / Confidence Badges
- Display structured model outputs as pill-less rectangular tags (`2px` radius, `1px solid #121212`).
- Include key-value pairs separated by a vertical hairline divider (e.g., `ACTION: APPROVE | P: 0.992`).

## 6. Motion & Micro-Interactions
- **Text Scramble:** Apply a subtle monospace character-scramble effect on hover for primary navigation links and hero technical tags.
- **Tactile Snap:** Interactive cards and draggable demo windows should move with crisp spring physics (`stiffness: 400, damping: 28`) and snap cleanly back to grid alignment.
- **Performance:** Prefer lightweight CSS transitions and crisp SVGs over heavy video embeds.
