import { useAuth, useClerk } from "@clerk/expo";
import { useHostedAuth } from "@clerk/expo/hosted-auth";
import { isRunningInExpoGo } from "expo";
import { getBundleOrigin } from "expo/internal/bundle-origin";
import { getInitialURL, getLinkingURL } from "expo-linking";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { styled } from "nativewind";
import { ActivityIndicator, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { ActionButton, AppText, Surface } from "@/design-system";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import type { Me } from "@/lib/api/generated";
import {
  expoGoCallback,
  expoGoCallbackFromHost,
} from "@/lib/auth/expo-go-callback";

const AuthSafeArea = styled(SafeAreaView, { className: "style" });

function AuthScreen({ children }: { children: ReactNode }) {
  return (
    <AuthSafeArea className="flex-1 bg-background">
      <ScrollView contentContainerClassName="grow w-full max-w-[640px] self-center gap-8 px-6 py-8">
        {children}
      </ScrollView>
    </AuthSafeArea>
  );
}

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
  // Clerk's getToken is a new function on every render, so it can never be an
  // effect dependency without re-firing the effect forever. Keep the latest in a ref.
  const getTokenRef = useRef(getToken);
  // One provisioning request per (session, retry) pair. Without this the effect
  // would call GET /me again after every successful account update.
  const provisioned = useRef<string | null>(null);

  useEffect(() => {
    getTokenRef.current = getToken;
  }, [getToken]);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || !sessionId) {
      // Signed out: allow the next sign-in to provision again.
      provisioned.current = null;
      return;
    }
    const key = `${sessionId}#${attempt}`;
    if (provisioned.current === key) return;
    provisioned.current = key;
    const active = new AbortController();
    // Provision/resolve the application user before exposing private screens.
    void (async () => {
      try {
        const me = await configuredUploadApi(() => getTokenRef.current()).me(
          active.signal,
        );
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
  }, [isLoaded, isSignedIn, sessionId, attempt]);

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
      <AuthScreen>
        <AppText variant="heading">Chorus Clip</AppText>
        <Surface>
          <ActivityIndicator color="#F0F424" />
          <AppText accessibilityLiveRegion="polite">
            Loading your session…
          </AppText>
        </Surface>
      </AuthScreen>
    );
  if (!isSignedIn)
    return (
      <AuthScreen>
        <View className="flex-row items-center justify-between gap-4">
          <AppText variant="heading">Chorus Clip</AppText>
          <View className="rounded-full bg-primary px-4 py-2">
            <AppText variant="label" className="text-primary-foreground">
              YOUR CREATIVE SPACE
            </AppText>
          </View>
        </View>
        <View className="gap-4 pt-6">
          <AppText variant="display" accessibilityRole="header">
            Your story.{"\n"}Starts here.
          </AppText>
          <AppText tone="secondary">
            A private home for your videos. Bring your original. Make room for
            what comes next.
          </AppText>
        </View>
        <View className="gap-6 rounded-[32px] bg-accent p-6">
          <AppText variant="label" className="text-accent-foreground">
            01 / BRING YOUR VIDEO
          </AppText>
          <AppText variant="title" className="text-accent-foreground">
            Keep the original.{"\n"}Keep it yours.
          </AppText>
          <View className="self-start rounded-full bg-highlight px-4 py-2">
            <AppText variant="label" className="text-highlight-foreground">
              Private uploads
            </AppText>
          </View>
        </View>
        <View className="gap-4">
          <ActionButton
            label="Sign in / Create account"
            busy={busy}
            busyLabel="Opening sign-in…"
            onPress={authenticate}
          />
          <AppText variant="caption" tone="secondary" className="text-center">
            Continue securely in your browser, then return here.
          </AppText>
          {!!message && (
            <AppText tone="danger" accessibilityRole="alert">
              {message}
            </AppText>
          )}
        </View>
      </AuthScreen>
    );
  const current = account?.sessionId === sessionId ? account : null;
  return (
    <View className="flex-1 bg-background">
      <AuthSafeArea edges={["top", "left", "right"]} className="bg-background">
        <View className="flex-row flex-wrap items-center justify-between gap-4 px-6 py-4">
          <AppText variant="heading">Chorus Clip</AppText>
          <ActionButton
            label="Sign out"
            variant="secondary"
            disabled={busy}
            onPress={logout}
          />
        </View>
      </AuthSafeArea>
      {!!message && (
        <AppText tone="danger" className="px-6" accessibilityRole="alert">
          {message}
        </AppText>
      )}
      {current?.me ? (
        <View key={sessionId} className="flex-1">
          {children}
        </View>
      ) : (
        <AuthScreen>
          <AppText variant="title">Getting your space ready.</AppText>
          {current?.error ? (
            <>
              <AppText tone="danger" accessibilityRole="alert">
                {current.error}
              </AppText>
              <ActionButton
                label="Retry account setup"
                onPress={() => {
                  setAccount(null);
                  setAttempt((value) => value + 1);
                }}
              />
            </>
          ) : (
            <Surface>
              <ActivityIndicator color="#F0F424" />
              <AppText accessibilityLiveRegion="polite">
                Connecting to your account…
              </AppText>
              <AppText variant="caption" tone="secondary">
                Your private workspace must load before uploading.
              </AppText>
            </Surface>
          )}
        </AuthScreen>
      )}
    </View>
  );
}
