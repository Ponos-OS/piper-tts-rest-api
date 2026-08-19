import {
  type ChildProcess,
  type ChildProcessWithoutNullStreams,
  execFileSync,
  spawn,
  spawnSync,
} from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { logger, wordCounter } from "../shared/index.js";

/**
 * Ensures Piper CLI and model are available before serving requests.
 */
export function verifyPiperInstallation(modelPath: string): void {
  try {
    execFileSync("piper", ["--help"], { stdio: "pipe" });
  } catch {
    throw new Error("Piper is not installed or not available in PATH");
  }

  if (!existsSync(modelPath)) {
    throw new Error(`Piper model not found: ${modelPath}`);
  }

  if (!existsSync(`${modelPath}.json`)) {
    throw new Error(`Piper model config not found: ${modelPath}.json`);
  }
}

/**
 * @summary Reads the Piper model sample rate from its sidecar config.
 * @description Piper ships a JSON sidecar next to each model (`<model>.onnx.json`) with the audio settings the voice was trained for. We read `audio.sample_rate` from that file because:
 *
 * - Piper's `--output-raw` mode writes headerless PCM, so the stream does not carry its own sample rate [ref](https://github.com/OHF-Voice/piper1-gpl/blob/main/docs/CLI.md).
 * - `ffmpeg` requires raw audio parameters to be supplied explicitly. In this pipeline that means `-f s16le` for the PCM format and `-ar` for the sample rate. Learn more [here](https://ffmpeg.org/ffmpeg-formats.html#Raw-PCM-muxers) and [here](https://ffmpeg.org/ffmpeg.html#Audio-Options).
 *
 * Read once at startup and cache the result; the model config is effectively immutable for the lifetime of the process.
 */
export function readPiperModelSampleRate(modelPath: string): number {
  logger.info(`Reading Piper model (${modelPath}) sample rate`);

  const configPath = `${modelPath}.json`;
  const raw = readFileSync(configPath, "utf8");
  const parsed = JSON.parse(raw) as { audio?: { sample_rate?: unknown } };
  const rate = parsed.audio?.sample_rate;

  if (typeof rate !== "number" || !Number.isInteger(rate) || rate <= 0) {
    throw new Error(
      `Invalid or missing audio.sample_rate in ${configPath} (got: ${String(rate)})`,
    );
  }

  logger.verbose(`Piper model sample rate is: ${rate}`);

  return rate;
}

/**
 * Ensures ffmpeg is available before serving requests.
 */
export function verifyFfmpegInstallation(): void {
  try {
    execFileSync("ffmpeg", ["-version"], { stdio: "pipe" });
  } catch {
    throw new Error("ffmpeg is not installed or not available in PATH");
  }
}

/**
 * Generates MP3 bytes from input text by first synthesizing WAV via Piper
 * and then transcoding to MP3 via ffmpeg.
 */
export function synthesizeTextToMp3Buffer(
  text: string,
  modelPath: string,
): Buffer {
  const workDir = mkdtempSync(join(tmpdir(), "tts-api-"));
  const fileId = randomUUID();
  const wavPath = join(workDir, `${fileId}.wav`);
  const mp3Path = join(workDir, `${fileId}.mp3`);

  try {
    const piperResult = spawnSync(
      "piper",
      ["--model", modelPath, "--output_file", wavPath],
      {
        input: text,
        encoding: "utf8",
        stdio: ["pipe", "pipe", "pipe"],
      },
    );

    if (piperResult.status !== 0) {
      throw new Error(
        (piperResult.stderr || "").trim() ||
          "Piper exited with a non-zero status code",
      );
    }

    const ffmpegResult = spawnSync(
      "ffmpeg",
      [
        "-y",
        "-loglevel",
        "error",
        "-i",
        wavPath,
        "-codec:a",
        "libmp3lame",
        "-q:a",
        "2",
        mp3Path,
      ],
      { stdio: ["ignore", "pipe", "pipe"] },
    );

    if (ffmpegResult.status !== 0) {
      throw new Error(
        (ffmpegResult.stderr || "").toString().trim() ||
          "ffmpeg exited with a non-zero status code",
      );
    }

    return readFileSync(mp3Path);
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

/**
 * Assert helper to narrow ChildProcess -> ChildProcessWithoutNullStreams.
 */
function assertStreams(
  cp: ChildProcess,
): asserts cp is ChildProcessWithoutNullStreams {
  if (!cp.stdin || !cp.stdout || !cp.stderr) {
    throw new Error(
      "Child process stdio not piped (stdin/stdout/stderr is null)",
    );
  }
}

/**
 * @description Spawn Piper and emit raw 16-bit signed little-endian PCM samples to stdout (no WAV header). The piper-rewrite (piper-tts >= 1.3.0, the `OHF-voice/piper1-gpl` Python project) does not honour `--output_file -` as a stdout sentinel — it silently produces zero bytes. `--output-raw` is the new way to stream synthesis output.
 *
 * The downstream ffmpeg invocation must be told the sample rate explicitly (the WAV header is gone), see `spawnFfmpegMp3FromPcm`.
 *
 * Performance note: every call spawns a fresh `piper` process which reloads the ONNX model (~61 MB for medium voices) and initialises the ONNX runtime before synthesis can start. This costs ~1.7–2 s per request for the bundled voices, regardless of input length.
 *
 * This is a deliberate design choice, not an oversight to be "fixed":
 * - The piper CLI has no daemon mode; a process pool would not help because every new process pays the same load cost.
 * - The only way to amortise the model-load cost is to drive piper from its Python library with a single long-lived model handle, which means abandoning the CLI wrapper entirely.
 * - The per-request process boundary makes cancellation and resource cleanup trivial — `SIGKILL` reclaims everything in one step. A shared in-memory model would require careful thread/queue management and per-job cancellation hooks inside the Python runtime.
 *
 * If you arrive here intending to add a process pool, please measure cold-start cost first and update this comment with your findings.
 */
export function spawnPiperPcmStdout(
  text: string,
  modelPath: string,
): ChildProcessWithoutNullStreams {
  const args = ["--model", modelPath, "--output-raw"];

  logger.info(
    `Initializing Piper pulse-code modulation stream with ${args.join(" ")} (${wordCounter(text)} words)`,
  );

  const childProcess = spawn("piper", args, {
    stdio: ["pipe", "pipe", "pipe"],
  });

  assertStreams(childProcess);

  childProcess.stdin.write(text + "\n"); // newline helps ensure synthesis starts
  childProcess.stdin.end();

  return childProcess;
}

/**
 * @description Spawn ffmpeg to read raw 16-bit signed little-endian mono PCM from stdin (at `sampleRate` Hz) and encode MP3 to stdout. Pairs with `spawnPiperPcmStdout`.
 */
export function spawnFfmpegMp3FromPcm(
  sampleRate: number,
): ChildProcessWithoutNullStreams {
  const args = [
    "-loglevel",
    "error",
    "-f",
    "s16le", // raw, 16-bit signed little-endian samples
    "-ar",
    String(sampleRate), // sample rate from the Piper model config
    "-ac",
    "1", // mono
    "-i",
    "pipe:0",
    "-codec:a",
    "libmp3lame",
    "-q:a",
    "2", // adjust quality as needed
    "-f",
    "mp3",
    "pipe:1",
  ];

  logger.info(`ffmpeg PCM→MP3 encoder started: ${args.join(" ")}`);

  const childProcess = spawn("ffmpeg", args, {
    stdio: ["pipe", "pipe", "pipe"],
  });

  assertStreams(childProcess);

  return childProcess;
}
