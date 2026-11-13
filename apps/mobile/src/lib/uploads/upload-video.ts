import type {
  CompleteUploadPart,
  CreateUploadRequest,
  UploadSession,
} from "../api/generated";
import type { UploadApi } from "../api/uploads";

export interface ChunkSource {
  sizeBytes: number;
  read(offset: number, length: number): Promise<Uint8Array<ArrayBuffer>>;
  close(): void;
}

export interface UploadProgress {
  phase: "creating" | "uploading" | "completing" | "complete";
  completedBytes: number;
  totalBytes: number;
}

export function videoMetadata(
  filename: string,
  sizeBytes: number,
): CreateUploadRequest {
  const types: Record<string, CreateUploadRequest["contentType"]> = {
    mp4: "video/mp4",
    mov: "video/quicktime",
    webm: "video/webm",
  };
  const extension = filename.split(".").at(-1)?.toLowerCase() ?? "";
  if (
    !Object.hasOwn(types, extension) ||
    new TextEncoder().encode(filename).length > 255 ||
    /[/\\\u0000-\u001f\u007f]/.test(filename)
  )
    throw new Error("Choose an MP4, MOV, or WebM video with a valid filename.");
  if (
    !Number.isSafeInteger(sizeBytes) ||
    sizeBytes <= 0 ||
    sizeBytes > 2_000_000_000
  )
    throw new Error(
      "Choose a readable, non-empty video no larger than 2,000,000,000 bytes.",
    );
  return { filename, sizeBytes, contentType: types[extension]! };
}

export async function uploadVideo({
  api,
  source,
  file,
  signal,
  onProgress,
}: {
  api: UploadApi;
  source: ChunkSource;
  file: CreateUploadRequest;
  signal: AbortSignal;
  onProgress: (progress: UploadProgress) => void;
}): Promise<UploadSession> {
  let session: UploadSession | undefined;
  let completedBytes = 0;
  let completing = false;
  const progress = (phase: UploadProgress["phase"]) =>
    onProgress({ phase, completedBytes, totalBytes: file.sizeBytes });

  try {
    // Close the reader before attempting remote cleanup, including on cancellation.
    try {
      videoMetadata(file.filename, file.sizeBytes);
      if (source.sizeBytes !== file.sizeBytes)
        throw new Error("The selected video changed. Select it again.");
      signal.throwIfAborted();
      progress("creating");
      session = await api.create(file, signal); // Never automatically retry create.
      signal.throwIfAborted();
      if (
        !session.uploadId ||
        !session.uploadToken ||
        session.status !== "uploading" ||
        !Number.isSafeInteger(session.partSizeBytes) ||
        session.partSizeBytes <= 0 ||
        session.partSizeBytes > 16 * 1024 * 1024 ||
        session.partCount !== Math.ceil(file.sizeBytes / session.partSizeBytes)
      )
        throw new Error("The server returned an invalid upload plan.");

      const parts: CompleteUploadPart[] = [];
      progress("uploading");
      for (let partNumber = 1; partNumber <= session.partCount; partNumber++) {
        signal.throwIfAborted();
        const offset = (partNumber - 1) * session.partSizeBytes;
        const length = Math.min(session.partSizeBytes, file.sizeBytes - offset);
        const bytes = await source.read(offset, length);
        signal.throwIfAborted();
        if (bytes.byteLength !== length)
          throw new Error("Could not read the expected video bytes.");
        const signed = await api.signPart(session, partNumber, signal);
        signal.throwIfAborted();
        const eTag = await api.put(signed, bytes, signal);
        parts.push({ partNumber, eTag });
        completedBytes += length;
        progress("uploading");
      }

      signal.throwIfAborted();
      progress("completing");
      completing = true;
      const completed = await api.complete(session, parts, signal);
      if (
        completed.status !== "uploaded_unvalidated" ||
        completed.uploadId !== session.uploadId
      )
        throw new Error("Upload completion was not confirmed.");
      progress("complete");
      return completed;
    } finally {
      source.close();
    }
  } catch (error) {
    let cleanup = "";
    if (session) {
      try {
        await api.abort(session, new AbortController().signal);
      } catch {
        cleanup =
          " S3 cleanup was not confirmed; unfinished parts may need bucket lifecycle cleanup.";
      }
    } else {
      cleanup =
        " If creation reached the server, unfinished parts may need bucket lifecycle cleanup.";
    }
    if (completing)
      throw new Error(
        `Completion was not confirmed. The video may already be stored; it was not deleted.${cleanup}`,
      );
    const message = signal.aborted
      ? "Upload stopped."
      : error instanceof Error
        ? error.message
        : "Video upload failed.";
    throw new Error(message + cleanup);
  }
}
