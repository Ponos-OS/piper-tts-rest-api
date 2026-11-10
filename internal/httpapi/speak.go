package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/trace"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

const (
	keyError = "error"
	keyJobID = "jobId"

	chunkSize = 32 * 1024

	// traceIDHeader lets the caller find the request's trace.
	traceIDHeader = "x-trace-id"

	contentTypeJSON = "application/json"
	contentTypeText = "text/plain"
)

var (
	errNoAudio    = errors.New("synthesis produced no audio")
	errClientGone = errors.New("client went away")
)

type speakRequest struct {
	Text string `json:"text" doc:"The text to speak." example:"Hello from Piper API" pattern:"\\S"`
}

type speaker struct {
	synth   *piper.Synthesizer
	slots   semaphore
	maxBody int
	log     *slog.Logger
}

type speakInput struct {
	ContentType string `header:"Content-Type"`
	RawBody     []byte
}

var errBlankText = huma.Error422UnprocessableEntity("Invalid request body", &huma.ErrorDetail{Message: "expected non-blank string", Location: "body.text"})

// speakDescription is Markdown shown in the API reference.
const speakDescription = `Streams the MP3 in the response, chunked, without a Content-Length.

Piper writes raw PCM to its stdout and ffmpeg encodes it to MP3 on the fly. The PCM sample rate is read once at startup from the model's ` + "`.onnx.json`" + ` sidecar.

**Concurrency and cancellation.** ` + "`MAX_CONCURRENCY`" + ` (default 10) caps the requests handled at once. A slot is held for the whole request: parsing, synthesis, encoding and streaming. When the client disconnects, the piper and ffmpeg processes are killed and awaited, then the slot is freed, so a queued request can start at once. Cancellation is per replica; there is no cross-replica cancellation. Pick a client timeout that allows for synthesis: piper takes several seconds before the first byte even for short inputs, and longer for longer text.

**Performance.** Each request starts a fresh piper process that loads the ONNX voice model (about 61 MB), a fixed cold start of several seconds of CPU. The piper CLI has no daemon mode, so this service targets batch and offline narration, not low-latency interactive use. For that, drive piper from Python with a long-lived model.`

func registerSpeak(api huma.API, s *speaker) {
	text := &huma.Schema{Type: typeString, Examples: []any{"Hello from plain text"}}
	huma.Register(api, huma.Operation{
		OperationID:      "speak",
		Method:           http.MethodPost,
		Path:             "/v1/speak",
		Summary:          "Synchronous text to speech",
		Description:      speakDescription,
		Tags:             []string{"Speech"},
		MaxBodyBytes:     int64(s.maxBody),
		SkipValidateBody: true,
		RequestBody: &huma.RequestBody{
			Required: true,
			Content: map[string]*huma.MediaType{
				contentTypeJSON: {Schema: huma.SchemaFromType(api.OpenAPI().Components.Schemas, reflect.TypeFor[speakRequest]())},
				contentTypeText: {Schema: text},
			},
		},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "The MP3 audio stream.",
				Headers: map[string]*huma.Param{
					"Content-Disposition": {Schema: &huma.Schema{Type: typeString}},
					"Cache-Control":       {Schema: &huma.Schema{Type: typeString}},
					traceIDHeader:         {Description: "The trace id, when OTEL_ENABLED=true.", Schema: &huma.Schema{Type: typeString}},
				},
				Content: map[string]*huma.MediaType{"audio/mpeg": {Schema: &huma.Schema{Type: typeString, Format: "binary"}}},
			},
		},
		Errors: []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusInternalServerError},
	}, s.handleSpeak)

	// Huma documents a RawBody as application/octet-stream; the two content types above are the real ones.
	delete(api.OpenAPI().Paths["/v1/speak"].Post.RequestBody.Content, "application/octet-stream")
}

