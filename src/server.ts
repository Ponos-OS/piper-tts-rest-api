import {
  createServer as createHttpServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";

import { withLogging } from "./middlewares/index.js";
import { handleHealth, handleSpeak } from "./routes/index.js";
import {
  connectAmqpBroker,
  verifyFfmpegInstallation,
  verifyPiperInstallation,
} from "./services/index.js";
import { config, createLogger, sendText } from "./shared/index.js";

// Create logger instance
export const logger = createLogger(
  config.log.level,
  config.log.mode,
  config.service.name,
);

/**
 * Creates the API server.
 */
function createServer(): Server {
  const handler = async (
    req: IncomingMessage,
    res: ServerResponse,
  ): Promise<void> => {
    if (!req.url || !req.method) {
      sendText(res, 400, "Bad Request");
      return;
    }

    if (req.method === "GET" && req.url === "/health") {
      handleHealth(res);
      return;
    }

    if (req.method === "POST" && req.url === "/speak") {
      await handleSpeak(req, res);
      return;
    }

    sendText(res, 404, "Not Found");
  };

  return createHttpServer(withLogging(logger, handler));
}

/**
 * Bootstraps API dependencies and starts the HTTP server.
 */
async function bootstrap(): Promise<void> {
  verifyPiperInstallation(config.piperModelPath);
  verifyFfmpegInstallation();

  const broker = await connectAmqpBroker(config.synthesize.rabbitmq);
  const server = createServer();

  registerShutdown(server, broker);

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(config.service.port, resolve);
  });

  logger.info(`TTS API running on port ${config.service.port}`, {
    context: "Bootstrap",
  });
  logger.info(`Piper model: ${config.piperModelPath}`, {
    context: "Bootstrap",
  });
}

function registerShutdown(
  server: Server,
  broker: Awaited<ReturnType<typeof connectAmqpBroker>>,
): void {
  let shuttingDown = false;

  const shutdown = (): void => {
    if (shuttingDown) {
      return;
    }

    shuttingDown = true;

    server.close(() => {
      void broker.close().catch((error: unknown) => {
        logger.error("Failed to close AMQP broker", {
          context: "Shutdown",
          error: error instanceof Error ? error.message : String(error),
        });
      });
    });
  };

  process.once("SIGTERM", shutdown);
  process.once("SIGINT", shutdown);
}

void bootstrap().catch((error: unknown) => {
  logger.error("Failed to start service", {
    context: "Bootstrap",
    error: error instanceof Error ? error.message : String(error),
  });
  process.exitCode = 1;
});
