import { jest } from "@jest/globals";
import { EventEmitter } from "node:events";
import { Readable, Writable } from "node:stream";

jest.unstable_mockModule("node:child_process", () => ({
  execFileSync: jest.fn(),
  spawn: jest.fn(),
  spawnSync: jest.fn(),
}));

jest.unstable_mockModule("node:crypto", () => ({
  randomUUID: jest.fn(),
}));

jest.unstable_mockModule("node:fs", () => ({
  existsSync: jest.fn(),
  mkdtempSync: jest.fn(),
  readFileSync: jest.fn(),
  rmSync: jest.fn(),
}));

jest.unstable_mockModule("node:os", () => ({
  tmpdir: jest.fn(),
}));

const childProcess = await import("node:child_process");
const crypto = await import("node:crypto");
const fs = await import("node:fs");
const os = await import("node:os");
const {
  readPiperModelSampleRate,
  spawnFfmpegMp3FromPcm,
  spawnPiperPcmStdout,
  synthesizeTextToMp3Buffer,
  verifyFfmpegInstallation,
  verifyPiperInstallation,
} = await import("./piper.js");

const execFileSyncMock = childProcess.execFileSync as jest.Mock;
const spawnMock = childProcess.spawn as jest.Mock;
const spawnSyncMock = childProcess.spawnSync as jest.Mock;
const randomUUIDMock = crypto.randomUUID as jest.Mock;
const existsSyncMock = fs.existsSync as jest.Mock;
const mkdtempSyncMock = fs.mkdtempSync as jest.Mock;
const readFileSyncMock = fs.readFileSync as jest.Mock;
const rmSyncMock = fs.rmSync as jest.Mock;
const tmpdirMock = os.tmpdir as jest.Mock;

beforeEach(() => {
  jest.clearAllMocks();
});

describe(verifyPiperInstallation.name, () => {
  describe("when piper CLI is not in PATH", () => {
    it("should throw an error stating Piper is not installed", () => {
      // Arrange
      execFileSyncMock.mockImplementation(() => {
        throw new Error("ENOENT");
      });

      // Act & Assert
      expect(() => verifyPiperInstallation("/models/voice.onnx")).toThrow(
        "Piper is not installed or not available in PATH",
      );
    });
  });

  describe("when piper CLI exists but model file is missing", () => {
    it("should throw an error containing the model path", () => {
      // Arrange
      execFileSyncMock.mockReturnValue(Buffer.from(""));
      existsSyncMock.mockReturnValueOnce(false);

      // Act & Assert
      expect(() => verifyPiperInstallation("/models/voice.onnx")).toThrow(
        "Piper model not found: /models/voice.onnx",
      );
    });
  });

  describe("when the model exists but its sidecar config is missing", () => {
    it("should throw an error mentioning the .json sidecar", () => {
      // Arrange
      execFileSyncMock.mockReturnValue(Buffer.from(""));
      existsSyncMock
        .mockReturnValueOnce(true) // model
        .mockReturnValueOnce(false); // sidecar

      // Act & Assert
      expect(() => verifyPiperInstallation("/models/voice.onnx")).toThrow(
        "Piper model config not found: /models/voice.onnx.json",
      );
    });
  });

  describe("when piper, model and sidecar are all available", () => {
    it("should return without throwing", () => {
      // Arrange
      execFileSyncMock.mockReturnValue(Buffer.from(""));
      existsSyncMock.mockReturnValue(true);

      // Act & Assert
      expect(() => verifyPiperInstallation("/models/voice.onnx")).not.toThrow();
      expect(execFileSyncMock).toHaveBeenCalledExactlyOnceWith(
        "piper",
        ["--help"],
        { stdio: "pipe" },
      );
    });
  });
});

