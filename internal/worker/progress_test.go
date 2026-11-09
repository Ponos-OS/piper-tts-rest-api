package worker

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateMP3Bytes(t *testing.T) {
	tests := []struct {
		name  string
		words int
		wpm   int
		want  int64
	}{
		{"one word at 150 wpm is 0.4 s of 16 kB/s audio", 1, 150, 6400},
		{"three words", 3, 150, 19200},
		{"never less than one byte", 0, 150, 1},
		{"a slower voice means more audio", 3, 75, 38400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, estimateMP3Bytes(tt.words, tt.wpm))
		})
	}
}

func TestPercentOf(t *testing.T) {
	tests := []struct {
		name  string
		done  int64
		total int64
		want  int
	}{
		{"nothing yet", 0, 100, 0},
		{"half", 50, 100, 50},
		{"rounds down", 1, 3, 33},
		{"never reaches 100, the completed status says that", 100, 100, 99},
		{"an underestimate stays at 99", 500, 100, 99},
		{"unknown total", 5, 0, 0},
		{"negative is clamped", -5, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, percentOf(tt.done, tt.total))
		})
	}
}

func TestThrottle_OnlyForwardsGrowingPercents(t *testing.T) {
	var sent []int
	th := newThrottle(0, func(p int) { sent = append(sent, p) })

	for _, p := range []int{0, 10, 10, 5, 20} {
		th.report(p)
	}

	assert.Equal(t, []int{0, 10, 20}, sent)
}

func TestThrottle_HoldsBackUpdatesInsideTheInterval(t *testing.T) {
	var sent []int
	th := newThrottle(time.Hour, func(p int) { sent = append(sent, p) })

	for _, p := range []int{0, 10, 20} {
		th.report(p)
	}

	assert.Equal(t, []int{0}, sent)
}

func TestProgressWriter_ReportsTheShareWritten(t *testing.T) {
	var reported []int
	w := &progressWriter{w: io.Discard, total: 10, report: func(p int) { reported = append(reported, p) }}

	_, err := w.Write(make([]byte, 5))
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 5))
	require.NoError(t, err)

	assert.Equal(t, []int{50, 99}, reported)
}

func TestProgressReader_ReportsTheShareRead(t *testing.T) {
	var reported []int
	r := &progressReader{r: bytes.NewReader(make([]byte, 10)), total: 10, report: func(p int) { reported = append(reported, p) }}

	_, err := r.Read(make([]byte, 4))
	require.NoError(t, err)
	_, err = io.ReadAll(r)
	require.NoError(t, err)

	assert.Equal(t, []int{40, 99, 99}, reported)
}
