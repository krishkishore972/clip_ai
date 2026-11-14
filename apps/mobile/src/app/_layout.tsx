import { ClerkProvider } from "@clerk/expo";
import { tokenCache } from "@clerk/expo/token-cache";
import { DarkTheme, ThemeProvider } from "expo-router";
import { Tabs } from "expo-router/js-tabs";
import { useFonts } from "expo-font";
import * as SplashScreen from "expo-splash-screen";
import { useEffect } from "react";
import { ActivityIndicator, Text, View } from "react-native";

import { AuthGate } from "@/components/auth-gate";
import { FloatingTabBar } from "@/components/floating-tab-bar";
import { displayFonts } from "@/design-system/fonts";
import { nativeColors } from "@/design-system/tokens";

import { GluestackUIProvider } from "@/components/ui/gluestack-ui-provider";
import "@/src/global.css";

const publishableKey = process.env.EXPO_PUBLIC_CLERK_PUBLISHABLE_KEY ?? "";

if (!publishableKey) {
  throw new Error(
    "Missing EXPO_PUBLIC_CLERK_PUBLISHABLE_KEY. Add your key to .env.local.\nRun: 1) clerk auth login  2) clerk link  3) clerk env pull — then restart the dev server.",
  );
}

SplashScreen.preventAutoHideAsync();

export default function RootLayout() {
  const [fontsLoaded, fontError] = useFonts(displayFonts);
  useEffect(() => {
    // Show an explicit loading/error screen instead of leaving the native splash
    // visible indefinitely if an asset fails. Never expose auth before fonts load.
    void SplashScreen.hideAsync().catch(() => {});
  }, []);
  if (!fontsLoaded) {
    return (
      <View className="flex-1 items-center justify-center gap-4 bg-background p-6">
        {!fontError && <ActivityIndicator color={nativeColors.primary} />}
        <Text
          className="text-center text-base text-foreground"
          accessibilityRole={fontError ? "alert" : undefined}
        >
          {fontError
            ? "Could not load the app fonts. Please restart the app."
            : "Loading Chorus Clip…"}
        </Text>
      </View>
    );
  }
  return (
    <GluestackUIProvider mode="dark">
      <ClerkProvider publishableKey={publishableKey} tokenCache={tokenCache}>
        <ThemeProvider
          value={{
            ...DarkTheme,
            colors: {
              ...DarkTheme.colors,
              background: nativeColors.background,
              card: nativeColors.card,
              primary: nativeColors.primary,
            },
          }}
        >
          <AuthGate>
            {/* AuthGate only mounts this navigator for a provisioned session, so
                the first tab (Home) is always the signed-in landing screen. */}
            <Tabs
              backBehavior="history"
              screenOptions={{ headerShown: false }}
              tabBar={(props) => <FloatingTabBar {...props} />}
            >
              <Tabs.Screen name="index" options={{ title: "Home" }} />
              <Tabs.Screen name="upload" options={{ title: "Upload" }} />
              <Tabs.Screen name="account" options={{ title: "Account" }} />
            </Tabs>
          </AuthGate>
        </ThemeProvider>
      </ClerkProvider>
    </GluestackUIProvider>
  );
}
