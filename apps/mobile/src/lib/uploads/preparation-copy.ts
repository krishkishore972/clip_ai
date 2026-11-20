import type { MediaPreparationStatus } from "../api/generated";

export function preparationCopy(value: MediaPreparationStatus | null) {
  switch (value?.status) {
    case "not_prepared":
      return {
        title: "Not prepared",
        detail:
          "This older video has no media preparation. You can prepare it once below.",
      };
    case "pending":
      return {
        title: "Queued",
        detail: "Validation passed. Audio preparation is queued.",
      };
    case "preparing":
      return {
        title: "Preparing",
        detail:
          "Extracting normalized audio for transcription. Your original stays unchanged.",
      };
    case "ready":
      return {
        title: "Preparation ready",
        detail:
          "Normalized audio is ready. Transcription will use it in a later step.",
      };
    case "failed":
      return {
        title: "Preparation failed",
        detail:
          "We could not prepare this video. Checking status does not restart preparation or upload again.",
      };
    default:
      return {
        title: "Checking preparation",
        detail:
          "Your upload and validation are complete. Checking separate media preparation status.",
      };
  }
}
