# Tenji sweep: ui-kit (src/components/ui, shared, layout, navigation, helpers)

Scope: src/components/ui/, src/components/shared/, src/components/layout/, src/components/navigation/,
src/components/helpers/. All 40 files in scope were read in full (list below). Cross-checked
H:/Projects/seanime/tenji-audit.md §0 ledger first — nothing in that ledger overlaps this scope
(it's API/WS/perf-hook level, not ui-kit).

## Files read (all 40, full read)
ui/: avatar.tsx, badge.tsx, bottom-sheet.tsx, button.tsx, card.tsx, chip-selector.tsx, date-picker.tsx,
form-field.tsx, input.tsx, label.tsx, separator.tsx, skeleton.tsx, switch.tsx, text.tsx
shared/: animations.ts, carousel.tsx, centered-spinner.tsx, episode-page-selector.tsx, inline-select.tsx,
labeled-switch.tsx, library-search-bar.tsx, library-search-header.tsx, luffy-error.tsx,
media-genre-selector.tsx, multi-toggle.tsx, native-select.tsx, offline-banner.tsx, option-row.tsx,
pagination.tsx, row-divider.tsx, sea-image.tsx, segmented-control.tsx, sheet-footer.tsx, styles.ts, surface.tsx
layout/: layout-view.tsx, tab-fade-view.tsx, tabs.tsx
navigation/: screen-header.tsx, tab-bar-icon.tsx
helpers/: score.ts

Also read for cross-checking (not in scope, not separately reported): global.css (theme tokens),
src/lib/useColorScheme.tsx, src/components/ThemeToggle.tsx, src/lib/constants.ts (NAV_THEME),
src/constants/colors.ts (COLORS), app/_layout.tsx (theme init effect), src/lib/icons/Ionicons.tsx,
src/components/features/player/external-player-picker-sheet.tsx (OptionRow consumer),
src/components/features/media/media-entry-score.tsx (getScoreColor consumer).

## Findings kept

### F1. OptionRow `detail` prop is accepted, typed, documented — and silently dropped (dead JSX)
File: src/components/shared/option-row.tsx:54-64 (inside the component body, lines ~46-73)
The JSDoc example above the component (lines 24-36) explicitly shows `detail={opt.sublabel}` as part of
the intended API, and `OptionRowProps` types `detail?: string`. But the actual rendering block is commented out:
```
<View className="flex-1 mr-3">
    <Text className="text-foreground text-sm font-medium">{label}</Text>
    {/* {detail ? (
     <Text ... >
     {detail}
     </Text>
     ) : null} */}
</View>
```
Concrete failure: src/components/features/player/external-player-picker-sheet.tsx:95-101 passes
`detail={preset.urlTemplate}` for every configured external-player preset row, intending to show the
URL scheme under the preset name (per the OptionRow doc comment's own example). Because rendering is
commented out, users who configure two presets can't distinguish them by URL template in the picker —
only the name is shown. Reported as medium (real, reachable, silently loses information in a shipped
screen) not high (app still functions, presets still selectable).

### F2. LibrarySearchBar focus-highlight animation is fully wired but never applied
File: src/components/shared/library-search-bar.tsx:38-58
`animatedContainerStyle` (interpolates border color from `rgba(255,255,255,0.08)` to the brand color on
focus, driven by `handleFocus`/`handleBlur` touching a shared value) is computed but the `style` prop that
would apply it is commented out:
```
<Animated.View
    className={cn(
        "flex-row items-center h-11 rounded-2xl bg-white/[0.04] px-3 gap-2",
        className,
    )}
// style={animatedContainerStyle}
>
```
The container's className has no `border` utility at all, so there is currently zero visual focus
affordance on the library search bar — tapping in and typing looks identical to idle. This is the
primary search entry point for the library/discover screens (verified via LibrarySearchHeader, which
renders LibrarySearchBar as the sole content of the sticky header). Low-medium: doesn't block use,
but is a concretely half-wired UX feature, not a stylistic no-op — someone built and then disabled it.

### F3. SeaBottomSheet ignores the app's color-scheme system and hardcodes the dark palette
File: src/components/ui/bottom-sheet.tsx:83,90-108
```
backgroundStyle={{ backgroundColor: NAV_THEME.dark.card }}
...
borderTopColor: "rgba(255,255,255,0.08)",
backgroundColor: NAV_THEME.dark.card,
```
`NAV_THEME` (src/lib/constants.ts) has both `light` and `dark` card colors, and the app has a real,
working light/dark mechanism: `src/lib/useColorScheme.tsx` + `Uniwind.setTheme` + `setStoredTheme`/
`getStoredTheme` (src/atoms/storage.ts), read on every cold start in app/_layout.tsx
(`getStoredTheme() ?? "dark"` → `setColorScheme`). But `SeaBottomSheet` never calls `useColorScheme()` —
it always paints the sheet body with `NAV_THEME.dark.card` (`hsl(0 0% 6%)`, near-black), while the title/
children inside it use theme-reactive Tailwind classes like `text-foreground` (via global.css's
`--color-foreground`, which becomes near-black in light mode). Concrete failure scenario: if the stored
theme is ever "light" (the only current path is via `src/components/ThemeToggle.tsx`, which itself is
never imported/mounted anywhere in `app/` — verified via repo-wide grep, so it is dead code today, not
reachable from any settings screen), every one of the 17 screens that use SeaBottomSheet (date picker,
search filters, collection filters, download modals, torrent/manga match modals, nakama room sheet,
episode/quick-info sheets, reader settings, external-player picker, season switcher, ...) would render
near-black-on-near-black text. Capped at low-medium severity because the only entry point into light mode
(ThemeToggle) is itself currently unreachable, so no shipped user flow triggers this today — but the
defect is real, not hypothetical, and the same anti-pattern (hardcoded `white/`, `black/`, or literal hex
opacity utilities instead of the theme tokens already defined in global.css) recurs pervasively across
this scope: ui/input.tsx:16, ui/chip-selector.tsx:44-48, ui/date-picker.tsx:73,84,87, ui/form-field.tsx:25,27,62,69,
shared/native-select.tsx:42,50,56, shared/multi-toggle.tsx:25,31, shared/inline-select.tsx:31,37,
shared/episode-page-selector.tsx:66,72, shared/segmented-control.tsx:46,59,71, shared/labeled-switch.tsx:26,30,
shared/offline-banner.tsx:19-21, shared/row-divider.tsx:10, shared/luffy-error.tsx:24,
shared/library-search-header.tsx:24-26,33-41, shared/styles.ts (via constants/colors.ts `COLORS`, itself
static/dark-only) and layout/tabs.tsx:162 (`TabBar` background = `COLORS.background`, also static).
`shared/surface.tsx` shows the codebase *does* know the correct pattern (`bg-card/30`, theme-reactive) —
so this is an inconsistency, not a uniform design choice.

### F4. getScoreColor: 82-89 wrongly gets the top "90-100" tier; 80-81 is a dead duplicate branch
File: src/components/helpers/score.ts:32-44
```
if (score < 82) { // 80-81
    return cn(
        kind === "user" && "bg-emerald-800/90",
        kind === "audience" && "text-emerald-200",
        kind === "audience-icon" && "accent-emerald-200",
    )
}
// 90-100
return cn(
    kind === "user" && "bg-indigo-600/90",
    kind === "audience" && "text-indigo-200",
    kind === "audience-icon" && "accent-indigo-200",
)
```
The `score < 82` branch is byte-identical to the `score < 80` branch immediately above it (same three
classes) — a dead/no-op branch. Worse, the final `return` is commented "90-100" but is reached by every
score >= 82, so scores 82-89 (e.g. 8.5/10) get the same indigo "top tier" styling as a 98. Verified
consumer: src/components/features/media/media-entry-score.tsx:19,40-41 uses `getScoreColor(score, "user"/
"audience"/"audience-icon")` directly for the score-badge background/text/icon-tint colors shown on
every anime entry. Concrete failure: an entry scored 85 (8.5) renders with the same indigo badge as one
scored 98 (9.8), while 81 (8.1) renders identically to 75 (7.5) — the intended 80-89 tier never exists.
Low severity: cosmetic only, no crash, but a clear implementation bug (not a design choice) given the
duplicate branch and mislabeled comment.

### F5. MultiToggle / InlineSelect / ChipSelector are three ~95%-identical re-implementations
Files: src/components/shared/multi-toggle.tsx, src/components/shared/inline-select.tsx,
src/components/ui/chip-selector.tsx
All three render a `flex-row flex-wrap` list of pill `Pressable`s with the same
`h-9/h-10 px-4 rounded-xl|rounded-full border` selected/idle class pair, differing only in whether
selection is single (nullable) or multi, and whether an icon is supported. Reported as a low-severity
component-API-consistency smell per this scope's explicit concern list, not a functional bug — three
call sites to keep in sync (e.g. only ChipSelector supports the `icon` field; a future request to add
icons to InlineSelect/MultiToggle needs three separate edits).

### F6. ScreenHeader is an empty, unused, zero-prop component
File: src/components/navigation/screen-header.tsx (7 lines total)
```
export function ScreenHeader() {
    return (
        <View></View>
    )
}
```
Repo-wide grep confirms zero imports anywhere outside its own definition file — a dead scaffold.
Very low severity/impact (nothing renders it, nothing breaks), noted for completeness only.

## Near-misses considered and rejected
- switch.tsx `SwitchNative`'s `RGB_COLORS` hardcodes literal RGB per-scheme values instead of reading
  the CSS variables — but unlike F3, this one *does* branch on `colorScheme` correctly (light vs dark
  both defined and selected via `useColorScheme()`), it just can't use CSS vars because
  `interpolateColor` (react-native-reanimated, UI thread) needs concrete rgb values, not CSS var
  references. Correct/necessary pattern, not a bug.
- `Ionicons colorClassName={getScoreColor(score, "audience-icon")}` (media-entry-score.tsx:40, outside
  this scope) resolves to classes like `accent-emerald-200` — `accent-*` is normally a CSS
  `accent-color` utility, not a text/icon color utility, which looked suspicious. Did not have enough
  visibility into `uniwind`'s `withUniwind`/`colorClassName` internals to confirm whether this
  specific codebase treats `accent-*` as a generic icon-tint token by convention (used consistently
  across all three `getScoreColor` kinds) or whether it's actually broken. Left unreported — the
  helper itself (`helpers/score.ts`) is in scope, but I can't verify the icon-tint resolution behavior
  without reading the icon wrapper's runtime resolution logic (out of scope / not confidently
  verifiable from static reading alone), so it doesn't meet the evidence bar for a concrete finding.
- `sheet-footer.tsx` / `media-entry-quick-info-sheet.tsx` use `h-13` (not a "standard" Tailwind v3
  step). Confirmed this project uses Tailwind v4 (`@import 'tailwindcss'` in global.css), where
  numeric spacing utilities resolve algorithmically (`h-N` = `calc(var(--spacing) * N)`), so `h-13`
  (52px) is valid and intentional, not a typo. Not a bug.
- `LabeledSwitch`'s outer `Pressable onPress={onToggle}` wraps a `Switch` that also fires
  `onCheckedChange={onToggle}` — looked like a possible double-fire on tapping the thumb directly, but
  RN's nested-responder system means the inner touchable claims the gesture, so the outer `onPress`
  does not also fire. Standard pattern, not a bug.
- `LibrarySearchBar`'s clear button (h-5 w-5 with hitSlop 8) and `DatePicker`'s clear button
  (Ionicons wrapped in `p-1`) are icon-only touch targets with no `accessibilityLabel`, and both are
  under the ~44pt recommended minimum even with hitSlop. Real, but this pattern is so widespread across
  the whole app (not isolated to a broken component) that it reads as a systemic
  design-system-level gap rather than a specific defect in one component; flagged in notes only, not
  spun up as a separate finding to avoid diluting the report with a dozen near-identical low-value
  items — capped at "considered, not reported" per the volume-vs-quality guidance.
- `Styles.Container`/`COLORS.background` driving `LayoutContainerView`/`SafeView`/`TabBar` — same root
  cause as F3 (hardcoded dark-only literal instead of the theme-reactive CSS vars). Folded into F3's
  description rather than a separate finding since it's the exact same defect class with the exact same
  (currently-unreachable) precondition.

## Coverage
All 40 files in the assigned scope were read in full; no sampling. No file was skipped for size —
largest was layout/tabs.tsx at 186 lines. Cross-referenced consumers outside scope (external-player-
picker-sheet.tsx, media-entry-score.tsx, library-search-header.tsx's own file which is in scope) only to
confirm reachability/impact of in-scope defects, not audited themselves.
