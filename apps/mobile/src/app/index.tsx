import { ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { styled } from "nativewind";

import { UploadForm } from "@/components/upload-form";
import { BottomTabInset } from "@/constants/theme";

const ScreenSafeArea = styled(SafeAreaView, { className: "style" });

export default function HomeScreen() {
  return (
    <View className="flex-1 bg-background">
      <ScreenSafeArea edges={["left", "right", "bottom"]} className="flex-1">
        <ScrollView
          contentContainerClassName="grow w-full max-w-[640px] self-center p-6"
          contentContainerStyle={{ paddingBottom: BottomTabInset + 24 }}
        >
          <UploadForm />
        </ScrollView>
      </ScreenSafeArea>
    </View>
  );
}
