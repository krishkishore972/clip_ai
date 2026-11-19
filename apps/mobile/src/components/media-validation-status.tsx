import { useAuth } from "@clerk/expo";
import { useFocusEffect } from "expo-router";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";

import { ActionButton, AppText, StatusBadge, Surface } from "@/design-system";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import { mediaStatusCopy } from "@/lib/uploads/media-status-copy";
import { createMediaStatusPoller } from "@/lib/uploads/media-status-poller";

export function MediaValidationStatus({ assetId }: { assetId: string }) {
  const { getToken, isSignedIn, sessionId } = useAuth();
  if (!isSignedIn || !sessionId) return null;
  return (
    <ValidationStatusCard
      key={`${sessionId}:${assetId}`}
      assetId={assetId}
      getToken={getToken}
    />
  );
}

function ValidationStatusCard({
  assetId,
  getToken,
}: {
  assetId: string;
  getToken: () => Promise<string | null>;
}) {
  const [poller] = useState(() =>
    createMediaStatusPoller((signal) =>
      configuredUploadApi(getToken).mediaStatus(assetId, signal),
    ),
  );
  // Clerk changes this function's identity on render. Replace only the reader,
  // without recreating the controller or starting another polling request.
  useEffect(() => {
    poller.setRead((signal) =>
      configuredUploadApi(getToken).mediaStatus(assetId, signal),
    );
  }, [assetId, getToken, poller]);
  const snapshot = useSyncExternalStore(
    poller.subscribe,
    poller.getSnapshot,
    poller.getSnapshot,
  );

  useFocusEffect(
    useCallback(() => {
      poller.setEnabled(AppState.currentState === "active");
      const subscription = AppState.addEventListener("change", (state) => {
        poller.setEnabled(state === "active");
      });
      return () => {
        subscription.remove();
        poller.setEnabled(false);
      };
    }, [poller]),
  );

  const copy = mediaStatusCopy(snapshot.status);
  return (
    <Surface>
      <AppText variant="heading" accessibilityRole="header">
        Video validation
      </AppText>
      <StatusBadge
        label={copy.title}
        tone={
          snapshot.status?.status === "ready"
            ? "success"
            : snapshot.status?.status === "rejected" ||
                snapshot.status?.status === "validation_failed"
              ? "danger"
              : "secondary"
        }
      />
      <AppText accessibilityLiveRegion="polite">{copy.detail}</AppText>
      <AppText variant="caption" tone="secondary">
        {snapshot.status
          ? `Validation attempts: ${snapshot.status.attempts} / 4`
          : "Validation attempts: not checked"}
      </AppText>
      {snapshot.stale && (
        <AppText variant="caption">
          Last known status · may be out of date
        </AppText>
      )}
      {snapshot.error && (
        <AppText variant="caption" accessibilityLiveRegion="polite">
          Status check problem: {snapshot.error} Your upload is still complete.
        </AppText>
      )}
      <ActionButton
        label="Check status"
        variant="secondary"
        busy={snapshot.checking}
        busyLabel="Checking status…"
        disabled={snapshot.authenticationRequired}
        onPress={poller.check}
      />
      <AppText variant="caption" tone="secondary">
        Checks only read status; they never restart validation or upload again.
      </AppText>
    </Surface>
  );
}
