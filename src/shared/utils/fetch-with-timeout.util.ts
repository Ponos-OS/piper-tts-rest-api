export async function fetchWithTimeout(
  url: string,
  options: RequestInit,
  timeoutMs: number,
  signal: AbortSignal,
): Promise<Response> {
  const controller = new AbortController();
  const abort = (): void => controller.abort(signal.reason);

  signal.addEventListener("abort", abort, { once: true });

  const timeout = setTimeout(
    () => controller.abort(new Error("Request timed out")),
    timeoutMs,
  );

  try {
    return await fetch(url, { ...options, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
    signal.removeEventListener("abort", abort);
  }
}
