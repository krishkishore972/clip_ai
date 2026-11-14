import { useEffect, useRef, useState } from "react";
import { useAuth } from "@clerk/expo";
import { AppState, View } from "react-native";

import {
  ActionButton,
  AppText,
  ProgressMeter,
  StatusBadge,
  Surface,
} from "@/design-system";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import { pickVideo, type SelectedVideo } from "@/lib/uploads/native-video";
import { uploadVideo, type UploadProgress } from "@/lib/uploads/upload-video";

const sizeLabel = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
const statusLabels = {
  creating: "Preparing upload…",
  uploading: "Uploading video parts…",
  completing: "Finalizing upload…",
  complete: "Upload complete",
};

export function UploadForm() {
  const { getToken } = useAuth();
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
      const api = configuredUploadApi(getToken);
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
    <View className="gap-4">
      <StatusBadge label="Private upload · Development" />
      <AppText variant="title" accessibilityRole="header">
        Your next clip starts here.
      </AppText>
      <AppText tone="secondary">
        Choose a video to store privately. Your original stays untouched.
      </AppText>
      <Surface>
        <AppText variant="heading">
          {selected?.metadata.filename ?? "No video selected"}
        </AppText>
        <AppText variant="caption" tone="secondary">
          {selected
            ? `${sizeLabel(selected.metadata.sizeBytes)} · ${selected.metadata.contentType}`
            : "MP4, MOV or WebM · up to 2 GB"}
        </AppText>
        <ActionButton
          label={selected ? "Choose another video" : "Select video"}
          variant="secondary"
          disabled={busy}
          busy={operation === "selecting"}
          busyLabel="Opening files…"
          onPress={select}
        />
        <AppText variant="caption" tone="secondary">
          Keep the video available on this device until the upload finishes.
        </AppText>
      </Surface>
      {progress && (
        <View className="gap-4">
          <ProgressMeter value={percentage} label="Uploaded video parts" />
          <AppText variant="caption" tone="secondary">
            {percentage}% · {sizeLabel(progress.completedBytes)} /{" "}
            {sizeLabel(progress.totalBytes)}
          </AppText>
          <AppText variant="label" accessibilityLiveRegion="polite">
            {statusLabels[progress.phase]}
          </AppText>
          <AppText variant="caption" tone="secondary">
            Progress counts confirmed parts. Upload is complete only after
            finalization.
          </AppText>
        </View>
      )}
      {!!message && (
        <AppText variant="caption" accessibilityLiveRegion="polite">
          {message}
        </AppText>
      )}
      {!complete && (
        <ActionButton
          label="Upload video"
          disabled={busy || !selected}
          busy={operation === "uploading"}
          busyLabel="Uploading video…"
          onPress={start}
        />
      )}
      {operation === "uploading" && (
        <ActionButton
          label="Cancel upload"
          variant="danger"
          onPress={() => controller.current?.abort()}
        />
      )}
      <AppText variant="caption" tone="secondary">
        Keep the app in the foreground. Leaving it stops the upload. Failed
        uploads start fresh; resume is not supported. Uploads are linked to your
        signed-in account. Validation and clip analysis are not available yet.
      </AppText>
    </View>
  );
}
