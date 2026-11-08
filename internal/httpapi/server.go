package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Guards against clients that open a connection and never finish sending headers.
// There is deliberately no read or write timeout: /speak streams for as long as synthesis takes.
const readHeaderTimeout = 10 * time.Second

// Serve serves handler on listener until context is cancelled, then stops accepting connections and waits for in-flight requests to finish.
// In-flight requests keep running because their contexts do not derive from context.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	failed := make(chan error, 1)
	go func() {
		failed <- srv.Serve(ln) // sends the error to the channel failed.
	}()

	select { // select waits for whichever happens first.
	case err := <-failed: // the server crashed on its own
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done(): // SIGTERM/SIGINT arrived
	}

	if err := srv.Shutdown(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-failed; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}
