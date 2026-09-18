package report

import (
	"context"
	"errors"
	"net"
	"strings"
)

// classify groups errors so the summary shows "timeout: 412" rather than 412
// near-identical lines of Go error text.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, net.ErrClosed):
		return "connection closed"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if strings.Contains(opErr.Error(), "connection refused") {
			return "connection refused"
		}
		if strings.Contains(opErr.Error(), "connection reset") {
			return "connection reset"
		}
		return "network"
	}

	text := err.Error()
	switch {
	case strings.Contains(text, "connection refused"):
		return "connection refused"
	case strings.Contains(text, "connection reset"):
		return "connection reset"
	case strings.Contains(text, "EOF"):
		return "unexpected EOF"
	case strings.Contains(text, "x509") || strings.Contains(text, "tls"):
		return "tls"
	}
	return "other"
}
