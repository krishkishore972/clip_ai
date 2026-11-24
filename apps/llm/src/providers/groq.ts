import Groq from "groq-sdk";
import { z } from "zod";

const apiKeyResult = z
  .string()
  .trim()
  .min(1)
  .safeParse(process.env.GROQ_API_KEY);

if (!apiKeyResult.success) {
  throw new Error("GROQ_API_KEY must be set to a non-empty value");
}

export const groq = new Groq({
  apiKey: apiKeyResult.data,
});
