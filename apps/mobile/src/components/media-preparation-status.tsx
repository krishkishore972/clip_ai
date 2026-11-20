import { useFocusEffect } from "expo-router";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";

import { ActionButton, AppText, StatusBadge, Surface } from "@/design-system";
import { configuredPreparationApi } from "@/lib/api/mobile-client";
import { preparationCopy } from "@/lib/uploads/preparation-copy";
import { createPreparationPoller } from "@/lib/uploads/preparation-poller";

export function MediaPreparationStatus({
  assetId,
  getToken,
}: {
  assetId: string;
  getToken: () => Promise<string | null>;
}) {
  const [poller] = useState(() =>
    createPreparationPoller(
      (signal) => configuredPreparationApi(getToken).status(assetId, signal),
      (signal) => configuredPreparationApi(getToken).ensure(assetId, signal),
    ),
  );
  useEffect(() => {
    poller.setRead((signal) =>
      configuredPreparationApi(getToken).status(assetId, signal),
    );
    poller.setEnsure((signal) =>
      configuredPreparationApi(getToken).ensure(assetId, signal),
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
      const subscription = AppState.addEventListener("change", (state) =>
        poller.setEnabled(state === "active"),
      );
      return () => {
        subscription.remove();
        poller.setEnabled(false);
      };
    }, [poller]),
  );
  const copy = preparationCopy(snapshot.status);
  return (
    <Surface>
      <AppText variant="heading" accessibilityRole="header">
        Media preparation
      </AppText>
      <StatusBadge
        label={copy.title}
        tone={
          snapshot.status?.status === "ready"
            ? "success"
            : snapshot.status?.status === "failed"
              ? "danger"
              : "secondary"
        }
      />
      <AppText accessibilityLiveRegion="polite">{copy.detail}</AppText>
      <AppText variant="caption" tone="secondary">
        Preparation attempts:{" "}
        {snapshot.status ? `${snapshot.status.attempts} / 2` : "not checked"}
      </AppText>
      {snapshot.stale && (
        <AppText variant="caption">
          Last known preparation status · may be out of date
        </AppText>
      )}
      {snapshot.error && (
        <AppText accessibilityLiveRegion="polite">
          Preparation check problem: {snapshot.error} Your upload is still
          complete.
        </AppText>
      )}
      <ActionButton
        label="Check preparation"
        variant="secondary"
        busy={snapshot.checking}
        busyLabel="Checking preparation…"
        disabled={snapshot.authenticationRequired}
        onPress={poller.check}
      />
      {snapshot.status?.status === "not_prepared" && (
        <ActionButton
          label="Prepare media"
          variant="secondary"
          busy={snapshot.checking}
          busyLabel="Requesting preparation…"
          disabled={snapshot.authenticationRequired}
          onPress={poller.prepare}
        />
      )}
      <AppText variant="caption" tone="secondary">
        Preparation is separate from validation. No project, transcript or clips
        are created here.
      </AppText>
    </Surface>
  );
}
