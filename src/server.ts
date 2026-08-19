import {
  createServer as createHttpServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";

import { withLogging } from "./middlewares/index.js";
import {
  createHandleSynthesize,
  handleHealth,
  handleSpeak,
} from "./routes/index.js";
import {
  type AmqpBroker,
  connectAmqpBroker,
  startSynthesizeWorker,
  type SynthesizeWorker,
  verifyFfmpegInstallation,
  verifyPiperInstallation,
} from "./services/index.js";
import { config, logger, sendText } from "./shared/index.js";

function createServer(broker: Pick<AmqpBroker, "publish">): Server {
  const handleSynthesize = createHandleSynthesize({
    broker,
    config: config.synthesize,
  });
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

    if (req.method === "POST" && req.url === "/synthesize") {
      await handleSynthesize(req, res);
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
  const server = createServer(broker);
  const worker = await startSynthesizeWorker(
    broker,
    config.synthesize,
    config.piperModelPath,
  );

  registerShutdown(server, broker, worker);

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
  worker: SynthesizeWorker,
): void {
  let shuttingDown = false;

  const shutdown = (): void => {
    if (shuttingDown) {
      return;
    }

    shuttingDown = true;

    server.close(() => {
      void worker
        .close()
        .then(() => broker.close())
        .catch((error: unknown) => {
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
