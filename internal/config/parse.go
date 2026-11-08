package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	errNotPositiveInteger = errors.New("must be a positive integer")
	errNotBoolean         = errors.New("must be either true or false")
	errEmpty              = errors.New("must not be empty")
	errNotHostnames       = errors.New("must be comma-separated hostnames")
	errNotAMQPURL         = errors.New("must be a valid amqp or amqps URL")
	errNotAMQPScheme      = errors.New("must use amqp or amqps")
	errNotOneOf           = errors.New("is not an allowed value")
)

// intBitSize keeps parsed integers (and millisecond durations built from
// them) from overflowing on any platform.
const intBitSize = 31

// lookupFunc has the same shape as os.LookupEnv.
type lookupFunc func(key string) (string, bool)

// loader reads typed values and collects every problem, so one startup
// failure reports all misconfigured variables at once.
type loader struct {
	lookup lookupFunc
	errs   []error
}

func (l *loader) fail(name string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s %w", name, err))
}

func (l *loader) str(name, fallback string) string {
	if v, ok := l.lookup(name); ok {
		return v
	}

	return fallback
}

func (l *loader) name(name, fallback string) string {
	v := l.str(name, fallback)
	if strings.TrimSpace(v) == "" {
		l.fail(name, errEmpty)

		return fallback
	}

	return v
}

func (l *loader) oneOf(name, fallback string, allowed ...string) string {
	v := l.str(name, fallback)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail(name, fmt.Errorf("%w: %s", errNotOneOf, strings.Join(allowed, ", ")))

	return fallback
}

func (l *loader) positiveInt(name string, fallback int) int {
	raw, ok := l.lookup(name)
	if !ok {
		return fallback
	}

	v, err := strconv.ParseUint(raw, 10, intBitSize)
	if err != nil || v < 1 {
		l.fail(name, errNotPositiveInteger)

		return fallback
	}

	return int(v)
}

func (l *loader) millis(name string, fallback time.Duration) time.Duration {
	ms := l.positiveInt(name, int(fallback/time.Millisecond))

	return time.Duration(ms) * time.Millisecond
}

func (l *loader) boolean(name string, fallback bool) bool {
	raw, ok := l.lookup(name)
	if !ok {
		return fallback
	}

	switch raw {
	case "true":
		return true
	case "false":
		return false
	default:
		l.fail(name, errNotBoolean)

		return fallback
	}
}

func (l *loader) hosts(name string) []string {
	raw, ok := l.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		host := strings.ToLower(strings.TrimSpace(p))
		if host == "" || strings.Contains(host, "/") {
			l.fail(name, errNotHostnames)

			return nil
		}
		hosts = append(hosts, host)
	}

	return hosts
}

func (l *loader) amqpURL(name, fallback string) string {
	raw := l.str(name, fallback)

	u, err := url.Parse(raw)
	if err != nil {
		l.fail(name, errNotAMQPURL)

		return fallback
	}
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		l.fail(name, errNotAMQPScheme)

		return fallback
	}

	return raw
}
