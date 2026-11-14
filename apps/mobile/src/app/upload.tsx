import { styled } from "nativewind";
import { Image, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { UploadForm } from "@/components/upload-form";
import { AppText } from "@/design-system";

const ScreenSafeArea = styled(SafeAreaView, { className: "style" });

export default function UploadScreen() {
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
            Upload
          </AppText>
        </View>
        <ScrollView
          contentContainerClassName="grow w-full max-w-[640px] self-center p-6"
          contentContainerStyle={{ paddingBottom: 32 }}
        >
          <UploadForm />
        </ScrollView>
      </ScreenSafeArea>
    </View>
  );
}
