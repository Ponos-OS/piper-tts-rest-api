import { jest } from "@jest/globals";
import { ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import { access, readdir, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { Readable, Writable } from "node:stream";

const spawnFfmpegMock = jest.fn();
const spawnPiperMock = jest.fn();

jest.unstable_mockModule("./piper.js", () => ({
  spawnFfmpegMp3FromPcm: spawnFfmpegMock,
  spawnPiperPcmStdout: spawnPiperMock,
}));

const {
  createThrottledProgressReporter,
  estimateMp3Bytes,
  generationProgress,
  synthesizeTextToMp3File,
} = await import("./synthesis.js");

beforeEach(() => {
  jest.clearAllMocks();
});

describe(synthesizeTextToMp3File.name, () => {
  const modelPath = "/app/models/en_US-lessac-medium.onnx";

  it("should write streamed MP3 output to a temporary file and returns its metadata", async () => {
    // Arrange
    const mp3 = Buffer.from([0xff, 0xfb, 0x90, 0x00]);
    const piper = buildChild(Readable.from(Buffer.from("pcm")));
    const ffmpeg = buildChild(Readable.from(mp3));
    const progress: number[] = [];
    spawnPiperMock.mockReturnValue(piper);
    spawnFfmpegMock.mockReturnValue(ffmpeg);

    // Act
    const result = await synthesizeTextToMp3File(
      "hello world",
      modelPath,
      22050,
      150,
      { onProgress: (percent) => progress.push(percent) },
    );

    // Assert
    await expect(readFile(result.filePath)).resolves.toEqual(mp3);
    expect(result.fileSizeBytes).toBe(mp3.length);
    expect(result.generationDurationMs).toBeGreaterThanOrEqual(0);
    expect(progress).toEqual([0]);
    await result.cleanup();
    await expect(access(result.filePath)).rejects.toThrow();
  });

  it("should remove the temporary file when Piper fails", async () => {
    // Arrange
    const before = await temporaryDirectories();
    const piper = buildChild(Readable.from([]), 1);
    const ffmpeg = buildChild(Readable.from([]));
    spawnPiperMock.mockReturnValue(piper);
    spawnFfmpegMock.mockReturnValue(ffmpeg);

    // Act & Assert
    await expect(
      synthesizeTextToMp3File("hello", modelPath, 22050, 150),
    ).rejects.toThrow("Piper exited with a non-zero status code");
    await expect(temporaryDirectories()).resolves.toEqual(before);
  });

  it("should remove the temporary file when ffmpeg fails", async () => {
    // Arrange
    const before = await temporaryDirectories();
    const piper = buildChild(Readable.from(Buffer.from("pcm")));
    const ffmpeg = buildChild(Readable.from([]), 1);
    spawnPiperMock.mockReturnValue(piper);
    spawnFfmpegMock.mockReturnValue(ffmpeg);

    // Act & Assert
    await expect(
      synthesizeTextToMp3File("hello", modelPath, 22050, 150),
    ).rejects.toThrow("ffmpeg exited with a non-zero status code");
    await expect(temporaryDirectories()).resolves.toEqual(before);
  });

  it("should kill both children and removes temporary output when cancelled", async () => {
    // Arrange
    const before = await temporaryDirectories();
    const piper = buildChild(new Readable({ read() {} }), null);
    const ffmpeg = buildChild(new Readable({ read() {} }), null);
    spawnPiperMock.mockReturnValue(piper);
    spawnFfmpegMock.mockReturnValue(ffmpeg);
    const controller = new AbortController();
    controller.abort();

    // Act & Assert
    await expect(
      synthesizeTextToMp3File("hello", modelPath, 22050, 150, {
        signal: controller.signal,
      }),
    ).rejects.toThrow();
    expect(piper.kill).toHaveBeenCalledWith("SIGKILL");
    expect(ffmpeg.kill).toHaveBeenCalledWith("SIGKILL");
    await expect(temporaryDirectories()).resolves.toEqual(before);
  });
});

describe(estimateMp3Bytes.name, () => {
  it("should estimate bytes from words, speech rate, and bitrate", () => {
    expect(estimateMp3Bytes("one two three", 180, 128)).toBe(16_000);
  });
});

describe(generationProgress.name, () => {
  it("should be monotonic for growing byte counts and never reaches 100", () => {
    const progress = [0, 25, 50, 100, 10_000].map((bytes) =>
      generationProgress(bytes, 100),
    );

    expect(progress).toEqual([0, 25, 50, 99, 99]);
    expect(
      progress.every(
        (value, index) => index === 0 || value >= progress[index - 1],
      ),
    ).toBe(true);
  });
});

describe(createThrottledProgressReporter.name, () => {
  it("should coalesce duplicate and frequent updates while preserving increasing progress", () => {
    // Arrange
    const onProgress = jest.fn();
    const now = jest.spyOn(Date, "now");
    now.mockReturnValue(0);
    const report = createThrottledProgressReporter(onProgress, 500);

    // Act
    report(0);
    report(10);
    report(10);
    now.mockReturnValue(600);
    report(20);

    // Assert
    expect(onProgress).toHaveBeenNthCalledWith(1, 0);
    expect(onProgress).toHaveBeenNthCalledWith(2, 20);
    expect(onProgress).toHaveBeenCalledTimes(2);
    now.mockRestore();
  });
});

function buildChild(
  stdout: Readable,
  exitCode: number | null = 0,
): ChildProcessWithoutNullStreams {
  const child = new EventEmitter() as EventEmitter & {
    exitCode: number | null;
    kill: jest.Mock;
    signalCode: string | null;
    stderr: Readable;
    stdin: Writable;
    stdout: Readable;
  };

  child.exitCode = exitCode;
  child.signalCode = null;
  child.stdin = new Writable({
    write: (_chunk, _encoding, callback) => callback(),
  });
  child.stdout = stdout;
  child.stderr = new Readable({ read() {} });
  child.kill = jest.fn(() => {
    child.signalCode = "SIGKILL";
    child.emit("close", null, "SIGKILL");

    return true;
  });

  return child as unknown as ChildProcessWithoutNullStreams;
}

async function temporaryDirectories(): Promise<string[]> {
  const entries = await readdir(tmpdir());

  return entries.filter((entry) => entry.startsWith("piper-tts-")).sort();
}
