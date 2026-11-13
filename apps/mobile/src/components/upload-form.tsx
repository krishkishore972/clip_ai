import { useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  AppState,
  Pressable,
  StyleSheet,
  View,
} from "react-native";

import { Spacing } from "@/constants/theme";
import { useTheme } from "@/hooks/use-theme";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import { pickVideo, type SelectedVideo } from "@/lib/uploads/native-video";
import { uploadVideo, type UploadProgress } from "@/lib/uploads/upload-video";

import { ThemedText } from "./themed-text";
import { ThemedView } from "./themed-view";

const sizeLabel = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
const statusLabels = {
  creating: "Preparing upload…",
  uploading: "Uploading video parts…",
  completing: "Finalizing upload…",
  complete: "Upload complete",
};

export function UploadForm() {
  const theme = useTheme();
  const [selected, setSelected] = useState<SelectedVideo | null>(null);
  const [operation, setOperation] = useState<"selecting" | "uploading" | null>(
    null,
  );
  const busy = operation !== null;
  const [progress, setProgress] = useState<UploadProgress | null>(null);
  const [message, setMessage] = useState("");
  const controller = useRef<AbortController | null>(null);
  const locked = useRef(false);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    const subscription = AppState.addEventListener("change", (state) => {
      if (state !== "active") controller.current?.abort();
    });
    return () => {
      mounted.current = false;
      subscription.remove();
      controller.current?.abort();
    };
  }, []);

  async function select() {
    if (locked.current) return;
    locked.current = true;
    setOperation("selecting");
    try {
      const video = await pickVideo();
      if (video && mounted.current) {
        setSelected(video);
        setProgress(null);
        setMessage("");
      }
    } catch (error) {
      // Keep the real reason: the generic text below hid every failure.
      console.warn("Video selection failed", error);
      if (mounted.current)
        setMessage(
          error instanceof Error
            ? error.message
            : "Could not select this video. Choose a readable MP4, MOV, or WebM up to 2 GB.",
        );
    } finally {
      locked.current = false;
      if (mounted.current) setOperation(null);
    }
  }

  async function start() {
    if (locked.current || !selected) return;
    locked.current = true;
    const active = new AbortController();
    controller.current = active;
    setOperation("uploading");
    setMessage("");
    setProgress(null);
    try {
      // The usual cause of "Request failed or timed out" is this URL not being
      // reachable from the phone (localhost, wrong LAN IP, API not running).
      console.warn(
        "[upload] using API base",
        process.env.EXPO_PUBLIC_API_URL ?? "(EXPO_PUBLIC_API_URL is unset)",
      );
      const api = configuredUploadApi();
      await uploadVideo({
        api,
        file: selected.metadata,
        source: selected.open(),
        signal: active.signal,
        onProgress: (value) => {
          if (mounted.current) setProgress(value);
        },
      });
      if (mounted.current)
        setMessage(
          "Stored privately in S3. Video validation and clip analysis are not implemented yet.",
        );
    } catch (error) {
      console.error("[upload] failed", error);
      if (mounted.current)
        setMessage(
          error instanceof Error
            ? error.message
            : "Could not upload this video.",
        );
    } finally {
      controller.current = null;
      locked.current = false;
      if (mounted.current) setOperation(null);
    }
  }

  const percentage = progress
    ? Math.floor((100 * progress.completedBytes) / progress.totalBytes)
    : 0;
  const complete = progress?.phase === "complete";
  return (
    <ThemedView style={styles.form}>
      <ThemedText type="smallBold" themeColor="textSecondary">
        DIRECT TO S3 · DEVELOPMENT
      </ThemedText>
      <ThemedText type="title">Upload a video</ThemedText>
      <ThemedText themeColor="textSecondary">
        Select a local video to upload directly to your private S3 bucket.
      </ThemedText>
      <ThemedView type="backgroundElement" style={styles.card}>
        <ThemedText type="smallBold">
          {selected?.metadata.filename ?? "No video selected"}
        </ThemedText>
        <ThemedText type="small" themeColor="textSecondary">
          {selected
            ? `${sizeLabel(selected.metadata.sizeBytes)} · ${selected.metadata.contentType}`
            : "MP4, MOV or WebM · up to 2 GB"}
        </ThemedText>
        <Pressable
          accessibilityRole="button"
          disabled={busy}
          onPress={select}
          style={[
            styles.button,
            { borderColor: theme.textSecondary },
            busy && styles.disabled,
          ]}
        >
          <ThemedText type="smallBold">
            {selected ? "Choose another video" : "Select video"}
          </ThemedText>
        </Pressable>
        <ThemedText type="small" themeColor="textSecondary">
          The original is never modified. Keep the video available on this
          device until the upload finishes.
        </ThemedText>
      </ThemedView>
      {progress && (
        <View style={styles.form}>
          <View
            accessibilityRole="progressbar"
            accessibilityLabel="Uploaded video parts"
            accessibilityValue={{ min: 0, max: 100, now: percentage }}
            style={[styles.track, { backgroundColor: theme.backgroundElement }]}
          >
            <View style={[styles.fill, { width: `${percentage}%` }]} />
          </View>
          <ThemedText type="small">
            {percentage}% · {sizeLabel(progress.completedBytes)} /{" "}
            {sizeLabel(progress.totalBytes)}
          </ThemedText>
          <ThemedText type="smallBold" accessibilityLiveRegion="polite">
            {statusLabels[progress.phase]}
          </ThemedText>
          <ThemedText type="small" themeColor="textSecondary">
            Progress counts confirmed parts. Upload is complete only after
            finalization.
          </ThemedText>
        </View>
      )}
      {busy && <ActivityIndicator color={theme.text} />}
      {!!message && (
        <ThemedText type="small" accessibilityLiveRegion="polite">
          {message}
        </ThemedText>
      )}
      {!complete && (
        <Pressable
          accessibilityRole="button"
          disabled={busy || !selected}
          onPress={start}
          style={[
            styles.button,
            styles.primary,
            (busy || !selected) && styles.disabled,
          ]}
        >
          <ThemedText type="smallBold" style={styles.primaryText}>
            Upload video
          </ThemedText>
        </Pressable>
      )}
      {operation === "uploading" && (
        <Pressable
          accessibilityRole="button"
          onPress={() => controller.current?.abort()}
          style={[styles.button, { borderColor: theme.textSecondary }]}
        >
          <ThemedText type="smallBold">Cancel upload</ThemedText>
        </Pressable>
      )}
      <ThemedText type="small" themeColor="textSecondary">
        Keep the app in the foreground. Leaving it stops the upload. Failed
        uploads start fresh; resume is not supported. This dev flow has no user
        authentication.
      </ThemedText>
    </ThemedView>
  );
}

const styles = StyleSheet.create({
  form: { gap: Spacing.three },
  card: { gap: Spacing.three, padding: Spacing.four, borderRadius: 16 },
  button: {
    borderWidth: 1,
    borderRadius: 12,
    padding: Spacing.three,
    alignItems: "center",
    minHeight: 48,
  },
  primary: { backgroundColor: "#245DDD", borderColor: "#245DDD" },
  primaryText: { color: "#FFFFFF" },
  disabled: { opacity: 0.45 },
  track: { height: 8, borderRadius: 4, overflow: "hidden" },
  fill: { height: "100%", backgroundColor: "#245DDD" },
});
