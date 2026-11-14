import { styled } from "nativewind";
import { tva } from "@gluestack-ui/utils/nativewind-utils";
import {
  Text,
  View,
  type ActivityIndicatorProps,
  type PressableProps,
  type TextProps,
  type ViewProps,
} from "react-native";

import { Button, ButtonSpinner, ButtonText } from "@/components/ui/button";
import { Progress, ProgressFilledTrack } from "@/components/ui/progress";
import { progressPercent, textTones, textVariants } from "./tokens";

const textStyle = tva({ variants: { variant: textVariants, tone: textTones } });

// Spinner uses a color prop, rather than a text style. RC0 requires explicit mapping.
function SpinnerBase(
  props: Pick<ActivityIndicatorProps, "color" | "accessible">,
) {
  return <ButtonSpinner {...props} />;
}
const Spinner = styled(SpinnerBase, {
  className: { target: false, nativeStyleMapping: { color: true } },
});

export function AppText({
  variant = "body",
  tone = "text",
  className = "",
  ...props
}: TextProps & {
  variant?: keyof typeof textVariants;
  tone?: keyof typeof textTones;
  className?: string;
}) {
  return (
    <Text
      {...props}
      className={textStyle({ variant, tone, class: className })}
    />
  );
}

export function Surface({
  level = "surface",
  className = "",
  ...props
}: ViewProps & { level?: "surface" | "raised"; className?: string }) {
  return (
    <View
      {...props}
      className={`gap-4 rounded-[28px] border border-border p-6 ${level === "surface" ? "bg-card" : "bg-secondary"} ${className}`}
    />
  );
}

type ActionButtonProps = Omit<
  PressableProps,
  "children" | "style" | "onPress"
> & {
  label: string;
  onPress: NonNullable<PressableProps["onPress"]>;
  variant?: "primary" | "secondary" | "danger";
  busy?: boolean;
  busyLabel?: string;
};

export function ActionButton({
  label,
  variant = "primary",
  busy = false,
  busyLabel = "Working…",
  disabled = false,
  accessibilityState,
  ...props
}: ActionButtonProps) {
  const blocked = disabled || busy;
  const textClass =
    variant === "primary"
      ? "text-primary-foreground"
      : variant === "danger"
        ? "text-destructive"
        : "text-secondary-foreground";
  return (
    <Button
      {...props}
      variant={variant === "primary" ? "default" : "secondary"}
      accessibilityRole="button"
      accessibilityState={{ ...accessibilityState, disabled: blocked, busy }}
      isDisabled={blocked}
      disabled={blocked}
      className={`min-h-14 gap-2 rounded-full border px-6 py-4 data-[active=true]:opacity-80 ${variant === "danger" ? "border-destructive" : variant === "primary" ? "border-primary" : "border-border"}`}
    >
      {busy && <Spinner className={textClass} accessible={false} />}
      <ButtonText
        className={`shrink font-display-medium text-center text-base font-normal leading-6 ${textClass}`}
      >
        {busy ? busyLabel : label}
      </ButtonText>
    </Button>
  );
}

export function StatusBadge({
  label,
  tone = "secondary",
}: {
  label: string;
  tone?: "secondary" | "success" | "danger";
}) {
  return (
    <View className="self-start rounded-full bg-secondary px-3 py-1">
      <AppText variant="label" tone={tone}>
        {label}
      </AppText>
    </View>
  );
}

export function ProgressMeter({
  value,
  label,
}: {
  value: number;
  label: string;
}) {
  const percent = progressPercent(value);
  return (
    <Progress
      value={percent}
      min={0}
      max={100}
      accessible
      accessibilityRole="progressbar"
      accessibilityLabel={label}
      accessibilityValue={{ min: 0, max: 100, now: percent }}
      className="h-2 overflow-hidden rounded-full border border-border bg-primary-foreground"
    >
      <ProgressFilledTrack className="h-full rounded-full bg-primary" />
    </Progress>
  );
}
