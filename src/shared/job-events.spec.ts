import { redactPresignedUrl } from "./job-events.js";

describe(redactPresignedUrl.name, () => {
  it.each([
    {
      description: "mask AWS S3 full presigned URL",
      input:
        "https://s3.amazonaws.com/my-bucket/my-object?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20260819T120000Z&X-Amz-Expires=3600&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20260819%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=47cee36a6d4b8f6e8b5c7a9d0f1e2a3b4c5d6e7f8g9h0i1j2k3l4m5n6o7p8q9r0",
      expected:
        "https://s3.amazonaws.com/my-bucket/my-object?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20260819T120000Z&X-Amz-Expires=3600&X-Amz-Credential=***&X-Amz-Signature=***",
    },
    {
      description:
        "mask credentials in the URL generated in MinIO (AWS-compatible, different host)",
      input:
        "https://minio.example.com/my-bucket/my-object?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=MINIO_ACCESS_KEY%2F20260819%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=xyz789abc123&X-Amz-Expires=86400",
      expected:
        "https://minio.example.com/my-bucket/my-object?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=***&X-Amz-Signature=***&X-Amz-Expires=86400",
    },
    {
      description: "mask Google Cloud Storage (GCS) with X-Goog-Signature",
      input:
        "https://storage.googleapis.com/my-bucket/my-object?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Signature=a1b2c3d4e5f6g7h8i9j0&X-Goog-Expires=3600&X-Goog-Date=20260819T120000Z",
      expected:
        "https://storage.googleapis.com/my-bucket/my-object?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Signature=***&X-Goog-Expires=3600&X-Goog-Date=20260819T120000Z",
    },
    {
      description:
        "mask generic provider with token and sig in the query param",
      input:
        "https://generic-storage.com/bucket/file?token=secret123&sig=abc&other=keep&another=stay",
      expected:
        "https://generic-storage.com/bucket/file?token=***&sig=***&other=keep&another=stay",
    },
    {
      description: "return AWS S3 URL unchanged when it has no sensitive data",
      input:
        "https://s3.amazonaws.com/my-bucket/my-object?versionId=123&response-content-disposition=inline",
      expected:
        "https://s3.amazonaws.com/my-bucket/my-object?versionId=123&response-content-disposition=inline",
    },
    {
      description: "return URL with query params but none match sensitive list",
      input: "https://cdn.example.com/image.png?width=200&format=webp",
      expected: "https://cdn.example.com/image.png?width=200&format=webp",
    },
    {
      description: "return the URL with no query string at all untouched",
      input: "https://s3.amazonaws.com/my-bucket/my-object",
      expected: "https://s3.amazonaws.com/my-bucket/my-object",
    },
    {
      description: "return the invalid URL untouched",
      input: "not-a-valid-url",
      expected: "not-a-valid-url",
    },
    {
      description: "return the empty string (falling back to original)",
      input: "",
      expected: "",
    },
  ])("should $description", ({ input, expected }) => {
    const redactedUrl = redactPresignedUrl(input);

    expect(redactedUrl).toBe(expected);
  });
});
