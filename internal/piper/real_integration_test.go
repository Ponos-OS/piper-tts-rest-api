//go:build integration

package piper_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// Runs the real piper and ffmpeg. Set PIPER_MODEL to a voice (.onnx) to enable it.
func TestSynthesize_RealPiperAndFfmpeg(t *testing.T) {
	model := os.Getenv("PIPER_MODEL")
	if model == "" {
		t.Skip("PIPER_MODEL is not set")
	}
	synth, err := piper.New(t.Context(), model)
	require.NoError(t, err)

	stream, err := synth.Synthesize(t.Context(), "Hello from the Go rewrite of the Piper text to speech service.")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stream.Close()) })
	mp3, err := io.ReadAll(stream)

	require.NoError(t, err)
	require.Greater(t, len(mp3), 10_000, "expected a few seconds of audio")
	// An MP3 stream starts with an ID3 tag or an MPEG frame sync (11 set bits).
	assert.True(t, string(mp3[:3]) == "ID3" || (mp3[0] == 0xFF && mp3[1]&0xE0 == 0xE0), "not an MP3 header: % x", mp3[:4])
	if dir := os.Getenv("MP3_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "out.mp3"), mp3, 0o600)) //nolint:gosec // a test output directory chosen by the developer.
	}
}
