// Package server is the product listener's handler: the routes under the
// middleware every request passes, the host check among them.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/textsafe"
)

// csp is the page policy: everything from forge-scout's own origin, no
// inline script or style, no framing, and no image from a forge's CDN.
const csp = "default-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'"

// Config is what Handler serves. Every field is required.
type Config struct {
	// UI answers every path no other route takes.
	UI http.Handler
	// Hosts is the host check, from HostPolicy.
	Hosts *httpserver.HostPolicy
	// Record takes each completed request's metric.
	Record func(httpserver.RequestMetric)
	// Logger takes the access lines, host refusals and recovered panics.
	Logger *slog.Logger
}

// Handler returns the product listener's handler: an access line and a
// metric for every request, a recovered panic answered 500, a Host the
// policy refuses answered 403 host_not_allowed, and the security headers.
func Handler(c *Config) (http.Handler, error) {
	switch {
	case c.UI == nil:
		return nil, errors.New("server: no UI handler")
	case c.Hosts == nil:
		return nil, errors.New("server: no host policy")
	case c.Record == nil:
		return nil, errors.New("server: no metric hook")
	case c.Logger == nil:
		return nil, errors.New("server: no logger")
	}
	mux := http.NewServeMux()
	mux.Handle("/", c.UI)
	return httpserver.Chain(mux,
		httpserver.Logging(httpserver.WithLogger(c.Logger), httpserver.WithRecordRouteMetric(c.Record)),
		httpserver.Recoverer(httpserver.WithRecoverLogger(c.Logger)),
		c.Hosts.Middleware(),
		httpserver.SecurityHeaders(httpserver.WithCSP(csp)),
	), nil
}

// Hosts is the host list both listeners admit: localhost, which a browser
// on the machine publishing the port sends, then each of allowed.
func Hosts(allowed []string) []string {
	return append([]string{"localhost"}, allowed...)
}

// maxShownHost caps the bytes of each refused ALLOWED_HOSTS entry the start
// error echoes: enough to tell the entry, never a pasted blob whole.
const maxShownHost = 64

// HostPolicy builds the product listener's host check over hosts, with a
// request from this host's own network stack admitted. Any entry
// httpserver.CanonicalHost refuses fails it, naming the entries: dropping
// one would leave a name the operator listed refused with no word why.
func HostPolicy(hosts []string, logger *slog.Logger) (*httpserver.HostPolicy, error) {
	p, invalid := httpserver.ParseHostList(hosts,
		httpserver.WithLoopbackExempt(true), httpserver.WithHostAllowlistLogger(logger))
	if len(invalid) > 0 {
		quoted := make([]string, len(invalid))
		for i, v := range invalid {
			quoted[i] = strconv.Quote(textsafe.SanitizeSingleLineBounded(v, maxShownHost))
		}
		return nil, fmt.Errorf("server: not a host name: %s", strings.Join(quoted, ", "))
	}
	return p, nil
}
