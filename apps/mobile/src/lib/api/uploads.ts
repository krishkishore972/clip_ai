import type {
  CompleteUploadPart,
  CreateUploadRequest,
  SignedUploadPart,
  SignedUploadParts,
  UploadSession,
  Me,
  MediaAssetStatus,
  MediaValidationState,
} from "./generated";

export class AuthenticationRequired extends Error {
  constructor() {
    super("Your session expired. Sign out and sign in again.");
  }
}

export class MediaStatusReadError extends Error {
  constructor(kind: "unavailable" | "temporary" | "invalid") {
    super(
      kind === "unavailable"
        ? "This video's status is unavailable or the video was not found."
        : kind === "temporary"
          ? "Status is temporarily unavailable. Check again later."
          : "Could not read a valid video status. Check again later.",
    );
  }
}

const validationStates: readonly MediaValidationState[] = [
  "uploaded_unvalidated",
  "validating",
  "ready",
  "rejected",
  "validation_failed",
];

// Require a real RFC3339 calendar timestamp, not Date.parse's normalized dates.
export function validTimestamp(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match =
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(
      value,
    );
  if (!match) return false;
  const [, year, month, day, hour, minute, second, zone] = match;
  const y = Number(year);
  const leapYear = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  const days =
    [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][
      Number(month) - 1
    ] ?? 0;
  return (
    Number(month) >= 1 &&
    Number(month) <= 12 &&
    Number(day) >= 1 &&
    Number(day) <= days &&
    Number(hour) <= 23 &&
    Number(minute) <= 59 &&
    Number(second) <= 59 &&
    (zone === "Z" ||
      (Number(zone?.slice(1, 3)) <= 23 && Number(zone?.slice(4)) <= 59)) &&
    Number.isFinite(Date.parse(value))
  );
}

export function parseMediaStatus(
  value: unknown,
  assetId: string,
): MediaAssetStatus {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new MediaStatusReadError("invalid");
  const data = value as Record<string, unknown>;
  if (
    data.assetId !== assetId ||
    !/^[A-Za-z0-9_.:-]{1,128}$/.test(assetId) ||
    !validationStates.includes(data.status as MediaValidationState) ||
    !Number.isInteger(data.attempts) ||
    Number(data.attempts) < 0 ||
    Number(data.attempts) > 4 ||
    !Number.isInteger(data.maxAttempts) ||
    Number(data.maxAttempts) < 1 ||
    Number(data.maxAttempts) > 2 ||
    !(
      data.failureCode === null ||
      (typeof data.failureCode === "string" &&
        /^[A-Z][A-Z0-9_]{0,63}$/.test(data.failureCode))
    ) ||
    !validTimestamp(data.updatedAt) ||
    !(data.validatedAt === null || validTimestamp(data.validatedAt))
  )
    throw new MediaStatusReadError("invalid");
  // Return only the public contract fields, never extra provider data.
  return {
    assetId,
    status: data.status as MediaValidationState,
    attempts: data.attempts as number,
    maxAttempts: data.maxAttempts as number,
    failureCode: data.failureCode as string | null,
    updatedAt: data.updatedAt,
    validatedAt: data.validatedAt as string | null,
  };
}

