import type { MediaAssetStatus } from "../api/generated";

const rejectionMessages: Record<string, string> = {
  FILE_TOO_LARGE: "The video exceeds the 2 GB size limit.",
  SIZE_MISMATCH: "The stored video's size did not match the upload.",
  INVALID_MEDIA: "The file could not be read as a supported video.",
  INVALID_MEDIA_METADATA:
    "The video has invalid or incomplete media information.",
  UNSUPPORTED_MEDIA: "Choose an MP4, MOV, or WebM video.",
  UNSUPPORTED_VIDEO_CODEC: "This video's encoding is not supported.",
  UNSUPPORTED_AUDIO_CODEC: "This video's audio encoding is not supported.",
  DURATION_LIMIT: "The video exceeds the two-hour duration limit.",
  RESOLUTION_LIMIT: "The video's resolution exceeds the supported limit.",
  VIDEO_REQUIRED: "The file must contain a video track.",
  AUDIO_REQUIRED: "The video must contain an audio track.",
};

export function mediaStatusCopy(status: MediaAssetStatus | null) {
  switch (status?.status) {
    case "uploaded_unvalidated":
      return {
        title: "Waiting for validation",
        detail:
          "Your upload is complete. Validation has not finished. Workers may be disabled or busy; there is no completion-time estimate.",
      };
    case "validating":
      return {
        title: "Validating video",
        detail:
          "The worker is checking the video. Your original stays untouched.",
      };
    case "ready":
      return {
        title: "Validation passed",
        detail:
          "The video passed validation. This does not mean playback or clips are available.",
      };
    case "rejected":
      return {
        title: "Video rejected",
        detail:
          status.failureCode &&
          Object.hasOwn(rejectionMessages, status.failureCode)
            ? rejectionMessages[status.failureCode]!
            : "The video did not meet the supported media requirements.",
      };
    case "validation_failed":
      return {
        title: "Validation failed",
        detail:
          "The worker could not finish validation. This is not an upload failure. Checking status does not restart the worker.",
      };
    default:
      return {
        title: "Validation status not checked",
        detail:
          "Upload complete. Check status to see whether validation has started.",
      };
  }
}
