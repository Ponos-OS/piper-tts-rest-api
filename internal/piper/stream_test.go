package piper_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// A piper that never produces audio: it records its pid and a child's pid, then waits.
const hangingPiper = `echo $$ > "$EVIDENCE/piper.pid"
sleep 30 &
echo $! > "$EVIDENCE/child.pid"
wait
`

func newSynth(t *testing.T, f fakes) (*piper.Synthesizer, string, string) {
	t.Helper()
	evidence := install(t, f)
	model := writeModel(t, validSidecar)
	synth, err := piper.New(t.Context(), model)
	require.NoError(t, err)

	return synth, model, evidence
}

func TestSynthesize_StreamsMP3AndPassesArguments(t *testing.T) {
	synth, model, evidence := newSynth(t, fakes{
		piper: `echo "$@" > "$EVIDENCE/piper.args"
printf 'pcm:%s' "$(cat)"
`,
		ffmpeg: `echo "$@" > "$EVIDENCE/ffmpeg.args"
cat
`,
	})

	stream, err := synth.Synthesize(t.Context(), "hello world")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stream.Close()) })
	got, err := io.ReadAll(stream)

	require.NoError(t, err)
	assert.Equal(t, "pcm:hello world", string(got))
	assert.Equal(t, "--model "+model+" --output-raw", readEvidence(t, evidence, "piper.args"))
	assert.Equal(t,
		"-loglevel error -f s16le -ar 22050 -ac 1 -i pipe:0 -codec:a libmp3lame -q:a 2 -f mp3 pipe:1",
		readEvidence(t, evidence, "ffmpeg.args"))
	require.NoError(t, stream.Close())
}

func TestSynthesize_PiperFailureSurfacesInsteadOfEOF(t *testing.T) {
	synth, _, _ := newSynth(t, fakes{
		piper:  "printf partial\nexit 3\n",
		ffmpeg: "cat\n",
	})

	stream, err := synth.Synthesize(t.Context(), "hi")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stream.Close()) })
	_, err = io.ReadAll(stream)

	require.ErrorContains(t, err, "piper: exit status 3")
}

func TestSynthesize_FfmpegFailureStopsPiper(t *testing.T) {
	synth, _, evidence := newSynth(t, fakes{
		piper:  hangingPiper,
		ffmpeg: "exit 1\n",
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	stream, err := synth.Synthesize(ctx, "hi")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stream.Close()) })
	_, err = io.ReadAll(stream)

	require.ErrorContains(t, err, "ffmpeg: exit status 1")
	assert.False(t, running(pid(t, evidence, "piper.pid")), "piper kept running after ffmpeg failed")
	requireGone(t, pid(t, evidence, "child.pid"))
}

func TestSynthesize_ContextCancellationKillsEverything(t *testing.T) {
	synth, _, evidence := newSynth(t, fakes{piper: hangingPiper, ffmpeg: "cat\n"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := synth.Synthesize(ctx, "hi")
	require.NoError(t, err)
	piperPID := pid(t, evidence, "piper.pid")
	childPID := pid(t, evidence, "child.pid")
	read := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(stream)
		read <- readErr
	}()

	cancel()

	select {
	case err = <-read:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after cancellation")
	}
	require.NoError(t, stream.Close())
	assert.False(t, running(piperPID), "piper is still running after Close returned")
	requireGone(t, childPID)
}

func TestSynthesize_CloseBeforeReadingKillsEverything(t *testing.T) {
	synth, _, evidence := newSynth(t, fakes{piper: hangingPiper, ffmpeg: "cat\n"})
	stream, err := synth.Synthesize(t.Context(), "hi")
	require.NoError(t, err)
	piperPID := pid(t, evidence, "piper.pid")
	childPID := pid(t, evidence, "child.pid")

	require.NoError(t, stream.Close())

	assert.False(t, running(piperPID), "piper is still running after Close returned")
	requireGone(t, childPID)
}

func TestSynthesize_CloseIsIdempotent(t *testing.T) {
	synth, _, _ := newSynth(t, fakes{piper: "cat >/dev/null\n", ffmpeg: "cat\n"})
	stream, err := synth.Synthesize(t.Context(), "hi")
	require.NoError(t, err)

	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())
}
