import {
  startSynthesizeE2eStack,
  statusOf,
  type SynthesizeE2eStack,
  waitFor,
} from "./synthesize-e2e-stack.js";

describe("synthesize concurrent load", () => {
  let stack: SynthesizeE2eStack;

  beforeAll(async () => {
    stack = await startSynthesizeE2eStack();
  }, 600_000);

  afterAll(async () => {
    await stack.stop();
  });

  it("measures enqueue latency separately from terminal completion", async () => {
    const jobs = await Promise.all(
      Array.from({ length: 3 }, async (_, index) => {
        const job = await stack.prepareJob();
        const startedAt = performance.now();
        const accepted = await stack.submit(
          job,
          `Concurrent synthesize load job ${index + 1}.`,
        );
        return {
          ...accepted,
          enqueueDurationMs: performance.now() - startedAt,
          job,
        };
      }),
    );
    const completionStartedAt = performance.now();
    await waitFor(async () =>
      (
        await Promise.all(
          jobs.map(async ({ job }) =>
            (await stack.callbacks(job)).some(
              (request) => statusOf(request) === "completed",
            ),
          ),
        )
      ).every(Boolean),
    );
    const completionDurationMs = performance.now() - completionStartedAt;

    expect(jobs.map((job) => job.enqueueDurationMs)).toHaveLength(3);
    expect(completionDurationMs).toBeGreaterThan(0);
    const finishedJobs = await Promise.all(
      jobs.map(async ({ job }) => await stack.objectSize(job)),
    );
    finishedJobs.forEach((jobSize) => expect(jobSize).toBeGreaterThan(0));
    expect(finishedJobs).toHaveLength(3);
  }, 60_000);
});
