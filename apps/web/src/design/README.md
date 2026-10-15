# Alpine design system

Small, deliberate, and token-driven. Screens never hard-code colours.

| File | What |
| --- | --- |
| `tokens.css` | CSS variables (light + `.dark`), Tailwind `@theme` mapping, base styles, `tnum` / `kbd` utilities |
| `tokens.ts` | Semantic maps for stages, blast radius, gate status, motion |
| `theme.tsx` | Light / dark / system theme provider (`?theme=dark` forces a theme, used for screenshots) |

Rules

- **Colour:** neutral grays + one accent (`brand`, glacier teal) for focus, links, active nav and the primary
  chart series. Primary buttons are neutral ink. Status (`ok`, `warn`, `fail`, `info`), stage
  (`stage-*`) and blast-radius (`br1`…`br4`) colours are semantic only.
- **Type:** Inter Variable, 13px base. Scale: 11 / 12 / 13 / 14 / 16 / 20 / 24. Numbers in tables and
  metrics use `tnum`.
- **Space:** 4px grid (`--spacing: 4px`). Radius 4 / 6 / 8. 1px borders, no decorative shadows
  (only overlays like the command palette get a shadow).
- **Motion:** 150–200ms, standard easing, only for state changes (hover, open, live updates).
  Respects `prefers-reduced-motion`.
- **Icons:** lucide only, stroke width 1.75 (set globally).
- **Accessibility:** colour pairs target WCAG AA; every interactive element has a visible
  `:focus-visible` ring; icon-only buttons have `aria-label`s.
