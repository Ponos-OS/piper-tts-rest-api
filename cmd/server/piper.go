package main

import (
	"context"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

func PiperProvider(cfg config.Config) (*piper.Synthesizer, error) {
	return piper.New(context.Background(), cfg.PiperModelPath)
}
