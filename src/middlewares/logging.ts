import type { IncomingMessage, ServerResponse } from "node:http";

import { type Logger } from "../shared/index.js";

type RequestHandler = (
  req: IncomingMessage,
  res: ServerResponse,
) => void | Promise<void>;

/**
 * Wraps a request handler to include logging middleware functionality.
 */
export function withLogging(
  logger: Logger,
  handler: RequestHandler,
): RequestHandler {
  const blackListedEndpoints = ["/health"];

  return (req, res) => {
    const startTime = Date.now();
    const method = req.method ?? "UNKNOWN";
    const url = req.url ?? "/";
    const isBlackListed = blackListedEndpoints.some(
      (endpoint) => endpoint === url,
    );

    if (!isBlackListed) {
      // Log incoming request
      logger.info(`Incoming ${method} ${url}`, {
        context: "HttpMiddleware",
        method,
        url,
        headers: req.headers,
      });
    }

    // Intercept response finish to log completion
    const originalEnd = res.end;
    res.end = function (this: ServerResponse, ...args: unknown[]) {
      const duration = Date.now() - startTime;
      const statusCode = res.statusCode;

      if (!isBlackListed) {
        logger.info(`Completed ${method} ${url}`, {
          context: "HttpMiddleware",
          method,
          url,
          statusCode,
          duration,
        });
      }

      return (originalEnd as (...args: unknown[]) => ServerResponse).apply(
        this,
        args,
      );
    } as ServerResponse["end"];

    // Call the actual handler
    return handler(req, res);
  };
}
