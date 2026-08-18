import type { SynthesizeConfig } from "./config.js";

export function parseSynthesizeRequest(
  rawBody: string,
  config: Pick<SynthesizeConfig, "callback">,
): SynthesizeRequest {
  let payload: unknown;

  try {
    payload = JSON.parse(rawBody) as unknown;
  } catch {
    throw invalidRequest("Invalid JSON body");
  }

  if (!isRecord(payload)) {
    throw invalidRequest("Body must be a JSON object");
  }

  const text = readNonEmptyString(payload.text, "text");
  const genUploadUrl = validateCallbackUrl(
    readNonEmptyString(payload.genUploadUrl, "genUploadUrl"),
    config,
  );
  const statusCallbackUrl = validateCallbackUrl(
    readNonEmptyString(payload.statusCallbackUrl, "statusCallbackUrl"),
    config,
  );
  const callbackToken = readOptionalToken(payload.callbackToken);

  return {
    text,
    genUploadUrl,
    statusCallbackUrl,
    ...(callbackToken === undefined ? {} : { callbackToken }),
  };
}

function validateCallbackUrl(
  value: string,
  config: Pick<SynthesizeConfig, "callback">,
): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw invalidCallbackUrl("Callback URL must be an absolute URL");
  }

  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw invalidCallbackUrl("Callback URL must use HTTP or HTTPS");
  }
  if (config.callback.requireHttps && url.protocol !== "https:") {
    throw invalidCallbackUrl("Callback URL must use HTTPS");
  }
  if (url.username !== "" || url.password !== "") {
    throw invalidCallbackUrl("Callback URL must not contain credentials");
  }
  if (url.hash !== "") {
    throw invalidCallbackUrl("Callback URL must not contain a fragment");
  }
  if (
    config.callback.allowedHosts.length > 0 &&
    !config.callback.allowedHosts.includes(url.hostname.toLowerCase())
  ) {
    throw invalidCallbackUrl("Callback URL host is not allowed");
  }

  return url.toString();
}

function readNonEmptyString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw invalidRequest(`${field} must be a non-empty string`);
  }
  return value.trim();
}

function readOptionalToken(value: unknown): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw invalidRequest(
      "callbackToken must be a non-empty string when provided",
    );
  }
  return value;
}

function invalidRequest(message: string): SynthesizeRequestError {
  return new SynthesizeRequestError(400, "invalid-request", message);
}

function invalidCallbackUrl(message: string): SynthesizeRequestError {
  return new SynthesizeRequestError(400, "invalid-callback-url", message);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export interface SynthesizeJobMessage extends SynthesizeRequest {
  jobId: string;
}
export type SynthesizeStatusPayload =
  | {
      jobId: string;
      status: "queued";
      attempt: number;
    }
  | {
      jobId: string;
      status: "generating" | "uploading";
      percent: number;
      attempt: number;
    }
  | {
      jobId: string;
      status: "completed";
      attempt: number;
      fileSizeBytes: number;
    }
  | {
      jobId: string;
      status: "failed";
      attempt: number;
      failedAt: string;
      error: SynthesizeError;
    };
export interface SynthesizeError {
  code: SynthesizeErrorCode;
  message: string;
}
export type SynthesizeErrorCode =
  | "invalid-request"
  | "invalid-callback-url"
  | "request-body-too-large";
export interface SynthesizeRequest {
  text: string;
  genUploadUrl: string;
  statusCallbackUrl: string;
  callbackToken?: string;
}

export class SynthesizeRequestError extends Error {
  readonly statusCode: 400 | 413;
  readonly code: SynthesizeErrorCode;

  constructor(
    statusCode: 400 | 413,
    code: SynthesizeErrorCode,
    message: string,
  ) {
    super(message);
    this.name = "SynthesizeRequestError";
    this.statusCode = statusCode;
    this.code = code;
  }
}
