import type { MediaAssetStatus } from "../api/generated";
import { AuthenticationRequired, MediaStatusReadError } from "../api/uploads";

export type MediaStatusSnapshot = {
  status: MediaAssetStatus | null;
  checking: boolean;
  stale: boolean;
  error: string | null;
  authenticationRequired: boolean;
};

export const terminalMediaStatus = (status: MediaAssetStatus | null) =>
  status !== null &&
  ["ready", "rejected", "validation_failed"].includes(status.status);

// No upload capabilities: this controller can only read the selected asset.
// An aborted request retains the in-flight lock until it actually settles.
export function createMediaStatusPoller(
  read: (signal: AbortSignal) => Promise<MediaAssetStatus>,
  clock = {
    set: (callback: () => void) => setTimeout(callback, 5_000),
    clear: (timer: ReturnType<typeof setTimeout>) => clearTimeout(timer),
  },
) {
  let snapshot: MediaStatusSnapshot = {
    status: null,
    checking: false,
    stale: false,
    error: null,
    authenticationRequired: false,
  };
  const listeners = new Set<() => void>();
  let enabled = false;
  let generation = 0;
  let inflight: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let resumePending = false;

  function update(patch: Partial<MediaStatusSnapshot>) {
    snapshot = { ...snapshot, ...patch };
    listeners.forEach((listener) => listener());
  }
  function clearTimer() {
    if (timer !== undefined) clock.clear(timer);
    timer = undefined;
  }
  async function check() {
    if (!enabled || snapshot.authenticationRequired) return;
    if (inflight) return;
    clearTimer();
    const request = new AbortController();
    const version = generation;
    inflight = request;
    update({ checking: true });
    try {
      const status = await read(request.signal);
      if (version !== generation || request.signal.aborted) return;
      update({ status, stale: false, error: null });
    } catch (error) {
      if (version !== generation || request.signal.aborted) return;
      const auth = error instanceof AuthenticationRequired;
      update({
        stale: snapshot.status !== null,
        authenticationRequired: auth,
        error:
          auth || error instanceof MediaStatusReadError
            ? error.message
            : "Could not check status. Check your connection and try again.",
      });
    } finally {
      inflight = null;
      if (version === generation) update({ checking: false });
      if (resumePending) {
        resumePending = false;
        if (enabled) void check();
      } else if (
        enabled &&
        !snapshot.authenticationRequired &&
        !terminalMediaStatus(snapshot.status)
      ) {
        timer = clock.set(() => {
          timer = undefined;
          void check();
        });
      }
    }
  }
  return {
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setRead(next: (signal: AbortSignal) => Promise<MediaAssetStatus>) {
      read = next;
    },
    setEnabled(value: boolean) {
      if (enabled === value) return;
      enabled = value;
      generation++;
      clearTimer();
      if (!value) {
        resumePending = false;
        inflight?.abort();
        update({
          checking: false,
          stale:
            snapshot.stale ||
            (snapshot.status !== null && !terminalMediaStatus(snapshot.status)),
        });
      } else if (
        !snapshot.authenticationRequired &&
        !terminalMediaStatus(snapshot.status)
      ) {
        if (inflight) resumePending = true;
        else void check();
      }
    },
    check: () => {
      void check();
    },
  };
}
