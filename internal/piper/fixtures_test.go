package piper_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakes are shell script bodies for the piper and ffmpeg binaries; an empty body means the binary is not installed.
// Scripts see the directory they can write evidence to in $EVIDENCE.
type fakes struct {
	piper  string
	ffmpeg string
}

// install puts the fake binaries first on PATH and returns the evidence directory.
func install(t *testing.T, f fakes) string {
	t.Helper()
	binDir := t.TempDir()
	evidence := t.TempDir()

	if f.piper != "" {
		writeScript(t, filepath.Join(binDir, "piper"), "case \"$1\" in --help) exit 0;; esac\n"+f.piper)
	}
	if f.ffmpeg != "" {
		writeScript(t, filepath.Join(binDir, "ffmpeg"), "case \"$1\" in -version) exit 0;; esac\n"+f.ffmpeg)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("EVIDENCE", evidence)

	return evidence
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700)) //nolint:gosec // an executable test script.
}

// writeModel creates a model file and its sidecar config and returns the model path.
func writeModel(t *testing.T, sidecar string) string {
	t.Helper()
	model := filepath.Join(t.TempDir(), "voice.onnx")
	require.NoError(t, os.WriteFile(model, nil, 0o600))
	if sidecar != "" {
		require.NoError(t, os.WriteFile(model+".json", []byte(sidecar), 0o600))
	}

	return model
}

const validSidecar = `{"audio":{"sample_rate":22050}}`

func readEvidence(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a test file.
	require.NoError(t, err)

	return strings.TrimSpace(string(data))
}

// waitForEvidence waits until a script has written the file.
func waitForEvidence(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path) //nolint:gosec // a test file.

		return err == nil && strings.TrimSpace(string(data)) != ""
	}, 5*time.Second, 10*time.Millisecond, "%s was never written", name)

	return readEvidence(t, dir, name)
}

func pid(t *testing.T, dir, name string) int {
	t.Helper()
	n, err := strconv.Atoi(waitForEvidence(t, dir, name))
	require.NoError(t, err)

	return n
}

func running(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func requireGone(t *testing.T, pid int) {
	t.Helper()
	assert.Eventually(t, func() bool { return !running(pid) }, 5*time.Second, 10*time.Millisecond, "process %d is still running", pid)
}
