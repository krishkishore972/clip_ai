import { readFile } from "node:fs/promises";
import type { ChatCompletionCreateParamsNonStreaming } from "groq-sdk/resources/chat/completions.js";
import { z } from "zod";
import { CURATION_BUDGETS, CURATION_LIMITS } from "../config/curation.js";
import { compactCurationInput } from "./curation-input.js";
import {
  createCurationSchemas,
  curationLimitsSchema,
  type CurationResponse,
} from "../schemas/curation.js";

const optionsSchema = z
  .object({
    model: z.literal("openai/gpt-oss-120b").default("openai/gpt-oss-120b"),
    promptPath: z.string().min(1),
    limits: curationLimitsSchema.default(CURATION_LIMITS),
    timeoutMs: z
      .number()
      .int()
      .positive()
      .max(30_000)
      .default(CURATION_BUDGETS.timeoutMs),
    overallTimeoutMs: z
      .number()
      .int()
      .positive()
      .max(65_000)
      .default(CURATION_BUDGETS.overallTimeoutMs),
    maxPromptBytes: z
      .number()
      .int()
      .positive()
      .max(100_000)
      .default(CURATION_BUDGETS.maxPromptBytes),
    maxOutputTokens: z
      .number()
      .int()
      .positive()
      .max(CURATION_BUDGETS.maxOutputTokens)
      .default(CURATION_BUDGETS.maxOutputTokens),
    maxResponseBytes: z
      .number()
      .int()
      .positive()
      .max(CURATION_BUDGETS.maxResponseBytes)
      .default(CURATION_BUDGETS.maxResponseBytes),
  })
  .strict();

export type CuratorOptions = z.input<typeof optionsSchema>;

// A small injectable boundary allows offline tests without credentials or calls.
export type Complete = (
  request: ChatCompletionCreateParamsNonStreaming,
  options: { timeout: number; maxRetries: number; signal?: AbortSignal },
) => Promise<unknown>;

const completionSchema = z.object({
  choices: z
    .array(
      z.object({
        finish_reason: z.literal("stop"),
        message: z.object({ content: z.string().min(1) }),
      }),
    )
    .length(1),
});

export class CurationError extends Error {
  constructor(
    public readonly code:
      | "CONFIGURATION"
      | "INVALID_INPUT"
      | "CANCELLED"
      | "TIMEOUT"
      | "PROMPT_TOO_LARGE"
      | "PROVIDER_FAILED"
      | "INVALID_RESPONSE",
  ) {
    // Do not attach raw provider errors, model output or transcript content.
    super(`Curation failed: ${code}`);
    this.name = "CurationError";
  }
}

