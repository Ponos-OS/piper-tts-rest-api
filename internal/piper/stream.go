package piper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// processCount is piper and ffmpeg.
const processCount = 2

// waitDelay bounds how long Wait lingers on I/O after a process is gone.
const waitDelay = 5 * time.Second

// Stream is the MP3 output of one synthesis.
// Read until io.EOF, or Close early to abandon it. Close is always required: it stops both processes and waits until they are gone.
type Stream struct {
	out       *os.File
	cancel    context.CancelFunc
	done      chan struct{} // closed once both processes have been reaped
	err       error         // why the pipeline failed; set before done is closed
	closeOnce sync.Once
	closeErr  error
}

// Synthesize starts `piper --output-raw | ffmpeg` for text and returns the MP3 bytes as they are produced.
// Cancelling ctx, or closing the stream, kills both processes.
// A failure in either process is returned by Read in place of io.EOF.
func (s *Synthesizer) Synthesize(ctx context.Context, text string) (*Stream, error) {
	pcmR, pcmW, err := os.Pipe() // piper stdout -> ffmpeg stdin
	if err != nil {
		return nil, fmt.Errorf("create pipe: %w", err)
	}
	mp3R, mp3W, err := os.Pipe() // ffmpeg stdout -> us
	if err != nil {
		closeFiles(pcmR, pcmW)

		return nil, fmt.Errorf("create pipe: %w", err)
	}

	procCtx, cancel := context.WithCancel(ctx)
	piper := command(procCtx, "piper", "--model", s.modelPath, "--output-raw")
	piper.Stdin = strings.NewReader(text + "\n") // the newline makes sure synthesis starts
	piper.Stdout = pcmW
	ffmpeg := command(procCtx, "ffmpeg", ffmpegArgs(s.sampleRate)...)
	ffmpeg.Stdin = pcmR
	ffmpeg.Stdout = mp3W

	startErr := startBoth(piper, ffmpeg, cancel)
	// The children have their own copies of these ends. Closing ours means piper gets SIGPIPE if ffmpeg dies, and we get EOF on mp3R once ffmpeg exits.
	closeFiles(pcmR, pcmW, mp3W)
	if startErr != nil {
		cancel()
		closeFiles(mp3R)

		return nil, startErr
	}

	stream := &Stream{out: mp3R, cancel: cancel, done: make(chan struct{})}
	go stream.supervise(ctx, piper, ffmpeg)

	return stream, nil
}

// Read implements io.Reader. At the end of the audio it reports how the processes finished.
func (s *Stream) Read(b []byte) (int, error) {
	n, err := s.out.Read(b)
	if errors.Is(err, io.EOF) {
		<-s.done          // wait until both processes are reaped and s.err is written
		if s.err != nil { // now safe to read
			return n, s.err
		}
	}

	return n, err //nolint:wrapcheck // io.Reader callers compare against io.EOF.
}

// Close kills whatever is still running and returns once both processes have exited.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.closeErr = s.out.Close()
		<-s.done
	})

	return s.closeErr
}

// supervise reaps both processes. The first failure stops the other one, so a crashed ffmpeg does not leave a CPU-bound piper running.
func (s *Stream) supervise(parent context.Context, piper, ffmpeg *exec.Cmd) {
	defer close(s.done)

	results := make(chan error, processCount)
	go func() { results <- exitError("piper", piper.Wait()) }()
	go func() { results <- exitError("ffmpeg", ffmpeg.Wait()) }()

	var first error
	for range processCount {
		if err := <-results; err != nil && first == nil {
			first = err
			s.cancel()
		}
	}

	if first != nil && parent.Err() != nil {
		first = parent.Err() // the kill was ours: report the cancellation, not "signal: killed"
	}
	s.err = first
}

func exitError(name string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", name, err)
}

func startBoth(piper, ffmpeg *exec.Cmd, cancel context.CancelFunc) error {
	if err := piper.Start(); err != nil {
		return fmt.Errorf("start piper: %w", err)
	}
	if err := ffmpeg.Start(); err != nil {
		cancel()
		piper.Wait() //nolint:errcheck,gosec // killed on purpose; Wait only reaps it.

		return fmt.Errorf("start ffmpeg: %w", err)
	}

	return nil
}

// command builds a process in its own process group and, on cancellation, kills the whole group so nothing it spawned outlives it.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed binary names; the model path is operator configuration.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}
	cmd.WaitDelay = waitDelay

	return cmd
}

// ffmpegArgs reads headerless 16-bit mono PCM from stdin and writes MP3 to stdout.
func ffmpegArgs(sampleRate int) []string {
	return []string{
		"-loglevel", "error",
		"-f", "s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", "1",
		"-i", "pipe:0",
		"-codec:a", "libmp3lame",
		"-q:a", "2",
		"-f", "mp3",
		"pipe:1",
	}
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		f.Close() //nolint:errcheck,gosec // nothing useful to do if closing a pipe end fails.
	}
}
