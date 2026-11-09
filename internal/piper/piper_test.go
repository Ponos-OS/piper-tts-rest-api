package piper_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name      string
		fakes     fakes
		sidecar   string
		noModel   bool
		wantError string
	}{
		{"everything available", fakes{piper: "exit 0\n", ffmpeg: "exit 0\n"}, validSidecar, false, ""},
		{"piper missing", fakes{ffmpeg: "exit 0\n"}, validSidecar, false, "piper is not installed or not available in PATH"},
		{"model missing", fakes{piper: "exit 0\n", ffmpeg: "exit 0\n"}, validSidecar, true, "piper model not found"},
		{"sidecar missing", fakes{piper: "exit 0\n", ffmpeg: "exit 0\n"}, "", false, "piper model config not found"},
		{"ffmpeg missing", fakes{piper: "exit 0\n"}, validSidecar, false, "ffmpeg is not installed or not available in PATH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			install(t, tt.fakes)
			model := writeModel(t, tt.sidecar)
			if tt.noModel {
				require.NoError(t, os.Remove(model))
			}

			synth, err := piper.New(t.Context(), model)

			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, 22050, synth.SampleRate())
		})
	}
}

func TestNew_SampleRate(t *testing.T) {
	tests := []struct {
		name      string
		sidecar   string
		want      int
		wantError string
	}{
		{"integer", `{"audio":{"sample_rate":16000}}`, 16000, ""},
		{"extra fields are ignored", `{"audio":{"sample_rate":22050,"quality":"medium"},"num_speakers":1}`, 22050, ""},
		{"missing audio", `{}`, 0, "invalid or missing audio.sample_rate"},
		{"missing sample_rate", `{"audio":{}}`, 0, "invalid or missing audio.sample_rate"},
		{"zero", `{"audio":{"sample_rate":0}}`, 0, "invalid or missing audio.sample_rate"},
		{"negative", `{"audio":{"sample_rate":-1}}`, 0, "invalid or missing audio.sample_rate"},
		{"fraction", `{"audio":{"sample_rate":22050.5}}`, 0, "sample_rate"},
		{"string", `{"audio":{"sample_rate":"22050"}}`, 0, "sample_rate"},
		{"not json", `not json`, 0, "parse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			install(t, fakes{piper: "exit 0\n", ffmpeg: "exit 0\n"})

			synth, err := piper.New(t.Context(), writeModel(t, tt.sidecar))

			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, synth.SampleRate())
		})
	}
}
