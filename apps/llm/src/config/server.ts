import { z } from "zod";

export const serverEnvSchema = z.object({
  HOST: z.string().min(1).default("127.0.0.1"),
  PORT: z.coerce.number().int().min(1).max(65535).default(8090),
  CURATION_ENABLED: z.enum(["true", "false"]).default("true"),
  CURATION_SERVICE_TOKEN: z.string().optional(),
  CURATION_PROMPT_PATH: z.string().min(1).optional(),
});
