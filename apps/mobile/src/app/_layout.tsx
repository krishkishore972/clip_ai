import { ClerkProvider } from "@clerk/expo";
import { tokenCache } from "@clerk/expo/token-cache";
import { DarkTheme, ThemeProvider } from "expo-router";
import { NativeTabs } from "expo-router/native-tabs";
import { useFonts } from "expo-font";
import * as SplashScreen from "expo-splash-screen";
import { useEffect } from "react";
import { ActivityIndicator, Text, View } from "react-native";

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

export default function RootLayout() {
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
            {/* AuthGate only mounts this navigator for a provisioned session, so
                the first tab (Home) is always the signed-in landing screen. */}
            <NativeTabs
              backgroundColor="#000000"
              iconColor={{ default: "#8E8E96", selected: "#F0F424" }}
              labelStyle={{
                default: { color: "#8E8E96", fontSize: 11 },
                selected: { color: "#F0F424", fontSize: 11 },
              }}
              indicatorColor="#1F1F23"
              rippleColor="#2A2A2E"
              labelVisibilityMode="labeled"
              backBehavior="history"
            >
              <NativeTabs.Trigger name="index">
                <NativeTabs.Trigger.Label>Home</NativeTabs.Trigger.Label>
                <NativeTabs.Trigger.Icon sf="house.fill" md="home" />
              </NativeTabs.Trigger>
              <NativeTabs.Trigger name="account">
                <NativeTabs.Trigger.Label>Account</NativeTabs.Trigger.Label>
                <NativeTabs.Trigger.Icon sf="person.fill" md="person" />
              </NativeTabs.Trigger>
              <NativeTabs.Trigger name="upload">
                <NativeTabs.Trigger.Label>Upload</NativeTabs.Trigger.Label>
                <NativeTabs.Trigger.Icon sf="plus.square.fill" md="add_box" />
              </NativeTabs.Trigger>
            </NativeTabs>
          </AuthGate>
        </ThemeProvider>
      </ClerkProvider>
    </GluestackUIProvider>
  );
}
