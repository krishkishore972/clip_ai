import type {
  MediaPreparationState,
  MediaPreparationStatus,
} from "./generated";
import { AuthenticationRequired, validTimestamp } from "./uploads";

export class PreparationReadError extends Error {
  constructor(
    readonly kind: "unavailable" | "temporary" | "invalid" | "not_ready",
  ) {
    super(
      kind === "unavailable"
        ? "This video is unavailable."
        : kind === "not_ready"
          ? "Media preparation is not ready yet. Check preparation status."
          : kind === "temporary"
            ? "Media preparation is temporarily unavailable. Try again later."
            : "Could not read a valid preparation response. Try again later.",
    );
  }
}
const validId = (value: string) => /^[A-Za-z0-9_.:-]{1,128}$/.test(value);
function invalid(): never {
  throw new PreparationReadError("invalid");
}
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return invalid();
  return value as Record<string, unknown>;
}
const states: readonly MediaPreparationState[] = [
  "not_prepared",
  "pending",
  "preparing",
  "ready",
  "failed",
];
export function parsePreparationStatus(
  value: unknown,
  assetId: string,
): MediaPreparationStatus {
  const d = object(value);
  if (
    !validId(assetId) ||
    d.assetId !== assetId ||
    !states.includes(d.status as MediaPreparationState) ||
    !Number.isInteger(d.attempts) ||
    Number(d.attempts) < 0 ||
    Number(d.attempts) > 2 ||
    !validTimestamp(d.updatedAt) ||
    !(d.preparedAt === null || validTimestamp(d.preparedAt)) ||
    !(
      d.failureCode === null ||
      (typeof d.failureCode === "string" &&
        /^[A-Z][A-Z0-9_]{0,63}$/.test(d.failureCode))
    )
  )
    return invalid();
  const ready = d.status === "ready";
  if (
    ready
      ? d.preparedAt === null ||
        d.failureCode !== null ||
        Number(d.attempts) < 1
      : d.preparedAt !== null
  )
    return invalid();
  if (
    (d.status === "not_prepared" &&
      (d.attempts !== 0 || d.failureCode !== null)) ||
    (d.status === "preparing" && Number(d.attempts) < 1) ||
    (d.status === "failed" && d.failureCode === null)
  )
    return invalid();
  if (
    d.preparedAt !== null &&
    Date.parse(d.preparedAt as string) > Date.parse(d.updatedAt)
  )
    return invalid();
  return {
    assetId,
    status: d.status as MediaPreparationState,
    attempts: d.attempts as number,
    failureCode: d.failureCode as string | null,
    updatedAt: d.updatedAt,
    preparedAt: d.preparedAt as string | null,
  };
}

// Deliberately has no UploadSession, upload capability, or S3 fetch method.
export function createPreparationApi(
  baseUrl: string,
  fetcher: typeof fetch,
  getToken: () => Promise<string | null>,
) {
  let base: URL;
  try {
    base = new URL(baseUrl.trim());
    if (
      !/^https?:$/.test(base.protocol) ||
      base.username ||
      base.password ||
      base.search ||
      base.hash
    )
      throw new Error();
  } catch {
    throw new Error(
      "Set EXPO_PUBLIC_API_URL to your device-reachable Go API URL.",
    );
  }
  async function request<T>(
    assetId: string,
    endpoint: "preparation",
    method: "GET" | "POST",
    signal: AbortSignal,
    parse: (data: unknown) => T,
  ): Promise<T> {
    if (!validId(assetId)) return invalid();
    signal.throwIfAborted();
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal.addEventListener("abort", abort, { once: true });
    const timer = setTimeout(abort, 30_000);
    try {
      let token: string | null;
      try {
        token = await getToken();
      } catch {
        throw new AuthenticationRequired();
      }
      controller.signal.throwIfAborted();
      if (!token) throw new AuthenticationRequired();
      const response = await fetcher(
        `${base.toString().replace(/\/$/, "")}/media/${encodeURIComponent(assetId)}/${endpoint}`,
        {
          method,
          headers: {
            Accept: "application/json",
            Authorization: `Bearer ${token}`,
          },
          credentials: "omit",
          cache: "no-store",
          redirect: "error",
          signal: controller.signal,
        },
      );
      if (response.status === 401) throw new AuthenticationRequired();
      if (response.status === 404)
        throw new PreparationReadError("unavailable");
      if (response.status === 409) throw new PreparationReadError("not_ready");
      if (response.status === 503) throw new PreparationReadError("temporary");
      if (response.status !== (method === "POST" ? 202 : 200)) return invalid();
      return parse(await response.json());
    } catch (error) {
      if (
        error instanceof AuthenticationRequired ||
        error instanceof PreparationReadError
      )
        throw error;
      throw new PreparationReadError("temporary");
    } finally {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
    }
  }
  return {
    status: (assetId: string, signal: AbortSignal) =>
      request(assetId, "preparation", "GET", signal, (data) =>
        parsePreparationStatus(data, assetId),
      ),
    ensure: (assetId: string, signal: AbortSignal) =>
      request(assetId, "preparation", "POST", signal, (data) =>
        parsePreparationStatus(data, assetId),
      ),
  };
}
