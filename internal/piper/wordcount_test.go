package piper_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

func TestWordCount(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"words separated by spaces, tabs and newlines", "Hi, this is ME!\n\n Do you know my name?\t\t I'm Kasir\n\n At your service!\n", 14},
		{"empty", "", 0},
		{"tabs only", "\t\t", 0},
		{"newlines only", "\n\n", 0},
		{"tab and newline", "\t\n", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, piper.WordCount(tt.text))
		})
	}
}
