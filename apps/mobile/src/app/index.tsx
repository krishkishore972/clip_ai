import { useUser } from "@clerk/expo";
import { router } from "expo-router";
import { styled } from "nativewind";
import { Image, Pressable, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { ActionButton, AppText } from "@/design-system";

const ScreenSafeArea = styled(SafeAreaView, { className: "style" });

function accountInitial(
  firstName: string | null | undefined,
  email: string | null | undefined,
) {
  return (firstName?.[0] ?? email?.[0] ?? "C").toUpperCase();
}

export default function HomeScreen() {
  const { user } = useUser();
  const email = user?.primaryEmailAddress?.emailAddress;
  const initial = accountInitial(user?.firstName, email);

  return (
    <View className="flex-1 bg-background">
      <ScreenSafeArea edges={["top", "left", "right"]} className="flex-1">
        <View className="flex-row items-center justify-between px-6 pt-4">
          <AppText variant="screenTitle" accessibilityRole="header">
            Projects
          </AppText>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Open account"
            onPress={() => router.navigate("/account")}
            className="h-11 w-11 items-center justify-center overflow-hidden rounded-full bg-secondary"
          >
            {user?.imageUrl ? (
              <Image
                source={{ uri: user.imageUrl }}
                className="h-11 w-11"
                resizeMode="cover"
                accessibilityIgnoresInvertColors
              />
            ) : (
              <AppText variant="label">{initial}</AppText>
            )}
          </Pressable>
        </View>

        <View className="flex-1 items-center justify-center gap-5 px-8 pb-10">
          <Image
            source={require("../../assets/images/projects-empty.png")}
            className="h-52 w-52"
            resizeMode="contain"
            accessible={false}
          />
          <AppText variant="title" className="text-center">
            No projects yet
          </AppText>
          <AppText tone="secondary" className="max-w-[320px] text-center">
            Upload a video to store it privately. Project creation and clip
            review are not available yet.
          </AppText>
          <View className="mt-2 w-[220px]">
            <ActionButton
              label="Upload a video"
              onPress={() => router.navigate("/upload")}
            />
          </View>
        </View>
      </ScreenSafeArea>
    </View>
  );
}
