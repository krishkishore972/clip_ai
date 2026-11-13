import type {
  CompleteUploadPart,
  CreateUploadRequest,
  SignedUploadPart,
  SignedUploadParts,
  UploadSession,
  Me,
} from "./generated";

export class AuthenticationRequired extends Error {
  constructor() {
    super("Your session expired. Sign out and sign in again.");
  }
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
      if (error instanceof AuthenticationRequired) throw error;
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
          if (
            typeof data?.userId !== "string" ||
            typeof data?.workspaceId !== "string"
          )
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
