import { fileURLToPath } from "node:url";
import { serverEnvSchema } from "./config/server.js";
import { createApp, serviceTokenSchema } from "./http/app.js";
import { createCurator } from "./tasks/curate.js";

async function main(): Promise<void> {
  const env = serverEnvSchema.parse(process.env);

  let service: Parameters<typeof createApp>[0];
  if (env.CURATION_ENABLED === "true") {
    const token = serviceTokenSchema.parse(env.CURATION_SERVICE_TOKEN);
    const curate = await createCurator({
      promptPath:
        env.CURATION_PROMPT_PATH ??
        fileURLToPath(new URL("../src/prompts/curate/v1.md", import.meta.url)),
    });
    service = { token, curate };
  }
  const app = createApp(service);
  const server = app.listen(env.PORT, env.HOST, () => {
    console.log(`LLM server listening on ${env.HOST}:${env.PORT}`);
  });
  server.requestTimeout = 15_000;
  server.headersTimeout = 10_000;
  server.keepAliveTimeout = 5_000;
  server.setTimeout(75_000, (socket) => socket.destroy());
  server.on("error", () => {
    console.error("LLM server failed");
    process.exitCode = 1;
  });

  function shutdown(): void {
    const timeout = setTimeout(() => {
      server.closeAllConnections();
      process.exit(1);
    }, 10_000);
    timeout.unref();
    server.close((error) => {
      clearTimeout(timeout);
      process.exitCode = error ? 1 : 0;
    });
  }
  process.once("SIGINT", shutdown);
  process.once("SIGTERM", shutdown);
}

main().catch(() => {
  // Environment and SDK errors may contain secrets; never print raw errors.
  console.error("LLM server initialization failed; check configuration");
  process.exitCode = 1;
});
