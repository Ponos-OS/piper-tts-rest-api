// Command server runs the Piper TTS REST API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

const (
	keyPort       = "port"
	keyPath       = "path"
	keySampleRate = "sampleRate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "piper-tts-rest-api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.New(cfg.Log, cfg.Service.Name, os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	// First SIGTERM/SIGINT makes httpapi.Serve start the graceful shutdown.
	// A second SIGTERM/SIGINT terminates the process immediately.
	defer stop()
	// Fire-and-forget/anonymous goroutine
	go func() {
		<-ctx.Done() // The goroutine waits for the first signal
		stop()       // Then goroutine calls stop()
	}()

	synth, err := piper.New(ctx, cfg.PiperModelPath)
	if err != nil {
		return fmt.Errorf("piper: %w", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(cfg.Service.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	boot := logging.Named(logger, "Bootstrap")
	boot.InfoContext(ctx, "TTS API running", slog.Int(keyPort, cfg.Service.Port))
	boot.InfoContext(ctx, "Piper model", slog.String(keyPath, cfg.PiperModelPath), slog.Int(keySampleRate, synth.SampleRate()))

	if err := httpapi.Serve(ctx, ln, httpapi.NewRouter(logger)); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}
