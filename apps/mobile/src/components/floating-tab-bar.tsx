import { type BottomTabBarProps } from "expo-router/js-tabs";
import { Pressable, View } from "react-native";
import Svg, { Circle, Path } from "react-native-svg";

import { nativeColors } from "@/design-system/tokens";

type IconProps = { color: string };

/** Icons are vector paths so they stay crisp at any density without PNG assets. */
function HomeIcon({ color }: IconProps) {
  return (
    <Svg width={25} height={25} viewBox="0 0 24 24" fill="none">
      <Path
        d="M4.5 10.6 12 4.4l7.5 6.2V19a1.5 1.5 0 0 1-1.5 1.5h-3.7v-5.1H9.7v5.1H6A1.5 1.5 0 0 1 4.5 19z"
        stroke={color}
        strokeWidth={1.7}
        strokeLinejoin="round"
      />
    </Svg>
  );
}

function UploadIcon({ color }: IconProps) {
  return (
    <Svg width={25} height={25} viewBox="0 0 24 24" fill="none">
      <Path
        d="M12 15.2V4.9m0 0L8.6 8.3M12 4.9l3.4 3.4"
        stroke={color}
        strokeWidth={1.7}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <Path
        d="M5.2 15.1V18a2 2 0 0 0 2 2h9.6a2 2 0 0 0 2-2v-2.9"
        stroke={color}
        strokeWidth={1.7}
        strokeLinecap="round"
      />
    </Svg>
  );
}

function AccountIcon({ color }: IconProps) {
  return (
    <Svg width={25} height={25} viewBox="0 0 24 24" fill="none">
      <Circle cx={12} cy={8.2} r={3.4} stroke={color} strokeWidth={1.7} />
      <Path
        d="M5.9 20.2c.4-3.5 2.9-5.6 6.1-5.6s5.7 2.1 6.1 5.6"
        stroke={color}
        strokeWidth={1.7}
        strokeLinecap="round"
      />
    </Svg>
  );
}

const TABS = {
  index: { label: "Home", Icon: HomeIcon },
  upload: { label: "Upload", Icon: UploadIcon },
  account: { label: "Account", Icon: AccountIcon },
} as const;

/** Home, Upload, Account — Upload sits in the middle, Account on the right. */
const TAB_ORDER = ["index", "upload", "account"] as const;

export function FloatingTabBar({
  state,
  emitter,
  navigateToTab,
  insets,
}: BottomTabBarProps) {
  const activeKey = state.routes[state.index]?.key;
  const routes = TAB_ORDER.map((name) =>
    state.routes.find((route) => route.name === name),
  ).filter((route) => route !== undefined);

  return (
    // The bar floats above the content. Its wrapper is opaque so anything that
    // scrolls down disappears behind it instead of showing around the pill.
    <View
      className="absolute inset-x-0 bottom-0 bg-background pt-3"
      style={{ paddingBottom: insets.bottom }}
    >
      <View className="items-center">
        <View className="flex-row items-center gap-10 rounded-full bg-secondary px-4 py-2">
          {routes.map((route) => {
            const focused = route.key === activeKey;
            const { label, Icon } = TABS[route.name as keyof typeof TABS];
            return (
              <Pressable
                key={route.key}
                accessibilityRole="tab"
                accessibilityLabel={label}
                accessibilityState={{ selected: focused }}
                onPress={() => {
                  const event = emitter.emit({
                    type: "tabPress",
                    target: route.key,
                    canPreventDefault: true,
                  });
                  if (!focused && !event.defaultPrevented) {
                    navigateToTab(route.key);
                  }
                }}
                onLongPress={() =>
                  emitter.emit({ type: "tabLongPress", target: route.key })
                }
                className={`h-14 w-14 items-center justify-center rounded-full ${
                  focused ? "bg-primary" : ""
                }`}
              >
                <Icon
                  color={
                    focused
                      ? nativeColors.primaryForeground
                      : nativeColors.mutedForeground
                  }
                />
              </Pressable>
            );
          })}
        </View>
      </View>
    </View>
  );
}
