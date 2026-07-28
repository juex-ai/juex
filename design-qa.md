# Composer occlusion design QA

## Evidence

- Source visual truth:
  - `/var/folders/pd/f67pmmsn7898xr6n2xpmgzgr0000gn/T/codex-clipboard-427aee9a-7a8c-4e5d-b8b3-22c7ee6e9fd8.png`
  - `/var/folders/pd/f67pmmsn7898xr6n2xpmgzgr0000gn/T/codex-clipboard-afcb96b4-7b44-4cc4-9dba-e39b1b254433.png`
  - `/var/folders/pd/f67pmmsn7898xr6n2xpmgzgr0000gn/T/codex-clipboard-0d404775-6488-4e38-a119-dadffea0486c.png`
- Browser-rendered implementation:
  - `.tmp/design-qa/composer-occlusion/regression-fixed-1280x720.png`
  - `.tmp/design-qa/composer-occlusion/regression-fixed-mobile-390x844.png`
  - `.tmp/design-qa/composer-occlusion/high599-regression-fixed-1280x720.png`
- Full-view comparison:
  - `.tmp/design-qa/composer-occlusion/regression-comparison.png`
- Desktop viewport and pixels: `1280 x 720` CSS pixels, `1280 x 720`
  screenshot pixels, density `1`.
- Mobile viewport and pixels: `390 x 844` CSS pixels, `390 x 844`
  screenshot pixels, density `1`.
- Deployed high599 viewport and pixels: `1280 x 720` CSS pixels,
  `1280 x 720` screenshot pixels, density `1`.
- State: active primary session, manually scrolled so transcript content crosses
  the composer's top fade.

## Comparison history

### Regression findings

- P1: The transcript viewport was shortened to the fade boundary, so the
  native scrollbar stopped above the viewport bottom instead of describing the
  full scrolling region.
- P1: The scroll-to-bottom control was changed from the measured composer
  clearance plus 16px to a fixed 16px offset, leaving it almost attached to the
  prompt surface.
- Target behavior: preserve the full-height scrollport and original control
  offset, while hiding only composer-width transcript content below the
  rounded-top fade.

### Initial findings

- P1: The 48px fade stopped exactly at the prompt's top edge. Text crossing the
  16px rounded corner changed abruptly from faded to fully opaque.
- P1: The transcript viewport continued behind the entire floating overlay, so
  manually scrolled text could reappear in the transparent safe area below the
  prompt.

### Fixes

- Extended the composer-width fade 16px behind the rounded top edge and kept it
  below the prompt stack.
- Preserved the full measured composer clearance as transcript bottom padding
  and removed the viewport inset that shortened the native scrollport.
- Added a composer-width background occluder from the fade's opaque boundary
  to the viewport bottom. It hides transcript content behind the prompt and
  safe area without covering the scrollbar or adding a full-width surface.
- Restored the scroll-to-latest control to the measured composer clearance plus
  its original 16px offset.

### Post-fix evidence

- Desktop geometry: fade `540..604`, prompt top `588`, occluder `604..720`,
  transcript scrollport `52..720`, and a 64px button-to-prompt gap.
- Mobile geometry: fade `672..736`, prompt top `720`, occluder `736..844`,
  transcript scrollport `52..844`, a 64px button-to-prompt gap, and no
  horizontal document overflow.
- Deployed high599 geometry matches desktop: transcript scrollport bottom
  `720`, occluder bottom `720`, and a 64px button-to-prompt gap.
- The combined comparison shows the restored control spacing and a scrollbar
  track that reaches the viewport bottom while transcript text remains hidden
  beneath the prompt.

## Fidelity review

- Fonts and typography: unchanged; transcript and composer type retain the
  existing Juex system and monospace stacks.
- Spacing and layout rhythm: the total 150px-or-greater clearance and original
  scroll-control offset are preserved. The prompt radius, shadow, width, safe
  area, and latest-message position are unchanged.
- Colors and visual tokens: the existing background-token fade remains. No
  viewport-wide rectangular background surface was added.
- Image quality and asset fidelity: no image or icon assets changed.
- Copy and content: no product copy changed.
- Responsive behavior: passed at `1754 x 616` and `390 x 844`; the composer
  remains reachable and no horizontal overflow appears.

## Interaction and runtime checks

- Composer fill and clear passed on the mobile viewport.
- Scroll-to-latest passed and removed the control after reaching the bottom.
- Fresh deployed high599 browser console warnings/errors: none.

## Remaining findings

No actionable P0, P1, or P2 findings remain. No focused visual fix is pending.

final result: passed
