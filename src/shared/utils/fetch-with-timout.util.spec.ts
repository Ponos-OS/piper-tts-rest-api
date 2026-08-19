import { jest } from "@jest/globals";

import { fetchWithTimeout } from "./fetch-with-timeout.util.js";

describe(fetchWithTimeout.name, () => {
  let mockFetch: jest.MockedFunction<any>;
  let fetchResolve: (value: Response) => void;
  let fetchReject: (reason: any) => void;
  let fetchPromise: Promise<Response>;
  let clearTimeoutSpy: jest.SpiedFunction<typeof clearTimeout>;

  beforeEach(() => {
    jest.useFakeTimers();
    fetchPromise = new Promise<Response>((resolve, reject) => {
      fetchResolve = resolve;
      fetchReject = reject;
    });

    // Mock fetch implementation that respects the abort signal
    mockFetch = jest.fn<typeof fetch>().mockImplementation((url, options) => {
      const signal = options?.signal as AbortSignal;

      if (signal.aborted) {
        // If already aborted, reject immediately
        return Promise.reject(signal.reason);
      }

      // Listen for abort and reject the fetch promise
      const abortHandler = () => {
        fetchReject(signal.reason);
      };

      signal.addEventListener("abort", abortHandler, { once: true });

      return fetchPromise;
    });

    global.fetch = mockFetch;
    clearTimeoutSpy = jest.spyOn(global, "clearTimeout");
  });

  afterEach(() => {
    jest.clearAllTimers();
    jest.useRealTimers();
    jest.restoreAllMocks();
  });

  it("should resolve with the response if fetch completes before timeout", async () => {
    const externalController = new AbortController();
    const externalSignal = externalController.signal;
    const removeSpy = jest.spyOn(externalSignal, "removeEventListener");

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );

    const response = mockResponse();
    fetchResolve(response);
    await expect(promise).resolves.toBe(response);
    expect(clearTimeoutSpy).toHaveBeenCalled();
    // Verify that the abort listener was removed
    expect(removeSpy).toHaveBeenCalledWith("abort", expect.any(Function));
  });

  it("should reject with timeout error if fetch takes longer than timeoutMs", async () => {
    const externalController = new AbortController();
    const externalSignal = externalController.signal;

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );
    jest.advanceTimersByTime(1000);

    await expect(promise).rejects.toThrow("Request timed out");
    expect(clearTimeoutSpy).toHaveBeenCalled();
  });

  it("should reject with external abort reason when external signal aborts", async () => {
    const externalController = new AbortController();
    const externalSignal = externalController.signal;

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );
    const abortReason = new Error("External abort");
    externalController.abort(abortReason);

    await expect(promise).rejects.toBe(abortReason);
    expect(clearTimeoutSpy).toHaveBeenCalled();
  });

  it("should NOT abort internal controller if external signal is already aborted", async () => {
    const externalController = new AbortController();
    externalController.abort(new Error("Already aborted"));
    const externalSignal = externalController.signal;

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );
    const response = mockResponse();
    fetchResolve(response);

    await expect(promise).resolves.toBe(response);
    expect(clearTimeoutSpy).toHaveBeenCalled();
  });

  it("should propagate network errors (non-abort) and still clean up", async () => {
    const externalController = new AbortController();
    const externalSignal = externalController.signal;

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );
    const networkError = new Error("Network failure");
    fetchReject(networkError);

    await expect(promise).rejects.toBe(networkError);
    expect(clearTimeoutSpy).toHaveBeenCalled();
  });

  it("should clear timeout and remove listener even when fetch throws synchronously", async () => {
    mockFetch.mockImplementationOnce(() => {
      throw new Error("Sync error");
    });
    const externalController = new AbortController();
    const externalSignal = externalController.signal;
    const removeSpy = jest.spyOn(externalSignal, "removeEventListener");

    const promise = fetchWithTimeout(
      "https://api.example.com",
      { method: "GET" },
      1000,
      externalSignal,
    );

    await expect(promise).rejects.toThrow("Sync error");
    expect(clearTimeoutSpy).toHaveBeenCalled();
    expect(removeSpy).toHaveBeenCalledWith("abort", expect.any(Function));
  });
});

function mockResponse(): Response {
  return new Response("OK", { status: 200 });
}
