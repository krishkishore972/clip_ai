import { useAuth, useClerk } from "@clerk/expo";
import { useHostedAuth } from "@clerk/expo/hosted-auth";
import { useEffect, useState, type ReactNode } from "react";
import { ActivityIndicator, Button, StyleSheet, View } from "react-native";

import { ThemedText } from "./themed-text";
import { configuredUploadApi } from "@/lib/api/mobile-client";
import type { Me } from "@/lib/api/generated";

export function AuthGate({ children }: { children: ReactNode }) {
  const { isLoaded, isSignedIn, sessionId, getToken } = useAuth();
  const { signOut } = useClerk();
  const { startHostedAuth } = useHostedAuth();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [account, setAccount] = useState<{ sessionId: string; me?: Me; error?: string } | null>(null);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || !sessionId) return;
    const active = new AbortController();
    // Provision/resolve the application user before exposing private screens.
    void (async () => {
      try {
        const me = await configuredUploadApi(getToken).me(active.signal);
        if (!active.signal.aborted) setAccount({ sessionId, me });
      } catch {
        if (!active.signal.aborted) setAccount({ sessionId, error: "Could not load your account. Check the API configuration or sign in again." });
      }
    })();
    return () => active.abort();
  }, [isLoaded, isSignedIn, sessionId, getToken, attempt]);

  async function authenticate() {
    setBusy(true);
    setMessage("");
    try { await startHostedAuth(); }
    catch { setMessage("Sign-in failed. Check Clerk Native API, Google connection, and callback configuration."); }
    finally { setBusy(false); }
  }

  async function logout() {
    setBusy(true);
    setMessage("");
    try { await signOut(); setAccount(null); }
    catch { setMessage("Could not sign out. Please try again."); }
    finally { setBusy(false); }
  }

  if (!isLoaded) return <View style={styles.center}><ActivityIndicator /><ThemedText>Loading session…</ThemedText></View>;
  if (!isSignedIn) return (
    <View style={styles.center}>
      <ThemedText type="title">Chorus Clip</ThemedText>
      <ThemedText>Sign in to upload and manage your videos.</ThemedText>
      <Button title={busy ? "Opening sign-in…" : "Sign in / Create account"} disabled={busy} onPress={authenticate} />
      {!!message && <ThemedText accessibilityRole="alert">{message}</ThemedText>}
    </View>
  );
  const current = account?.sessionId === sessionId ? account : null;
  return (
    <View style={styles.container}>
      <View style={styles.bar}>
        <ThemedText>Signed in</ThemedText>
        <Button title="Sign out" disabled={busy} onPress={logout} />
      </View>
      {!!message && <ThemedText accessibilityRole="alert">{message}</ThemedText>}
      {current?.me ? <View key={sessionId} style={styles.container}>{children}</View> : (
        <View style={styles.center}>
          {current?.error ? <><ThemedText accessibilityRole="alert">{current.error}</ThemedText><Button title="Retry account setup" onPress={() => { setAccount(null); setAttempt(value => value + 1); }} /></> : <ActivityIndicator />}
        </View>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  center: { flex: 1, gap: 16, padding: 24, alignItems: "center", justifyContent: "center" },
  bar: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", padding: 16 },
});
