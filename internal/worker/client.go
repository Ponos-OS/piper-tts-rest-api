package worker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	dialTimeout     = 10 * time.Second
	idleConnTimeout = 90 * time.Second
	maxIdleConns    = 10
)

var errBlockedAddress = errors.New("connection to a non-public address is not allowed")

// newClient returns the HTTP client for every call to a caller-supplied URL.
// It refuses to connect to addresses that are not on the public internet, except for hosts the operator listed in allowedHosts.
// It follows no redirects: a 3xx response is returned as is and counts as a failure.
// Each call is a client span and carries the trace context; the telemetry setup strips the query from the recorded URL.
// It uses no proxy, because the check runs on the address that is dialed and a proxy would be that address.
func newClient(allowedHosts []string) *http.Client {
	public := &net.Dialer{Timeout: dialTimeout, Control: refuseNonPublic}
	open := &net.Dialer{Timeout: dialTimeout}

	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(address); err == nil && slices.Contains(allowedHosts, strings.ToLower(host)) {
			return open.DialContext(ctx, network, address)
		}

		return public.DialContext(ctx, network, address)
	}

	return &http.Client{
		Transport: otelhttp.NewTransport(&http.Transport{
			DialContext:         dial,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: dialTimeout,
			IdleConnTimeout:     idleConnTimeout,
			MaxIdleConns:        maxIdleConns,
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// refuseNonPublic runs for the address about to be dialed, after DNS resolution,
// so a name that resolves to a private address is blocked too.
func refuseNonPublic(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}
	if isNonPublic(ap.Addr().Unmap()) {
		return errBlockedAddress
	}

	return nil
}

func isNonPublic(addr netip.Addr) bool {
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || // includes the cloud metadata address 169.254.169.254.
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}
