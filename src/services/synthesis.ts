import type { ChildProcessWithoutNullStreams } from "node:child_process";

import { createWriteStream } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Transform } from "node:stream";
import { pipeline } from "node:stream/promises";

import { logger, wordCounter } from "../shared/index.js";
import { spawnFfmpegMp3FromPcm, spawnPiperPcmStdout } from "./piper.js";

const ESTIMATED_MP3_BITRATE_KBPS = 128;
const PROGRESS_REPORT_INTERVAL_MS = 500;

/**
 * @description Generates an MP3 into a private temporary directory. The caller owns the successful result and must call `cleanup` after uploading the file.
 */
export async function synthesizeTextToMp3File(
  text: string,
  modelPath: string,
  sampleRate: number,
  wordsPerMinute: number,
  options: SynthesizeTextToMp3FileOptions = {},
): Promise<SynthesisFile> {
  logger.info(
    `Synthesizing text with ${wordCounter(text)} words using ${modelPath} at ${sampleRate}Hz (${wordsPerMinute} words/min)`,
    { context: "synthesizeTextToMp3File" },
  );

  const startedAt = performance.now();
  const workDir = await mkdtemp(join(tmpdir(), "piper-tts-"));
  const filePath = join(workDir, "audio.mp3");
  let cleaned = false;
  const cleanup = async (): Promise<void> => {
    if (cleaned) {
      return;
    }

    cleaned = true;
    await rm(workDir, { force: true, recursive: true });
  };

  const estimatedBytes = estimateMp3Bytes(
    text,
    wordsPerMinute,
    ESTIMATED_MP3_BITRATE_KBPS,
  );
  let bytesWritten = 0;
  let piper: ChildProcessWithoutNullStreams | undefined;
  let ffmpeg: ChildProcessWithoutNullStreams | undefined;

  try {
    piper = spawnPiperPcmStdout(text, modelPath);
    ffmpeg = spawnFfmpegMp3FromPcm(sampleRate);
    piper.stderr.resume();
    ffmpeg.stderr.resume();

    const report = createThrottledProgressReporter(
      options.onProgress,
      PROGRESS_REPORT_INTERVAL_MS,
    );

    report(0);

    const counter = new Transform({
      transform(chunk: Buffer, _encoding, callback) {
        bytesWritten += chunk.length;
        report(generationProgress(bytesWritten, estimatedBytes));
        callback(null, chunk);
      },
    });

    await Promise.all([
      pipeline(piper.stdout, ffmpeg.stdin, { signal: options.signal }),
      pipeline(
        ffmpeg.stdout,
        counter,
        createWriteStream(filePath, { flags: "wx" }),
        { signal: options.signal },
      ),
    ]);
    await Promise.all([waitForClose(piper), waitForClose(ffmpeg)]);
    throwForFailedChild("Piper", piper);
    throwForFailedChild("ffmpeg", ffmpeg);

    const result = {
      filePath,
      fileSizeBytes: bytesWritten,
      generationDurationMs: performance.now() - startedAt,
      cleanup,
    };

    logger.info(
      `Synthesized text in ${result.filePath} (file size: ${result.fileSizeBytes}) in ${result.generationDurationMs} milliseconds`,
      { context: "synthesizeTextToMp3File" },
    );

    return result;
  } catch (error) {
    logger.warn("Synthesizing text failed, cleaning up...", {
      context: "synthesizeTextToMp3File",
    });
    if (piper) {
      killSafely(piper);
    }
    if (ffmpeg) {
      killSafely(ffmpeg);
    }
    await Promise.allSettled(
      [piper, ffmpeg]
        .filter(
          (child): child is ChildProcessWithoutNullStreams =>
            child !== undefined,
        )
        .map(waitForClose),
    );
    await cleanup();
    throw error;
  }
}

/**
 * @description This estimate is intentionally approximate: ffmpeg uses variable-bitrate MP3 encoding (`-q:a 2`), so output bytes are not an exact duration measure.
 */
export function estimateMp3Bytes(
  text: string,
  wordsPerMinute: number,
  bitrateKbps: number,
): number {
  const words = wordCounter(text);
  const seconds = (words / wordsPerMinute) * 60;

  return Math.max(1, Math.ceil(seconds * ((bitrateKbps * 1_000) / 8)));
}

/**
 * @description Keeps generation progress below completion until the whole job succeeds.
 */
export function generationProgress(
  bytesWritten: number,
  estimatedBytes: number,
): number {
  if (estimatedBytes <= 0) {
    return 0;
  }

  return Math.min(
    99,
    Math.max(0, Math.floor((bytesWritten / estimatedBytes) * 100)),
  );
}

/**
 * @description Coalesces frequent stream updates so status reporting cannot flood clients.
 */
export function createThrottledProgressReporter(
  onProgress: SynthesizeTextToMp3FileOptions["onProgress"],
  intervalMs: number,
): (percent: number) => void {
  logger.info(
    `Progress reporter initialized (throttled to ${intervalMs}ms intervals)`,
    { context: "createThrottledProgressReporter" },
  );

  let lastPercent = -1;
  let lastReportedAt = Number.NEGATIVE_INFINITY;

  return (percent: number): void => {
    if (percent <= lastPercent || Date.now() - lastReportedAt < intervalMs) {
      return;
    }

    lastPercent = percent;
    lastReportedAt = Date.now();

    try {
      onProgress?.(percent);
    } catch {
      // Status delivery is best-effort and must not interrupt audio generation.
    }
  };
}

export interface SynthesisFile {
  filePath: string;
  fileSizeBytes: number;
  generationDurationMs: number;
  cleanup(): Promise<void>;
}

export interface SynthesizeTextToMp3FileOptions {
  signal?: AbortSignal;
  onProgress?: (percent: number) => void;
}

function throwForFailedChild(
  name: string,
  child: ChildProcessWithoutNullStreams,
): void {
  if (child.exitCode !== 0) {
    throw new Error(`${name} exited with a non-zero status code`);
  }
}

function waitForClose(child: ChildProcessWithoutNullStreams): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve();
  }
  return new Promise((resolve) => child.once("close", resolve));
}

function killSafely(child: ChildProcessWithoutNullStreams): void {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }

  try {
    child.kill("SIGKILL");
  } catch {
    // The child already exited.
  }
}