describe(readPiperModelSampleRate.name, () => {
  describe("when the sidecar config has a valid integer sample_rate", () => {
    it("should return the sample rate as a number", () => {
      // Arrange
      readFileSyncMock.mockReturnValue(
        JSON.stringify({ audio: { sample_rate: 22050 } }),
      );

      // Act
      const result = readPiperModelSampleRate("/models/voice.onnx");

      // Assert
      expect(result).toBe(22050);
      expect(readFileSyncMock).toHaveBeenCalledExactlyOnceWith(
        "/models/voice.onnx.json",
        "utf8",
      );
    });
  });

  describe("when sample_rate is missing from the config", () => {
    it("should throw an error mentioning the sidecar path", () => {
      // Arrange
      readFileSyncMock.mockReturnValue(JSON.stringify({ audio: {} }));

      // Act & Assert
      expect(() => readPiperModelSampleRate("/models/voice.onnx")).toThrow(
        /Invalid or missing audio\.sample_rate in \/models\/voice\.onnx\.json/,
      );
    });
  });

  describe("when sample_rate is not an integer", () => {
    it("should throw on a non-integer numeric value", () => {
      // Arrange
      readFileSyncMock.mockReturnValue(
        JSON.stringify({ audio: { sample_rate: 22050.5 } }),
      );

      // Act & Assert
      expect(() => readPiperModelSampleRate("/models/voice.onnx")).toThrow(
        /Invalid or missing audio\.sample_rate/,
      );
    });

    it("should throw on a negative integer", () => {
      // Arrange
      readFileSyncMock.mockReturnValue(
        JSON.stringify({ audio: { sample_rate: -1 } }),
      );

      // Act & Assert
      expect(() => readPiperModelSampleRate("/models/voice.onnx")).toThrow(
        /Invalid or missing audio\.sample_rate/,
      );
    });

    it("should throw on a string value", () => {
      // Arrange
      readFileSyncMock.mockReturnValue(
        JSON.stringify({ audio: { sample_rate: "22050" } }),
      );

      // Act & Assert
      expect(() => readPiperModelSampleRate("/models/voice.onnx")).toThrow(
        /Invalid or missing audio\.sample_rate/,
      );
    });
  });

  describe("when the config file is not valid JSON", () => {
    it("should propagate the SyntaxError from JSON.parse", () => {
      // Arrange
      readFileSyncMock.mockReturnValue("{ not valid json");

      // Act & Assert
      expect(() => readPiperModelSampleRate("/models/voice.onnx")).toThrow(
        SyntaxError,
      );
    });
  });
});

describe("verifyFfmpegInstallation", () => {
  describe("when ffmpeg is not in PATH", () => {
    it("should throw an error stating ffmpeg is not installed", () => {
      // Arrange
      execFileSyncMock.mockImplementation(() => {
        throw new Error("ENOENT");
      });

      // Act & Assert
      expect(() => verifyFfmpegInstallation()).toThrow(
        "ffmpeg is not installed or not available in PATH",
      );
    });
  });

  describe("when ffmpeg is available", () => {
    it("should return without throwing and probe with -version", () => {
      // Arrange
      execFileSyncMock.mockReturnValue(Buffer.from("ffmpeg version 6.0"));

      // Act & Assert
      expect(() => verifyFfmpegInstallation()).not.toThrow();
      expect(execFileSyncMock).toHaveBeenCalledExactlyOnceWith(
        "ffmpeg",
        ["-version"],
        { stdio: "pipe" },
      );
    });
  });
});

