/** Complete, static NativeWind classes so Metro/Tailwind can discover every variant.
 * Semantic colors are defined once in src/global.css, shared with gluestack.
 */
export const textVariants = {
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

export function progressPercent(value: number): number {
  return Number.isFinite(value)
    ? Math.max(0, Math.min(100, Math.floor(value)))
    : 0;
}
