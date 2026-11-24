// Generated from apps/api/openapi/openapi.yaml. Do not edit.
// Regenerate: pnpm --filter llm gen:api

export type CurationRequestDTO = {
  "transcriptHash": string;
  "words": ({
  "index": number;
  "text": string;
  "startMs": number;
  "endMs": number;
})[];
  "constraints": {
  "candidateCount": number;
  "minDurationMs": number;
  "maxDurationMs": number;
};
};

export type CurationResponseDTO = {
  "candidates": ({
  "startWordIndex": number;
  "endWordIndexExclusive": number;
  "title": string;
  "hook": string;
  "reason": string;
})[];
};
