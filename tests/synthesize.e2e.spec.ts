import {
  jobIdOf,
  startSynthesizeE2eStack,
  statusOf,
  type SynthesizeE2eStack,
  waitFor,
} from "./synthesize-e2e-stack.js";

describe("POST /synthesize", () => {
  let stack: SynthesizeE2eStack;

  beforeAll(async () => {
    stack = await startSynthesizeE2eStack();
  }, 600_000);

  afterAll(async () => {
    await stack.stop();
  });

  it("should call its callbacks and uploads the generated MP3 to MinIO", async () => {
    const job = await stack.prepareJob();
    const { jobId } = await stack.submit(
      job,
      "Hello from the Testcontainers end-to-end test.",
    );
    await waitFor(async () =>
      (await stack.callbacks(job)).some(
        (request) => statusOf(request) === "completed",
      ),
    );

    expect(jobId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    );
    const callbacks = await stack.callbacks(job);
    expect(callbacks.every((request) => jobIdOf(request) === jobId)).toBe(true);
    expect(callbacks.map(statusOf)).toEqual(
      expect.arrayContaining([
        "queued",
        "generating",
        "uploading",
        "completed",
      ]),
    );
    expect(await stack.objectSize(job)).toBeGreaterThan(0);
  }, 60_000);
});
