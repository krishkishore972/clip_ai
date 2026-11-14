/** Complete, static NativeWind classes so Metro/Tailwind can discover every variant.
 * Semantic colors are defined once in src/global.css, shared with gluestack.
 */
export const textVariants = {
  screenTitle: "font-display-bold text-[34px] font-normal leading-[40px]",
  hero: "font-display-bold text-[42px] font-normal leading-[46px]",
  display: "font-display text-[44px] font-normal leading-[48px]",
  title: "font-display-bold text-3xl font-normal leading-9",
  heading: "font-display-medium text-xl font-normal leading-7",
  body: "font-display text-base font-normal leading-6",
  label: "font-display-medium text-sm font-normal leading-5",
  caption: "font-display text-sm font-normal leading-5",
} as const;

export const textTones = {
  text: "text-foreground",
  secondary: "text-muted-foreground",
  accent: "text-primary-ink",
  danger: "text-destructive",
  success: "text-success",
} as const;

/**
 * Resolved values for native props that cannot consume NativeWind classes
 * (SVG icon strokes, ActivityIndicator, the navigation theme).
 *
 * These mirror the dark semantic tokens in src/global.css. The app shell is
 * always dark, so there is deliberately no light counterpart here. A design
 * test asserts that every value still matches its CSS token.
 */
export const nativeColors = {
  background: "#000000",
  card: "#111114",
  secondary: "#1C1C20",
  border: "#3C3C42",
  foreground: "#FFFFFF",
  primary: "#F0F424",
  primaryForeground: "#000000",
  mutedForeground: "#AAAAB0",
} as const;

export function progressPercent(value: number): number {
  return Number.isFinite(value)
    ? Math.max(0, Math.min(100, Math.floor(value)))
    : 0;
}