// Resolve promptPath explicitly to the shipped v2.md artifact. This avoids
// assuming tsc copies Markdown into dist; the caller must ship that file.
export async function createCurator(
  input: CuratorOptions,
  complete?: Complete,
) {
  const parsed = optionsSchema.safeParse(input);
  if (!parsed.success) {
    throw new CurationError("CONFIGURATION");
  }
  const options = parsed.data;
  const schemas = createCurationSchemas(options.limits);

  let prompt: string;
  try {
    prompt = await readFile(options.promptPath, "utf8");
    if (!prompt.trim()) {
      throw new Error("Empty prompt");
    }
  } catch {
    throw new CurationError("CONFIGURATION");
  }

  let send = complete;
  if (!send) {
    const { groq } = await import("../providers/groq.js");
    send = (request, requestOptions) =>
      groq.chat.completions.create(request, requestOptions);
  }
  const completeRequest = send;

  async function runCurate(
    input: unknown,
    signal?: AbortSignal,
  ): Promise<CurationResponse> {
    if (signal?.aborted) {
      throw new CurationError("CANCELLED");
    }
    const request = schemas.requestSchema.safeParse(input);
    if (!request.success) {
      throw new CurationError("INVALID_INPUT");
    }

    const messages: ChatCompletionCreateParamsNonStreaming["messages"] = [
      { role: "system", content: prompt },
      {
        role: "user",
        content: JSON.stringify(compactCurationInput(request.data)),
      },
    ];
    // Count the entire serialized message list, including role/JSON overhead.
    // This is a byte budget, not a model-specific tokenizer measurement.
    if (
      Buffer.byteLength(JSON.stringify(messages), "utf8") >
      options.maxPromptBytes
    ) {
      throw new CurationError("PROMPT_TOO_LARGE");
    }

    let raw: unknown;
    try {
      raw = await completeRequest(
        {
          model: options.model,
          stream: false,
          response_format: { type: "json_object" },
          max_completion_tokens: options.maxOutputTokens,
          messages,
        },
        // The SDK retries transient transport/status errors once. Validation
        // failures below never trigger another paid request.
        { timeout: options.timeoutMs, maxRetries: 1, signal },
      );
    } catch {
      throw new CurationError(
        signal?.aborted ? "CANCELLED" : "PROVIDER_FAILED",
      );
    }

    if (signal?.aborted) {
      throw new CurationError("CANCELLED");
    }
    const completion = completionSchema.safeParse(raw);
    const content = completion.success
      ? completion.data.choices[0]?.message.content
      : undefined;
    if (
      !content ||
      Buffer.byteLength(content, "utf8") > options.maxResponseBytes
    ) {
      throw new CurationError("INVALID_RESPONSE");
    }

    let json: unknown;
    try {
      json = JSON.parse(content);
    } catch {
      throw new CurationError("INVALID_RESPONSE");
    }
    const response = schemas.responseSchema.safeParse(json);
    if (
      !response.success ||
      response.data.candidates.length > request.data.constraints.candidateCount
    ) {
      throw new CurationError("INVALID_RESPONSE");
    }

    const seen = new Set<string>();
    for (const candidate of response.data.candidates) {
      const { startWordIndex: start, endWordIndexExclusive: end } = candidate;
      const first = request.data.words[start];
      const key = `${start}:${end}`;
      if (
        !first ||
        first.startMs < 0 ||
        end > request.data.words.length ||
        seen.has(key)
      ) {
        throw new CurationError("INVALID_RESPONSE");
      }
      seen.add(key);

      let endMs = first.endMs;
      for (let index = start + 1; index < end; index++) {
        const word = request.data.words[index];
        if (!word) {
          throw new CurationError("INVALID_RESPONSE");
        }
        endMs = Math.max(endMs, word.endMs);
      }
      const durationMs = endMs - first.startMs;
      const constraints = request.data.constraints;
      if (
        durationMs < constraints.minDurationMs ||
        durationMs > constraints.maxDurationMs
      ) {
        throw new CurationError("INVALID_RESPONSE");
      }
    }

    // These checks are defense in depth; Go remains authoritative.
    return response.data;
  }

  return async function curate(
    input: unknown,
    signal?: AbortSignal,
  ): Promise<CurationResponse> {
    if (signal?.aborted) {
      throw new CurationError("CANCELLED");
    }
    const deadline = new AbortController();
    const combined = signal
      ? AbortSignal.any([signal, deadline.signal])
      : deadline.signal;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const expired = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => {
        reject(new CurationError("TIMEOUT"));
        deadline.abort();
      }, options.overallTimeoutMs);
    });

    try {
      const response = await Promise.race([
        runCurate(input, combined),
        expired,
      ]);
      if (signal?.aborted) {
        throw new CurationError("CANCELLED");
      }
      if (deadline.signal.aborted) {
        throw new CurationError("TIMEOUT");
      }
      return response;
    } catch (error) {
      if (signal?.aborted) {
        throw new CurationError("CANCELLED");
      }
      if (deadline.signal.aborted) {
        throw new CurationError("TIMEOUT");
      }
      throw error;
    } finally {
      clearTimeout(timer);
    }
  };
}
