import { useClerk, useUser } from "@clerk/expo";
import { styled } from "nativewind";
import { useState } from "react";
import { Image, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { ActionButton, AppText, Surface } from "@/design-system";

const ScreenSafeArea = styled(SafeAreaView, { className: "style" });

export default function AccountScreen() {
  const { user } = useUser();
  const { signOut } = useClerk();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  const name = user?.fullName ?? user?.firstName ?? "Chorus Clip member";
  const email = user?.primaryEmailAddress?.emailAddress ?? "Signed in";

  async function logout() {
    setBusy(true);
    setMessage("");
    try {
      await signOut();
    } catch {
      setMessage("Could not sign out. Please try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <View className="flex-1 bg-background">
      <ScreenSafeArea edges={["top", "left", "right"]} className="flex-1">
        <View className="flex-row items-center gap-3 px-6 pt-4">
          <Image
            source={require("../../assets/images/chorus-logo.png")}
            className="h-9 w-9"
            resizeMode="contain"
            accessibilityRole="image"
            accessibilityLabel="Chorus Clip"
          />
          <AppText variant="screenTitle" accessibilityRole="header">
            Account
          </AppText>
        </View>

        <ScrollView
          contentContainerClassName="grow w-full max-w-[640px] self-center gap-6 p-6"
          contentContainerStyle={{ paddingBottom: 128 }}
        >
          <Surface>
            <View className="flex-row items-center gap-4">
              {user?.imageUrl ? (
                <Image
                  source={{ uri: user.imageUrl }}
                  className="h-16 w-16 rounded-full"
                  resizeMode="cover"
                  accessibilityIgnoresInvertColors
                />
              ) : (
                <View className="h-16 w-16 items-center justify-center rounded-full bg-primary">
                  <AppText variant="title" className="text-primary-foreground">
                    {name[0]?.toUpperCase() ?? "C"}
                  </AppText>
                </View>
              )}
              <View className="flex-1">
                <AppText variant="heading">{name}</AppText>
                <AppText tone="secondary">{email}</AppText>
              </View>
            </View>
            <AppText variant="caption" tone="secondary">
              Uploads are linked to this signed-in account. Your originals stay
              private.
            </AppText>
          </Surface>

          <ActionButton
            label="Sign out"
            variant="secondary"
            busy={busy}
            busyLabel="Signing out…"
            onPress={logout}
          />
          {!!message && (
            <AppText tone="danger" accessibilityRole="alert">
              {message}
            </AppText>
          )}
          <AppText variant="caption" tone="secondary" className="text-center">
            Chorus Clip · Development build
          </AppText>
        </ScrollView>
      </ScreenSafeArea>
    </View>
  );
}
