import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import express, {
  type ErrorRequestHandler,
  type Express,
  type Response,
} from "express";
import { z } from "zod";
import { CURATION_LIMITS } from "../config/curation.js";
import type {
  CurationRequestDTO,
  CurationResponseDTO,
} from "../generated/curation.js";
import { createCurationSchemas } from "../schemas/curation.js";
import { CurationError } from "../tasks/curate.js";

export const serviceTokenSchema = z.string().regex(/^[A-Za-z0-9_-]{32,256}$/);

type Curate = (
  input: CurationRequestDTO,
  signal: AbortSignal,
) => Promise<CurationResponseDTO>;

// Shared across app instances: one active curation operation per Node process.
let active = false;

function digest(value: string): Buffer {
  return createHash("sha256").update(value).digest();
}

function failure(res: Response, status: number, code: string): void {
  res.status(status).json({
    code,
    message: "Curation request could not be completed",
    traceId: res.locals.traceId as string,
  });
}

export function createApp(service?: {
  token: string;
  curate: Curate;
}): Express {
  const tokenDigest = service
    ? digest(serviceTokenSchema.parse(service.token))
    : undefined;
  const schemas = createCurationSchemas(CURATION_LIMITS);
  const app = express();
  app.disable("x-powered-by");
  app.disable("etag");
  app.use((_req, res, next) => {
    res.setHeader("Cache-Control", "no-store");
    res.locals.traceId = randomUUID();
    next();
  });

  app.get("/healthz", (_req, res) => {
    res.status(204).end();
  });

  app.post(
    "/internal/curate",
    (req, res, next) => {
      if (!service || !tokenDigest) {
        failure(res, 503, "SERVICE_UNAVAILABLE");
        return;
      }
      const authorization = req.get("authorization") ?? "";
      const match = /^Bearer ([A-Za-z0-9_-]{32,256})$/i.exec(authorization);
      if (!match?.[1] || !timingSafeEqual(digest(match[1]), tokenDigest)) {
        res.setHeader("WWW-Authenticate", "Bearer");
        failure(res, 401, "UNAUTHENTICATED");
        return;
      }
      if (!req.is("application/json")) {
        failure(res, 415, "UNSUPPORTED_MEDIA_TYPE");
        return;
      }
      next();
    },
    // Authenticate before reading private transcript content. Compression is
    // rejected rather than inflating an untrusted request.
    express.json({ limit: "1mb", inflate: false }),
    async (req, res) => {
      const parsed = schemas.requestSchema.safeParse(req.body as unknown);
      if (!parsed.success) {
        failure(res, 400, "INVALID_INPUT");
        return;
      }
      if (active) {
        res.setHeader("Retry-After", "1");
        failure(res, 503, "BUSY");
        return;
      }
      if (!service) {
        failure(res, 503, "SERVICE_UNAVAILABLE");
        return;
      }

      active = true;
      const controller = new AbortController();
      const disconnect = () => {
        if (!res.writableFinished) controller.abort();
      };
      req.once("aborted", disconnect);
      res.once("close", disconnect);
      try {
        const result = await service.curate(parsed.data, controller.signal);
        if (!controller.signal.aborted && !res.destroyed) {
          // The adapter additionally enforces request-specific ranges/budgets.
          const output = schemas.responseSchema.safeParse(result);
          if (!output.success) {
            failure(res, 502, "INVALID_RESPONSE");
          } else {
            res.status(200).json(output.data);
          }
        }
      } catch (error: unknown) {
        if (controller.signal.aborted || res.destroyed) return;
        if (error instanceof CurationError) {
          const status = {
            CONFIGURATION: 503,
            INVALID_INPUT: 400,
            CANCELLED: 503,
            TIMEOUT: 504,
            PROMPT_TOO_LARGE: 413,
            PROVIDER_FAILED: 502,
            INVALID_RESPONSE: 502,
          }[error.code];
          failure(res, status, error.code);
        } else {
          failure(res, 500, "INTERNAL_ERROR");
        }
      } finally {
        req.off("aborted", disconnect);
        res.off("close", disconnect);
        active = false;
      }
    },
  );

  app.use((_req, res) => {
    failure(res, 404, "NOT_FOUND");
  });

  const parserErrorSchema = z.object({
    type: z.enum([
      "entity.parse.failed",
      "entity.too.large",
      "encoding.unsupported",
      "charset.unsupported",
    ]),
  });
  const errorHandler: ErrorRequestHandler = (
    error: unknown,
    _req,
    res,
    _next,
  ) => {
    if (res.headersSent) {
      // Express's default handler can print errors. Close without forwarding
      // private parser/provider details to it.
      res.destroy();
      return;
    }
    const parsed = parserErrorSchema.safeParse(error);
    if (parsed.success) {
      const type = parsed.data.type;
      const status =
        type === "entity.too.large"
          ? 413
          : type === "entity.parse.failed"
            ? 400
            : 415;
      failure(
        res,
        status,
        status === 413
          ? "BODY_TOO_LARGE"
          : status === 400
            ? "INVALID_JSON"
            : "UNSUPPORTED_MEDIA_TYPE",
      );
      return;
    }
    failure(res, 500, "INTERNAL_ERROR");
  };
  app.use(errorHandler);
  return app;
}
