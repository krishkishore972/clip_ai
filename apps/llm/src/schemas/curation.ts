import { z } from "zod";
import { CURATION_LIMITS } from "../config/curation.js";

const positiveInteger = z.number().int().positive();
const nonNegativeInteger = z.number().int().nonnegative();
const nonEmptyText = z.string().refine((text) => text.trim().length > 0, {
  message: "Text must not be blank",
});

// Supply approved server-side ceilings; client constraints cannot raise them.
export const curationLimitsSchema = z
  .object({
    maxCandidates: positiveInteger.max(CURATION_LIMITS.maxCandidates),
    maxClipDurationMs: positiveInteger.max(CURATION_LIMITS.maxClipDurationMs),
    maxTranscriptWords: positiveInteger.max(CURATION_LIMITS.maxTranscriptWords),
    maxWordBytes: positiveInteger.max(CURATION_LIMITS.maxWordBytes),
    maxMetadataCharacters: positiveInteger.max(
      CURATION_LIMITS.maxMetadataCharacters,
    ),
  })
  .strict();

export type CurationLimits = z.infer<typeof curationLimitsSchema>;

export function createCurationSchemas(input: CurationLimits) {
  const limits = curationLimitsSchema.parse(input);

  const wordSchema = z
    .object({
      index: nonNegativeInteger,
      text: nonEmptyText.refine(
        (text) => Buffer.byteLength(text, "utf8") <= limits.maxWordBytes,
        { message: "Word exceeds the UTF-8 byte limit" },
      ),
      // Go has already applied the prepared-audio offset. Do not apply it again.
      startMs: z.number().int(),
      endMs: z.number().int(),
    })
    .strict()
    .refine((word) => word.endMs > word.startMs, {
      message: "Word end must be after its start",
      path: ["endMs"],
    });

  const constraintsSchema = z
    .object({
      candidateCount: positiveInteger.max(limits.maxCandidates),
      minDurationMs: positiveInteger.max(limits.maxClipDurationMs),
      maxDurationMs: positiveInteger.max(limits.maxClipDurationMs),
    })
    .strict()
    .refine(
      (constraints) => constraints.minDurationMs <= constraints.maxDurationMs,
      {
        message: "Maximum duration must be at least the minimum duration",
        path: ["maxDurationMs"],
      },
    );

  const requestSchema = z
    .object({
      // Hash of the stored transcript artifact, not Deepgram's audio hash.
      transcriptHash: z.string().regex(/^sha256:[a-f0-9]{64}$/),
      words: z.array(wordSchema).min(1).max(limits.maxTranscriptWords),
      constraints: constraintsSchema,
    })
    .strict()
    .superRefine((request, ctx) => {
      for (const [position, word] of request.words.entries()) {
        if (word.index !== position) {
          ctx.addIssue({
            code: "custom",
            path: ["words", position, "index"],
            message: "Word indexes must be contiguous and start at zero",
          });
        }

        const previous = request.words[position - 1];
        if (previous && word.startMs < previous.startMs) {
          ctx.addIssue({
            code: "custom",
            path: ["words", position, "startMs"],
            message: "Words must be ordered by start time",
          });
        }
      }
      // Overlapping word intervals and decreasing end times are allowed.
    });

  const metadataText = nonEmptyText.max(limits.maxMetadataCharacters);
  const candidateSchema = z
    .object({
      startWordIndex: nonNegativeInteger,
      endWordIndexExclusive: positiveInteger,
      title: metadataText,
      hook: metadataText,
      reason: metadataText,
    })
    .strict()
    .refine(
      (candidate) => candidate.endWordIndexExclusive > candidate.startWordIndex,
      {
        message: "Candidate range must be non-empty",
        path: ["endWordIndexExclusive"],
      },
    );

  const responseSchema = z
    .object({
      candidates: z.array(candidateSchema).max(limits.maxCandidates),
    })
    .strict();

  // Candidate overlap is allowed. Go must validate the response against the
  // request's transcript, requested count, duration limits and source bounds.
  return { requestSchema, responseSchema };
}

export type CurationRequest = z.infer<
  ReturnType<typeof createCurationSchemas>["requestSchema"]
>;

export type CurationResponse = z.infer<
  ReturnType<typeof createCurationSchemas>["responseSchema"]
>;
