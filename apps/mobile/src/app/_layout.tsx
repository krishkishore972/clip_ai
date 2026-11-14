import { ClerkProvider } from "@clerk/expo";
import { tokenCache } from "@clerk/expo/token-cache";
import { DarkTheme, ThemeProvider } from "expo-router";
import { useFonts } from "expo-font";
import * as SplashScreen from "expo-splash-screen";
import { useEffect } from "react";
import { ActivityIndicator, Text, View } from "react-native";

import AppTabs from "@/components/app-tabs";
import { AuthGate } from "@/components/auth-gate";
import { displayFonts } from "@/design-system/fonts";

import { GluestackUIProvider } from "@/components/ui/gluestack-ui-provider";
import "@/src/global.css";

const publishableKey = process.env.EXPO_PUBLIC_CLERK_PUBLISHABLE_KEY ?? "";

if (!publishableKey) {
  throw new Error(
    "Missing EXPO_PUBLIC_CLERK_PUBLISHABLE_KEY. Add your key to .env.local.\nRun: 1) clerk auth login  2) clerk link  3) clerk env pull — then restart the dev server.",
  );
}

SplashScreen.preventAutoHideAsync();

export default function TabLayout() {
  const [fontsLoaded, fontError] = useFonts(displayFonts);
  useEffect(() => {
    // Show an explicit loading/error screen instead of leaving the native splash
    // visible indefinitely if an asset fails. Never expose auth before fonts load.
    void SplashScreen.hideAsync().catch(() => {});
  }, []);
  if (!fontsLoaded) {
    return (
      <View className="flex-1 items-center justify-center gap-4 bg-black p-6">
        {!fontError && <ActivityIndicator color="#F0F424" />}
        <Text
          className="text-center text-base text-white"
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
              background: "#000000",
              card: "#111114",
              primary: "#F0F424",
            },
          }}
        >
          <AuthGate>
            <AppTabs />
          </AuthGate>
        </ThemeProvider>
      </ClerkProvider>
    </GluestackUIProvider>
  );
}
