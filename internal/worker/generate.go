package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const audioFileMode = 0o600

// audio is the generated MP3 on disk.
// It goes to disk first because the upload needs the exact Content-Length, which a stream does not know.
type audio struct {
	path string
	size int64
}

func (w *Worker) generate(ctx context.Context, dir, text string) (audio, error) {
	path := filepath.Join(dir, "audio.mp3")

	size, err := w.generateInto(ctx, path, text)
	if err != nil {
		// We are intentionally returning empty struct instead of nil
		return audio{}, newJobError(synthesis.CodeGenerationFailed, "Audio generation failed", err)
	}

	return audio{path: path, size: size}, nil
}

func (w *Worker) generateInto(ctx context.Context, path, text string) (int64, error) {
	stream, err := w.synth.Synthesize(ctx, text)
	if err != nil {
		return 0, fmt.Errorf("start synthesis: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, audioFileMode) //nolint:gosec // the path is built from our own temp directory.
	if err != nil {
		return 0, errors.Join(fmt.Errorf("create %s: %w", filepath.Base(path), err), stream.Close())
	}

	size, copyErr := io.Copy(file, stream)
	// Close kills what is left of piper and ffmpeg, so a failed copy never leaves them running.
	err = errors.Join(copyErr, stream.Close(), file.Close())
	if err != nil {
		return 0, fmt.Errorf("write audio: %w", err)
	}

	return size, nil
}
