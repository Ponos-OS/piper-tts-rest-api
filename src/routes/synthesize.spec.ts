import type { IncomingMessage, ServerResponse } from "node:http";

import { expect, jest } from "@jest/globals";
import { context, propagation, trace, TraceFlags } from "@opentelemetry/api";
import { AsyncLocalStorageContextManager } from "@opentelemetry/context-async-hooks";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { BasicTracerProvider } from "@opentelemetry/sdk-trace-base";
import * as crypto from "node:crypto";
import { Readable } from "node:stream";

import type { AmqpBroker } from "../services/index.js";

import { createSynthesizeConfig } from "../shared/index.js";
import { createHandleSynthesize } from "./synthesize.js";

// jest.mock("node:crypto", () => ({
//   ...(jest.requireActual("node:crypto") as typeof crypto),
//   randomUUID: jest.fn(),
// }));

const synthesizeConfig = createSynthesizeConfig(
  { SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS: "false" },
  "test",
);

describe(createHandleSynthesize.name, () => {
  beforeAll(() => {
    context.setGlobalContextManager(new AsyncLocalStorageContextManager());
    propagation.setGlobalPropagator(new W3CTraceContextPropagator());
    trace.setGlobalTracerProvider(new BasicTracerProvider());
  });

  // FIXME: https://chat.deepseek.com/share/pqsjmbg4v3pt6wwk46
  it.skip("should publish one persistent job after confirmation and returns 202", async () => {
    const expectedJobId = "db6c1891-74b8-47a3-a885-f8bd1d866838";
    jest.mocked(crypto.randomUUID).mockReturnValue(expectedJobId);
    jest.spyOn(Date, "now").mockReturnValue(1_700_000_000);
    const broker = createBroker();
    const handleSynthesize = createHandleSynthesize({
      broker,
      config: synthesizeConfig,
    });
    const req = createRequest();
    const res = createResponse();

    await handleSynthesize(req, res);

    expect(res.writeHead).toHaveBeenCalledWith(202, {
      "Content-Type": "application/json; charset=utf-8",
    });
    expect(res.end).toHaveBeenCalledWith(
      JSON.stringify({ jobId: expectedJobId }),
    );
    expect(broker.publish).toHaveBeenCalledTimes(1);
    expect(broker.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        exchange: "synthesize",
        routingKey: "synthesize",
        content: Buffer.from(
          JSON.stringify({
            jobId: expectedJobId,
            text: "Hello from synthesize",
            genUploadUrl: "http://callbacks.test/upload-url",
            statusCallbackUrl: "http://callbacks.test/status",
          }),
        ),
        options: expect.objectContaining({
          contentType: "application/json",
          headers: expect.objectContaining({ timestamp: 1_700_000_000 }),
          messageId: expectedJobId,
          persistent: true,
          timestamp: 1_700_000,
        }),
      }),
    );
  });

  it("should return 503 when the broker does not confirm the publish", async () => {
    const broker = createBroker();
    jest
      .mocked(broker.publish)
      .mockRejectedValueOnce(new Error("broker unavailable"));
    const handleSynthesize = createHandleSynthesize({
      broker,
      config: synthesizeConfig,
    });
    const req = createRequest();
    const res = createResponse();

    await handleSynthesize(req, res);

    expect(res.writeHead).toHaveBeenCalledWith(503, {
      "Content-Type": "application/json; charset=utf-8",
    });
    expect(res.end).toHaveBeenCalledWith(
      JSON.stringify({ error: "Unable to accept synthesize job" }),
    );
  });

  it("should propagate the active trace context in the message headers", async () => {
    const broker = createBroker();
    const handleSynthesize = createHandleSynthesize({
      broker,
      config: synthesizeConfig,
    });
    const req = createRequest();
    const res = createResponse();
    const parent = trace.wrapSpanContext({
      traceId: "2ab95b2d9df64fda9b7c219d0a69b2c6",
      spanId: "7f2b0352f06847b2",
      traceFlags: TraceFlags.SAMPLED,
    });

    await context.with(trace.setSpan(context.active(), parent), () =>
      handleSynthesize(req, res),
    );

    expect(broker.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({
          headers: expect.objectContaining({
            traceparent: expect.stringContaining(
              "2ab95b2d9df64fda9b7c219d0a69b2c6",
            ),
          }),
        }),
      }),
    );
  });

  it("should add auth token to the message header", async () => {
    const broker = createBroker();
    const handleSynthesize = createHandleSynthesize({
      broker,
      config: synthesizeConfig,
    });
    const req = createRequest({ Authorization: "Basic someToken" });
    const res = createResponse();
    const parent = trace.wrapSpanContext({
      traceId: "2ab95b2d9df64fda9b7c219d0a69b2c6",
      spanId: "7f2b0352f06847b2",
      traceFlags: TraceFlags.SAMPLED,
    });

    await context.with(trace.setSpan(context.active(), parent), () =>
      handleSynthesize(req, res),
    );

    expect(broker.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({
          headers: expect.objectContaining({
            authorization: "Basic someToken",
          }),
        }),
      }),
    );
  });

  it("should return a 413 when the request is too large", async () => {
    const broker = createBroker();
    const config = { ...synthesizeConfig, maxBodySizeBytes: 10 };
    const handleSynthesize = createHandleSynthesize({ broker, config });
    const req = createRequest(); // body is larger than 10 bytes
    const res = createResponse();

    await handleSynthesize(req, res);

    expect(res.writeHead).toHaveBeenCalledWith(413, {
      "Content-Type": "application/json; charset=utf-8",
    });
    expect(res.end).toHaveBeenCalledWith(
      JSON.stringify({
        error: "Request body too large",
        code: "request-body-too-large",
      }),
    );
  });
});

function createBroker() {
  const broker = {
    publish: jest.fn().mockResolvedValue(undefined as never),
  } as Pick<AmqpBroker, "publish">;

  return broker;
}

function createRequest(headers?: Record<string, string>) {
  const req = Readable.from([
    Buffer.from(
      JSON.stringify({
        text: "Hello from synthesize",
        genUploadUrl: "http://callbacks.test/upload-url",
        statusCallbackUrl: "http://callbacks.test/status",
      }),
    ),
  ]) as IncomingMessage;
  req.headers = headers ? headers : {};

  return req;
}

function createResponse(): any {
  const res = {
    end: jest.fn(),
    writeHead: jest.fn(),
  } as Pick<ServerResponse, "end" | "writeHead">;

  return res;
}
