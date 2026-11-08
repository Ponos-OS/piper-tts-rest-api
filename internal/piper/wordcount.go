// Package piper runs Piper and ffmpeg to turn text into audio.
package piper

import "strings"

// WordCount returns the number of whitespace-separated words in text.
func WordCount(text string) int {
	return len(strings.Fields(text))
}
