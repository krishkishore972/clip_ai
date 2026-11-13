import { useAuth, useClerk } from "@clerk/expo";
import { useHostedAuth } from "@clerk/expo/hosted-auth";
import { isRunningInExpoGo } from "expo";
import { getBundleOrigin } from "expo/internal/bundle-origin";
import { getInitialURL, getLinkingURL } from "expo-linking";
import { useEffect, useState, type ReactNode } from "react";
import { ActivityIndicator, Button, StyleSheet, View } from "react-native";

import { ThemedText } from "./themed-text";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import type { Me } from "@/lib/api/generated";
import { expoGoCallback, expoGoCallbackFromHost } from "@/lib/auth/expo-go-callback";

export function AuthGate({ children }: { children: ReactNode }) {
  const { isLoaded, isSignedIn, sessionId, getToken } = useAuth();
  const { signOut } = useClerk();
  const { startHostedAuth } = useHostedAuth();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [account, setAccount] = useState<{
    sessionId: string;
    me?: Me;
    error?: string;
  } | null>(null);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || !sessionId) return;
    const active = new AbortController();
    // Provision/resolve the application user before exposing private screens.
    void (async () => {
      try {
        const me = await configuredUploadApi(getToken).me(active.signal);
        if (!active.signal.aborted) setAccount({ sessionId, me });
      } catch {
        if (!active.signal.aborted)
          setAccount({
            sessionId,
            error:
              "Could not load your account. Check the API configuration or sign in again.",
          });
      }
    })();
    return () => active.abort();
  }, [isLoaded, isSignedIn, sessionId, getToken, attempt]);

  async function authenticate() {
    setBusy(true);
    setMessage("");
    let stage = "callback_lookup";
    function reportFailure(code: string) {
      const text = `Sign-in did not complete (${code}).`;
      setMessage(text);
      // Fixed diagnostic codes only: never log callback URLs or SDK errors.
      console.warn(text);
    }
    try {
      let redirectUrl: string | undefined;
      if (isRunningInExpoGo()) {
        // Expo Go handles exp:// links, not this project's mobile:// scheme.
        // Return to the existing root screen; the SDK validates and redeems
        // the callback before activating the session.
        const callback =
          expoGoCallback(getLinkingURL()) ??
          expoGoCallback(await getInitialURL()) ??
          // SDK 58's own expo-linking uses this origin rather than manifest
          // metadata, which may be absent or disagree with a proxy/tunnel.
          expoGoCallbackFromHost(getBundleOrigin() ?? undefined);
        if (!callback) {
          reportFailure("expo_go_callback_unavailable");
          return;
        }
        redirectUrl = callback;
      }
      stage = "hosted_auth";
      const result = await startHostedAuth(
        redirectUrl ? { redirectUrl } : undefined,
      );
      if (result.createdSessionId) return;
      if (!result.authSessionResult) {
        reportFailure("clerk_not_ready");
      } else if (result.authSessionResult.type === "cancel") {
        reportFailure("browser_cancelled");
      } else if (result.authSessionResult.type === "dismiss") {
        reportFailure("browser_dismissed");
      } else {
        reportFailure("session_not_created");
      }
    } catch {
      reportFailure(
        stage === "callback_lookup"
          ? "callback_lookup_failed"
          : "hosted_auth_failed",
      );
    } finally {
      setBusy(false);
    }
  }

  async function logout() {
    setBusy(true);
    setMessage("");
    try {
      await signOut();
      setAccount(null);
    } catch {
      setMessage("Could not sign out. Please try again.");
    } finally {
      setBusy(false);
    }
  }

  if (!isLoaded)
    return (
      <View style={styles.center}>
        <ActivityIndicator />
        <ThemedText>Loading session…</ThemedText>
      </View>
    );
  if (!isSignedIn)
    return (
      <View style={styles.center}>
        <ThemedText type="title">Chorus Clip</ThemedText>
        <ThemedText>Sign in to upload and manage your videos.</ThemedText>
        <Button
          title={busy ? "Opening sign-in…" : "Sign in / Create account"}
          disabled={busy}
          onPress={authenticate}
        />
        {!!message && (
          <ThemedText accessibilityRole="alert">{message}</ThemedText>
        )}
      </View>
    );
  const current = account?.sessionId === sessionId ? account : null;
  return (
    <View style={styles.container}>
      <View style={styles.bar}>
        <ThemedText>Signed in</ThemedText>
        <Button title="Sign out" disabled={busy} onPress={logout} />
      </View>
      {!!message && (
        <ThemedText accessibilityRole="alert">{message}</ThemedText>
      )}
      {current?.me ? (
        <View key={sessionId} style={styles.container}>
          {children}
        </View>
      ) : (
        <View style={styles.center}>
          {current?.error ? (
            <>
              <ThemedText accessibilityRole="alert">{current.error}</ThemedText>
              <Button
                title="Retry account setup"
                onPress={() => {
                  setAccount(null);
                  setAttempt((value) => value + 1);
                }}
              />
            </>
          ) : (
            <ActivityIndicator />
          )}
        </View>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  center: {
    flex: 1,
    gap: 16,
    padding: 24,
    alignItems: "center",
    justifyContent: "center",
  },
  bar: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    padding: 16,
  },
});
