import type { IncomingMessage, ServerResponse } from "node:http";

import {
  context,
  propagation,
  SpanStatusCode,
  trace,
} from "@opentelemetry/api";
import { randomUUID } from "node:crypto";

import type { AmqpBroker } from "../services/index.js";

import {
  parseSynthesizeRequest,
  readRawBody,
  RequestBodyTooLargeError,
  sendJson,
  SYNTHESIZE_ROUTING_KEY,
  type SynthesizeConfig,
  SynthesizeJobHeader,
  SynthesizeRequest,
  SynthesizeRequestError,
} from "../shared/index.js";

const tracer = trace.getTracer("POST /synthesize");

export function createHandleSynthesize(
  dependencies: SynthesizeRouteDependencies,
): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  return async (req, res) => {
    let request;

    try {
      request = await readSynthesizeRequest(req, dependencies.config);
    } catch (error) {
      sendRequestError(res, error);

      return;
    }

    const jobId = randomUUID();

    await tracer.startActiveSpan("synthesize.enqueue", async (span) => {
      try {
        const timestamp = Date.now();
        const authorization = String(
          req.headers.authorization ?? req.headers["Authorization"],
        );
        const headers: SynthesizeJobHeader = {
          timestamp,
          authorization,
        };

        propagation.inject(context.active(), headers);

        const message = {
          jobId,
          ...request,
        };
        span.setAttribute("synthesize.job_id", jobId);
        span.setAttribute("synthesize.text_length", request.text.length);

        await dependencies.broker.publish({
          exchange: dependencies.config.rabbitmq.exchange,
          routingKey: SYNTHESIZE_ROUTING_KEY,
          content: Buffer.from(JSON.stringify(message)),
          options: {
            contentType: "application/json",
            headers,
            messageId: jobId,
            persistent: true,
            timestamp: Math.floor(timestamp / 1000),
          },
        });
        sendJson(res, 202, { jobId });
      } catch (error) {
        span.recordException(
          error instanceof Error ? error : new Error(String(error)),
        );
        span.setStatus({
          code: SpanStatusCode.ERROR,
          message: "broker publish failed",
        });
        sendJson(res, 503, { error: "Unable to accept synthesize job" });
      } finally {
        span.end();
      }
    });
  };
}

function sendRequestError(res: ServerResponse, error: unknown): void {
  if (error instanceof SynthesizeRequestError) {
    sendJson(res, error.statusCode, {
      error: error.message,
      code: error.code,
    });
    return;
  }

  sendJson(res, 500, { error: "Unable to read synthesize request" });
}

interface SynthesizeRouteDependencies {
  broker: Pick<AmqpBroker, "publish">;
  config: SynthesizeConfig;
}

async function readSynthesizeRequest(
  req: IncomingMessage,
  config: Pick<SynthesizeConfig, "maxBodySizeBytes" | "callback">,
): Promise<SynthesizeRequest> {
  try {
    const rawBody = await readRawBody(req, config.maxBodySizeBytes);

    return parseSynthesizeRequest(rawBody, config);
  } catch (error) {
    if (error instanceof RequestBodyTooLargeError) {
      throw requestBodyTooLargeError();
    }
    throw error;
  }
}

function requestBodyTooLargeError(): SynthesizeRequestError {
  return new SynthesizeRequestError(
    413,
    "request-body-too-large",
    "Request body too large",
  );
}
