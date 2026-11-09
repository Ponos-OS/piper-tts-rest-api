package piper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

var (
	errPiperMissing  = errors.New("piper is not installed or not available in PATH")
	errFfmpegMissing = errors.New("ffmpeg is not installed or not available in PATH")
	errModelMissing  = errors.New("piper model not found")
	errConfigMissing = errors.New("piper model config not found")
	errSampleRate    = errors.New("invalid or missing audio.sample_rate")
)

// Synthesizer turns text into MP3 audio with one Piper voice.
type Synthesizer struct {
	modelPath  string
	sampleRate int
}

// New checks that piper, ffmpeg, the model and its sidecar config are available and reads the model's sample rate. Call it once at startup.
func New(ctx context.Context, modelPath string) (*Synthesizer, error) {
	if err := exec.CommandContext(ctx, "piper", "--help").Run(); err != nil {
		return nil, fmt.Errorf("%w: %w", errPiperMissing, err)
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("%w: %w", errModelMissing, err)
	}
	if _, err := os.Stat(modelPath + ".json"); err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigMissing, err)
	}
	if err := exec.CommandContext(ctx, "ffmpeg", "-version").Run(); err != nil {
		return nil, fmt.Errorf("%w: %w", errFfmpegMissing, err)
	}

	sampleRate, err := readSampleRate(modelPath)
	if err != nil {
		return nil, err
	}

	return &Synthesizer{modelPath: modelPath, sampleRate: sampleRate}, nil
}

// SampleRate is the model's audio sample rate in Hz.
func (s *Synthesizer) SampleRate() int {
	return s.sampleRate
}

// readSampleRate reads audio.sample_rate from the sidecar config (<model>.json).
// Piper's raw output has no header, so ffmpeg must be told the rate explicitly.
func readSampleRate(modelPath string) (int, error) {
	configPath := modelPath + ".json"

	data, err := os.ReadFile(configPath) //nolint:gosec // the path comes from operator configuration.
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", configPath, err)
	}

	var config struct {
		Audio struct {
			SampleRate *int `json:"sample_rate"`
		} `json:"audio"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return 0, fmt.Errorf("parse %s: %w", configPath, err)
	}

	rate := config.Audio.SampleRate
	if rate == nil || *rate <= 0 {
		return 0, fmt.Errorf("%w in %s", errSampleRate, configPath)
	}

	return *rate, nil
}
