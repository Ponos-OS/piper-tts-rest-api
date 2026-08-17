import type { IncomingMessage } from "node:http";

export class RequestBodyTooLargeError extends Error {
  constructor() {
    super("Request body too large");
    this.name = "RequestBodyTooLargeError";
  }
}

/**
 * Reads the raw request body up to a maximum size.
 */
export function readRawBody(
  req: IncomingMessage,
  maxBodySizeBytes: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;

    req.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > maxBodySizeBytes) {
        reject(new RequestBodyTooLargeError());
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });

    req.on("end", () => {
      resolve(Buffer.concat(chunks).toString("utf8"));
    });

    req.on("error", reject);
  });
}

/**
 * Extracts speakable text from request payload.
 * Accepts JSON (`{ "text": "..." }`) or raw plain text.
 */
export function extractTextFromRequestBody(
  rawBody: string,
  contentType: string | undefined,
): string {
  if (!rawBody || !rawBody.trim()) {
    return "";
  }

  if ((contentType ?? "").includes("application/json")) {
    const parsed = JSON.parse(rawBody) as { text?: unknown };
    return typeof parsed.text === "string" ? parsed.text.trim() : "";
  }

  return rawBody.trim();
}
