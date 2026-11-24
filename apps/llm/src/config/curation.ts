// Initial configurable budgets, not measured model-quality guarantees.
export const CURATION_LIMITS = {
  maxCandidates: 5,
  maxClipDurationMs: 60_000,
  maxTranscriptWords: 12_000,
  maxWordBytes: 512,
  maxMetadataCharacters: 1_000,
} as const;

export const CURATION_BUDGETS = {
  timeoutMs: 30_000,
  overallTimeoutMs: 65_000,
  maxPromptBytes: 100_000,
  maxOutputTokens: 8_192,
  maxResponseBytes: 32 * 1024,
} as const;
