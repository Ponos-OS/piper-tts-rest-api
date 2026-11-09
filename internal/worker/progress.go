package worker

import (
	"io"
	"math"
	"time"
)

const (
	// progressInterval keeps progress callbacks from flooding the client.
	progressInterval = 500 * time.Millisecond

	estimatedBitrateKbps = 128
	bitsPerByte          = 8
	bitsPerKilobit       = 1000
	secondsPerMinute     = 60

	// maxProgressPercent: 100 is reported by the completed status instead.
	maxProgressPercent = 99
)

// estimateMP3Bytes guesses the size of the MP3 from the word count.
// It is approximate on purpose: ffmpeg encodes with a variable bitrate (-q:a 2), so size is not an exact measure of duration.
func estimateMP3Bytes(words, wordsPerMinute int) int64 {
	seconds := float64(words) / float64(wordsPerMinute) * secondsPerMinute
	bytesPerSecond := float64(estimatedBitrateKbps * bitsPerKilobit / bitsPerByte)

	return max(1, int64(math.Ceil(seconds*bytesPerSecond)))
}

// percentOf returns done out of total as 0-99.
func percentOf(done, total int64) int {
	if total <= 0 {
		return 0
	}

	return int(min(maxProgressPercent, max(0, done*100/total)))
}

// throttle forwards a percent only when it grew and the interval since the last forwarded one has passed.
// It is not safe for concurrent use; one goroutine reports to it.
type throttle struct {
	interval time.Duration
	send     func(percent int)
	last     int
	lastAt   time.Time
}

func newThrottle(interval time.Duration, send func(percent int)) *throttle {
	return &throttle{interval: interval, send: send, last: -1}
}

func (t *throttle) report(percent int) {
	if percent <= t.last || (!t.lastAt.IsZero() && time.Since(t.lastAt) < t.interval) {
		return
	}

	t.last, t.lastAt = percent, time.Now()
	t.send(percent)
}

// progressWriter reports how much of total was written.
type progressWriter struct {
	w       io.Writer
	total   int64
	written int64
	report  func(percent int)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	p.report(percentOf(p.written, p.total))

	return n, err //nolint:wrapcheck // io.Writer: the error belongs to the wrapped writer.
}

// progressReader reports how much of total was read.
type progressReader struct {
	r      io.Reader
	total  int64
	read   int64
	report func(percent int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	p.report(percentOf(p.read, p.total))

	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must come back unwrapped.
}