describe("synthesizeTextToMp3Buffer", () => {
  const text = "hello world";
  const modelPath = "/app/models/en_US-lessac-medium.onnx";
  const workDir = "/tmp/tts-api-abc123";
  const fileId = "11111111-1111-1111-1111-111111111111";
  const mp3Bytes = Buffer.from([0xff, 0xfb, 0x90, 0x00]);

  beforeEach(() => {
    tmpdirMock.mockReturnValue("/tmp");
    mkdtempSyncMock.mockReturnValue(workDir);
    randomUUIDMock.mockReturnValue(fileId);
  });

  describe("when piper and ffmpeg both succeed", () => {
    it("should return the MP3 bytes read from disk", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" }) // piper
        .mockReturnValueOnce({ status: 0, stderr: "" }); // ffmpeg
      readFileSyncMock.mockReturnValueOnce(mp3Bytes);

      // Act
      const result = synthesizeTextToMp3Buffer(text, modelPath);

      // Assert
      expect(result).toEqual(mp3Bytes);
    });

    it("should invoke piper with --model, --output_file and pipe text to stdin", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({ status: 0, stderr: "" });
      readFileSyncMock.mockReturnValueOnce(mp3Bytes);

      // Act
      synthesizeTextToMp3Buffer(text, modelPath);

      // Assert
      expect(spawnSyncMock).toHaveBeenNthCalledWith(
        1,
        "piper",
        ["--model", modelPath, "--output_file", `${workDir}/${fileId}.wav`],
        expect.objectContaining({
          encoding: "utf8",
          input: text,
          stdio: ["pipe", "pipe", "pipe"],
        }),
      );
    });

    it("should invoke ffmpeg with libmp3lame to transcode the wav into the mp3 file", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({ status: 0, stderr: "" });
      readFileSyncMock.mockReturnValueOnce(mp3Bytes);

      // Act
      synthesizeTextToMp3Buffer(text, modelPath);

      // Assert
      const [cmd, args] = spawnSyncMock.mock.calls[1] as [string, string[]];
      expect(cmd).toBe("ffmpeg");
      expect(args).toIncludeAllMembers([
        "-i",
        `${workDir}/${fileId}.wav`,
        "-codec:a",
        "libmp3lame",
        `${workDir}/${fileId}.mp3`,
      ]);
    });

    it("should remove the temporary working directory afterwards", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({ status: 0, stderr: "" });
      readFileSyncMock.mockReturnValueOnce(mp3Bytes);

      // Act
      synthesizeTextToMp3Buffer(text, modelPath);

      // Assert
      expect(rmSyncMock).toHaveBeenCalledExactlyOnceWith(workDir, {
        force: true,
        recursive: true,
      });
    });
  });

  describe("when piper exits with a non-zero status code", () => {
    it("should throw with piper's stderr message when present", () => {
      // Arrange
      spawnSyncMock.mockReturnValueOnce({
        status: 1,
        stderr: "  piper: model load failed  ",
      });

      // Act & Assert
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow(
        "piper: model load failed",
      );
    });

    it("should throw a generic message when piper's stderr is empty", () => {
      // Arrange
      spawnSyncMock.mockReturnValueOnce({ status: 1, stderr: "" });

      // Act & Assert
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow(
        "Piper exited with a non-zero status code",
      );
    });

    it("should still clean up the working directory", () => {
      // Arrange
      spawnSyncMock.mockReturnValueOnce({ status: 1, stderr: "boom" });

      // Act
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow();

      // Assert
      expect(rmSyncMock).toHaveBeenCalledExactlyOnceWith(workDir, {
        force: true,
        recursive: true,
      });
    });
  });

  describe("when ffmpeg exits with a non-zero status code", () => {
    it("should throw with ffmpeg's stderr message when present", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({
          status: 1,
          stderr: Buffer.from("  ffmpeg: bad input  "),
        });

      // Act & Assert
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow(
        "ffmpeg: bad input",
      );
    });

    it("should throw a generic message when ffmpeg's stderr is empty", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({ status: 1, stderr: Buffer.from("") });

      // Act & Assert
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow(
        "ffmpeg exited with a non-zero status code",
      );
    });

    it("should still clean up the working directory", () => {
      // Arrange
      spawnSyncMock
        .mockReturnValueOnce({ status: 0, stderr: "" })
        .mockReturnValueOnce({ status: 1, stderr: Buffer.from("boom") });

      // Act
      expect(() => synthesizeTextToMp3Buffer(text, modelPath)).toThrow();

      // Assert
      expect(rmSyncMock).toHaveBeenCalledExactlyOnceWith(workDir, {
        force: true,
        recursive: true,
      });
    });
  });
});

/**
 * Build a fake ChildProcess that has piped stdio so `assertStreams` accepts it.
 */
function buildFakeChildProcess(): EventEmitter & {
  stderr: Readable;
  stdin: Writable & { end: jest.Mock; write: jest.Mock };
  stdout: Readable;
} {
  const proc = new EventEmitter() as EventEmitter & {
    stderr: Readable;
    stdin: Writable & { end: jest.Mock; write: jest.Mock };
    stdout: Readable;
  };
  proc.stdout = new Readable({ read() {} });
  proc.stderr = new Readable({ read() {} });
  const stdin = new Writable({
    write(_c, _e, cb) {
      cb();
    },
  });
  const writeMock = jest.fn().mockReturnValue(true);
  const endMock = jest.fn();
  (stdin as unknown as { write: jest.Mock }).write = writeMock;
  (stdin as unknown as { end: jest.Mock }).end = endMock;
  proc.stdin = stdin as Writable & { end: jest.Mock; write: jest.Mock };
  return proc;
}