// handleSpeak holds a concurrency slot for the whole request, so MAX_CONCURRENCY caps running pipelines end to end.
// A client that disconnects cancels the request context, which kills piper and ffmpeg.
func (s *speaker) handleSpeak(ctx context.Context, in *speakInput) (*huma.StreamResponse, error) {
	text, err := speakText(in)
	if err != nil {
		return nil, err
	}

	if acquireErr := s.slots.acquire(ctx); acquireErr != nil {
		return nil, huma.Error503ServiceUnavailable("The client left while queued")
	}

	stream, err := s.synth.Synthesize(ctx, text)
	if err != nil {
		s.slots.release()
		s.log.ErrorContext(ctx, "Synthesis failed to start", slog.Any(keyError, err))

		return nil, huma.Error500InternalServerError("Synthesis failed")
	}

	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		// Close runs before release (defers run last-in first-out), so the slot is freed only after both processes are gone.
		defer s.slots.release()
		defer stream.Close() //nolint:errcheck,gosec // it kills what is left; its error cannot change the response.

		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			hctx.SetHeader(traceIDHeader, sc.TraceID().String())
		}
		s.stream(ctx, hctx, stream)
	}}, nil
}

// speakText returns the text to speak: the "text" field of a JSON body, or the whole body for any other content type.
func speakText(in *speakInput) (string, error) {
	var text string

	if len(bytes.TrimSpace(in.RawBody)) == 0 {
		return "", errBlankText
	}

	if strings.Contains(strings.ToLower(in.ContentType), contentTypeJSON) {
		var req speakRequest
		dec := json.NewDecoder(bytes.NewReader(in.RawBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.More() {
			return "", huma.Error400BadRequest("Invalid JSON body")
		}
		text = req.Text
	} else {
		text = string(in.RawBody)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return "", errBlankText
	}

	return text, nil
}

// stream sends the MP3 to the client. After the first chunk a failure aborts the connection, so the client sees a truncated response instead of a complete-looking one.
func (s *speaker) stream(ctx context.Context, hctx huma.Context, stream io.Reader) {
	started, err := copyAudio(hctx, stream)
	if err == nil || ctx.Err() != nil || errors.Is(err, errClientGone) {
		return // done, or the client left and the deferred Close already killed the processes.
	}

	s.log.ErrorContext(ctx, "Synthesis failed", slog.Any(keyError, err))
	if !started {
		hctx.SetStatus(http.StatusInternalServerError)
		hctx.SetHeader("Content-Type", "application/problem+json")
		//nolint:errcheck,gosec // a failed write means the client went away; there is nobody to tell.
		hctx.BodyWriter().Write([]byte(`{"title":"Internal Server Error","status":500,"detail":"Synthesis failed"}`))

		return
	}
	panic(http.ErrAbortHandler)
}

// copyAudio copies MP3 chunks to the client and reports whether any were sent.
// Headers go out with the first chunk, so a pipeline that fails before producing audio can still answer 500.
// A failed write is reported as errClientGone.
func copyAudio(hctx huma.Context, stream io.Reader) (bool, error) {
	buf := make([]byte, chunkSize)
	started := false

	for {
		n, err := stream.Read(buf)
		if n > 0 {
			if !started {
				writeAudioHeaders(hctx)
				started = true
			}
			if werr := writeChunk(hctx, buf[:n]); werr != nil {
				return started, werr
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

// writeChunk writes and flushes one chunk; a failure means the client went away.
func writeChunk(hctx huma.Context, chunk []byte) error {
	w := hctx.BodyWriter()
	if _, err := w.Write(chunk); err != nil {
		return fmt.Errorf("%w: %w", errClientGone, err)
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		if err := http.NewResponseController(rw).Flush(); err != nil {
			return fmt.Errorf("%w: %w", errClientGone, err)
		}
	}

	return nil
}

func writeAudioHeaders(hctx huma.Context) {
	hctx.SetHeader("Content-Type", "audio/mpeg")
	hctx.SetHeader("Content-Disposition", fmt.Sprintf(`inline; filename="%d-%s.mp3"`, time.Now().UnixMilli(), rand.Text()))
	hctx.SetHeader("Cache-Control", "no-store")
	hctx.SetStatus(http.StatusOK)
}