// Use fetch directly; tests supply an offline fetch instead of contacting AWS.
export function createUploadApi(
  baseUrl: string,
  fetcher: typeof fetch = fetch,
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
  const url = base.toString().replace(/\/$/, "");

  async function send<T>(
    target: string,
    init: RequestInit,
    signal: AbortSignal,
    consume: (response: Response) => Promise<T>,
    timeout = 30_000,
  ) {
    signal.throwIfAborted();
    const controller = new AbortController();
    const stop = () => controller.abort();
    signal.addEventListener("abort", stop, { once: true });
    const timer = setTimeout(stop, timeout);
    try {
      const response = await fetcher(target, {
        ...init,
        signal: controller.signal,
        redirect: "error",
      });
      return await consume(response);
    } catch (error) {
      if (
        error instanceof AuthenticationRequired ||
        error instanceof MediaStatusReadError
      )
        throw error;
      // Native network errors can contain secret signed URLs. Never display them.
      throw new Error(
        signal.aborted
          ? "Upload stopped."
          : "Request failed or timed out. Check your connection.",
      );
    } finally {
      clearTimeout(timer);
      signal.removeEventListener("abort", stop);
    }
  }

  async function authorization(signal: AbortSignal) {
    signal.throwIfAborted();
    let token: string | null;
    try {
      token = await getToken();
    } catch {
      throw new AuthenticationRequired();
    }
    signal.throwIfAborted();
    if (!token) throw new AuthenticationRequired();
    return { Authorization: `Bearer ${token}` };
  }

  async function request<T>(
    path: string,
    method: string,
    signal: AbortSignal,
    session?: UploadSession,
    body?: unknown,
  ): Promise<T> {
    return send(
      `${url}/uploads${path}`,
      {
        method,
        headers: {
          Accept: "application/json",
          ...(await authorization(signal)),
          ...(body === undefined ? {} : { "Content-Type": "application/json" }),
          ...(session ? { "Upload-Token": session.uploadToken } : {}),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      },
      signal,
      async (response) => {
        const expected = method === "DELETE" ? 204 : path === "" ? 201 : 200;
        if (response.status !== expected) {
          if (response.status === 401) throw new AuthenticationRequired();
          throw new Error(`Upload request failed (HTTP ${response.status}).`);
        }
        if (response.status === 204) return undefined as T;
        // Shapes come from our Go API contract; detailed client-side parsers are deferred.
        return (await response.json()) as T;
      },
    );
  }

  const path = (session: UploadSession) =>
    `/${encodeURIComponent(session.uploadId)}`;
  return {
    async mediaStatus(
      assetId: string,
      signal: AbortSignal,
    ): Promise<MediaAssetStatus> {
      if (!/^[A-Za-z0-9_.:-]{1,128}$/.test(assetId))
        throw new MediaStatusReadError("invalid");
      return send(
        `${url}/media/${encodeURIComponent(assetId)}/status`,
        {
          method: "GET",
          headers: {
            Accept: "application/json",
            ...(await authorization(signal)),
          },
        },
        signal,
        async (response) => {
          if (response.status === 401) throw new AuthenticationRequired();
          if (response.status === 404)
            throw new MediaStatusReadError("unavailable");
          if (response.status === 503)
            throw new MediaStatusReadError("temporary");
          if (response.status !== 200)
            throw new MediaStatusReadError("invalid");
          let data: unknown;
          try {
            data = await response.json();
          } catch {
            throw new MediaStatusReadError("invalid");
          }
          return parseMediaStatus(data, assetId);
        },
      );
    },
    async me(signal: AbortSignal): Promise<Me> {
      return send(
        `${url}/me`,
        {
          method: "GET",
          headers: {
            Accept: "application/json",
            ...(await authorization(signal)),
          },
        },
        signal,
        async (response) => {
          if (response.status === 401) throw new AuthenticationRequired();
          if (response.status !== 200)
            throw new Error("Could not load your account.");
          const data: Me = await response.json();
          if (typeof data?.userId !== "string" || data.userId.length === 0)
            throw new Error("Invalid account response.");
          return data;
        },
      );
    },
    create: (file: CreateUploadRequest, signal: AbortSignal) =>
      request<UploadSession>("", "POST", signal, undefined, file),
    async signPart(
      session: UploadSession,
      partNumber: number,
      signal: AbortSignal,
    ) {
      const result = await request<SignedUploadParts>(
        `${path(session)}/parts/sign`,
        "POST",
        signal,
        session,
        { partNumbers: [partNumber] },
      );
      const part = result.parts?.[0];
      if (result.parts?.length !== 1 || part?.partNumber !== partNumber)
        throw new Error("The server returned the wrong upload part.");
      let signedUrl: URL;
      try {
        signedUrl = new URL(part.url);
      } catch {
        throw new Error("The server returned an invalid part URL.");
      }
      if (
        signedUrl.protocol !== "https:" ||
        signedUrl.username ||
        signedUrl.password ||
        signedUrl.hash ||
        Object.keys(part.headers ?? {}).some((name) =>
          /^(upload-token|authorization|cookie|host)$/i.test(name),
        )
      )
        throw new Error(
          "The server returned invalid part upload instructions.",
        );
      return part;
    },
    async put(
      part: SignedUploadPart,
      bytes: Uint8Array<ArrayBuffer>,
      signal: AbortSignal,
    ) {
      const response = await send(
        part.url,
        { method: "PUT", headers: part.headers ?? {}, body: bytes },
        signal,
        async (response) => response,
        300_000,
      );
      if (!response.ok) {
        throw new Error(`Video part upload failed (HTTP ${response.status}).`);
      }
      const eTag = response.headers.get("ETag");
      if (!eTag)
        throw new Error("S3 did not return an ETag for the video part.");
      return eTag; // Preserve the quotes; Go/S3 need this exact value for completion.
    },
    complete: (
      session: UploadSession,
      parts: CompleteUploadPart[],
      signal: AbortSignal,
    ) =>
      request<UploadSession>(
        `${path(session)}/complete`,
        "POST",
        signal,
        session,
        { parts },
      ),
    abort: (session: UploadSession, signal: AbortSignal) =>
      request<void>(path(session), "DELETE", signal, session),
  };
}

export type UploadApi = ReturnType<typeof createUploadApi>;
