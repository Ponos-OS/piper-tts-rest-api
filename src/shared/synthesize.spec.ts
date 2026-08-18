import { createSynthesizeConfig } from "./config.js";
import {
  parseSynthesizeRequest,
  SynthesizeRequestError,
} from "./synthesize.js";

const callbackConfig = createSynthesizeConfig(
  { SYNTHESIZE_CALLBACK_ALLOWED_HOSTS: "callbacks.example.com" },
  "test",
);

describe(parseSynthesizeRequest.name, () => {
  it("should parse a valid request", () => {
    const request = parseSynthesizeRequest(
      JSON.stringify({
        text: "  Hello world  ",
        genUploadUrl: "https://callbacks.example.com/uploads",
        statusCallbackUrl: "https://callbacks.example.com/status",
        callbackToken: "client-secret",
      }),
      callbackConfig,
    );

    expect(request).toEqual({
      text: "Hello world",
      genUploadUrl: "https://callbacks.example.com/uploads",
      statusCallbackUrl: "https://callbacks.example.com/status",
      callbackToken: "client-secret",
    });
  });

  it.each([
    ["invalid JSON", "{", "Invalid JSON body"],
    [
      "empty text",
      '{"text":"","genUploadUrl":"https://callbacks.example.com/upload","statusCallbackUrl":"https://callbacks.example.com/status"}',
      "text must be a non-empty string",
    ],
    [
      "missing upload URL",
      '{"text":"Hello","statusCallbackUrl":"https://callbacks.example.com/status"}',
      "genUploadUrl must be a non-empty string",
    ],
    [
      "empty callback token",
      '{"text":"Hello","genUploadUrl":"https://callbacks.example.com/upload","statusCallbackUrl":"https://callbacks.example.com/status","callbackToken":""}',
      "callbackToken must be a non-empty string when provided",
    ],
  ])("should return a 400 error for %s", (_caseName, body, message) => {
    expect(() => parseSynthesizeRequest(body, callbackConfig)).toThrow(message);

    try {
      parseSynthesizeRequest(body, callbackConfig);
    } catch (error) {
      expect(error).toBeInstanceOf(SynthesizeRequestError);
      expect((error as SynthesizeRequestError).statusCode).toBe(400);
    }
  });

  it.each([
    ["relative URL", "/status", "Callback URL must be an absolute URL"],
    [
      "credential URL",
      "https://user:pass@callbacks.example.com/status",
      "Callback URL must not contain credentials",
    ],
    [
      "fragment URL",
      "https://callbacks.example.com/status#fragment",
      "Callback URL must not contain a fragment",
    ],
    [
      "unallowed host",
      "https://other.example.com/status",
      "Callback URL host is not allowed",
    ],
  ])("should reject a %s", (_caseName, statusCallbackUrl, message) => {
    const body = JSON.stringify({
      text: "Hello",
      genUploadUrl: "https://callbacks.example.com/upload",
      statusCallbackUrl,
    });

    expect(() => parseSynthesizeRequest(body, callbackConfig)).toThrow(message);
  });

  it("should reject HTTP callbacks when HTTPS is required", () => {
    const productionConfig = createSynthesizeConfig({}, "production");
    const body = JSON.stringify({
      text: "Hello",
      genUploadUrl: "http://callbacks.example.com/upload",
      statusCallbackUrl: "https://callbacks.example.com/status",
    });

    expect(() => parseSynthesizeRequest(body, productionConfig)).toThrow(
      "Callback URL must use HTTPS",
    );
  });
});
