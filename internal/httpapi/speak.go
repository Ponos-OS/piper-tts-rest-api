package httpapi

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

const (
	keyError = "error"
	keyJobID = "jobId"

	chunkSize = 32 * 1024
)

var (
	errNoAudio    = errors.New("synthesis produced no audio")
	errClientGone = errors.New("client went away")
)

type speakRequest struct {
	Text string `json:"text" validate:"required,notblank"`
}

type speaker struct {
	synth   *piper.Synthesizer
	slots   semaphore
	maxBody int
	log     *slog.Logger
}

// handleSpeak holds a concurrency slot for the whole request, so MAX_CONCURRENCY caps running pipelines end to end.
// A client that disconnects cancels the request context, which kills piper and ffmpeg.
func (s *speaker) handleSpeak(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := s.slots.acquire(ctx); err != nil {
		return // the client left while queued.
	}
	defer s.slots.release()

	req, err := decodeJSON[speakRequest](w, r, s.maxBody)
	if err != nil {
		writeBodyError(w, r, s.log, err)

		return
	}

	stream, err := s.synth.Synthesize(ctx, strings.TrimSpace(req.Text))
	if err != nil {
		s.log.ErrorContext(ctx, "Synthesis failed to start", slog.Any(keyError, err))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "Synthesis failed"})

		return
	}
	// Close runs before release (defers run last-in first-out), so the slot is freed only after both processes are gone.
	defer stream.Close() //nolint:errcheck,gosec // it kills what is left; its error cannot change the response.

	s.stream(w, r, stream)
}

// stream sends the MP3 to the client. After the first chunk a failure aborts the connection, so the client sees a truncated response instead of a complete-looking one.
func (s *speaker) stream(w http.ResponseWriter, r *http.Request, stream io.Reader) {
	ctx := r.Context()

	started, err := copyAudio(w, stream)
	if err == nil || ctx.Err() != nil || errors.Is(err, errClientGone) {
		return // done, or the client left and the deferred Close already killed the processes.
	}

	s.log.ErrorContext(ctx, "Synthesis failed", slog.Any(keyError, err))
	if !started {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "Synthesis failed"})

		return
	}
	panic(http.ErrAbortHandler)
}

// copyAudio copies MP3 chunks to the client and reports whether any were sent.
// Headers go out with the first chunk, so a pipeline that fails before producing audio can still answer 500.
// A failed write is reported as errClientGone.
func copyAudio(w http.ResponseWriter, stream io.Reader) (bool, error) {
	rc := http.NewResponseController(w)
	buf := make([]byte, chunkSize)
	started := false

	for {
		n, err := stream.Read(buf)
		if n > 0 {
			if !started {
				writeAudioHeaders(w)
				started = true
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				return started, fmt.Errorf("%w: %w", errClientGone, werr)
			}
			if ferr := rc.Flush(); ferr != nil {
				return started, fmt.Errorf("%w: %w", errClientGone, ferr)
			}
		}

		switch {
		case errors.Is(err, io.EOF) && started:
			return true, nil
		case errors.Is(err, io.EOF):
			return false, errNoAudio
		case err != nil:
			return started, err //nolint:wrapcheck // it is logged as is.
		}
	}
}

func writeAudioHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "audio/mpeg")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="%d-%s.mp3"`, time.Now().UnixMilli(), rand.Text()))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
