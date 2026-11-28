import type { CurationRequest } from "../schemas/curation.js";

// Call only after request validation: position is the original contiguous ID.
// Keep the authoritative objects for response validation, not in the prompt.
export function compactCurationInput(request: CurationRequest) {
  return {
    transcriptHash: request.transcriptHash,
    words: request.words.map((word) => word.text),
    startsMs: request.words.map((word) => word.startMs),
    endsMs: request.words.map((word) => word.endMs),
    constraints: request.constraints,
  };
}