describe("spawnPiperPcmStdout", () => {
  describe("when stdio streams are properly piped", () => {
    it("should spawn piper with --model and --output-raw", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      spawnPiperPcmStdout("hello", "/models/voice.onnx");

      // Assert
      expect(spawnMock).toHaveBeenCalledExactlyOnceWith(
        "piper",
        ["--model", "/models/voice.onnx", "--output-raw"],
        { stdio: ["pipe", "pipe", "pipe"] },
      );
    });

    it("should write the input text plus a trailing newline to stdin and close it", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      spawnPiperPcmStdout("hello", "/models/voice.onnx");

      // Assert
      expect(fakeProc.stdin.write).toHaveBeenCalledExactlyOnceWith("hello\n");
      expect(fakeProc.stdin.end).toHaveBeenCalledOnce();
    });

    it("should return the spawned child process", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      const result = spawnPiperPcmStdout("hello", "/models/voice.onnx");

      // Assert
      expect(result).toBe(fakeProc);
    });
  });

  describe("when the spawned child process has null stdio streams", () => {
    it("should throw because streams are not piped", () => {
      // Arrange
      const proc = new EventEmitter() as unknown as {
        stderr: null;
        stdin: null;
        stdout: null;
      };
      proc.stdin = null;
      proc.stdout = null;
      proc.stderr = null;
      spawnMock.mockReturnValue(proc);

      // Act & Assert
      expect(() => spawnPiperPcmStdout("hello", "/models/voice.onnx")).toThrow(
        "Child process stdio not piped (stdin/stdout/stderr is null)",
      );
    });
  });
});

describe("spawnFfmpegMp3FromPcm", () => {
  describe("when stdio streams are properly piped", () => {
    it("should spawn ffmpeg with the supplied sample rate and PCM input flags", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      spawnFfmpegMp3FromPcm(22050);

      // Assert
      expect(spawnMock).toHaveBeenCalledOnce();
      const [cmd, args, opts] = spawnMock.mock.calls[0] as [
        string,
        string[],
        Record<string, unknown>,
      ];
      expect(cmd).toBe("ffmpeg");
      expect(opts).toEqual({ stdio: ["pipe", "pipe", "pipe"] });
      expect(args).toIncludeAllMembers([
        "-f",
        "s16le",
        "-ar",
        "22050",
        "-ac",
        "1",
        "-i",
        "pipe:0",
        "-codec:a",
        "libmp3lame",
        "-f",
        "mp3",
        "pipe:1",
      ]);
    });

    it("should stringify the numeric sample rate when passed to ffmpeg", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      spawnFfmpegMp3FromPcm(48000);

      // Assert
      const [, args] = spawnMock.mock.calls[0] as [string, string[]];
      const arIndex = args.indexOf("-ar");
      expect(arIndex).toBeGreaterThanOrEqual(0);
      expect(args[arIndex + 1]).toBe("48000");
      expect(args[arIndex + 1]).toBeString();
    });

    it("should return the spawned child process", () => {
      // Arrange
      const fakeProc = buildFakeChildProcess();
      spawnMock.mockReturnValue(fakeProc);

      // Act
      const result = spawnFfmpegMp3FromPcm(22050);

      // Assert
      expect(result).toBe(fakeProc);
    });
  });

  describe("when the spawned child process has null stdio streams", () => {
    it("should throw because streams are not piped", () => {
      // Arrange
      const proc = new EventEmitter() as unknown as {
        stderr: null;
        stdin: null;
        stdout: null;
      };
      proc.stdin = null;
      proc.stdout = null;
      proc.stderr = null;
      spawnMock.mockReturnValue(proc);

      // Act & Assert
      expect(() => spawnFfmpegMp3FromPcm(22050)).toThrow(
        "Child process stdio not piped (stdin/stdout/stderr is null)",
      );
    });
  });
});
