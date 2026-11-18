package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forge-scout/internal/server"
	"github.com/cplieger/go/httpserver/v2"
)

// recorder keeps every metric the handler records.
type recorder struct {
	mu   sync.Mutex
	seen []httpserver.RequestMetric
}

func (r *recorder) record(m httpserver.RequestMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, m)
}

func (r *recorder) metrics() []httpserver.RequestMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func policy(t *testing.T, allowed ...string) *httpserver.HostPolicy {
	t.Helper()
	p, err := server.HostPolicy(server.Hosts(allowed), discard())
	if err != nil {
		t.Fatalf("HostPolicy: %v", err)
	}
	return p
}

func handler(t *testing.T, ui http.Handler, rec *recorder) http.Handler {
	t.Helper()
	h, err := server.Handler(&server.Config{UI: ui, Hosts: policy(t, "scout.example"), Record: rec.record, Logger: discard()})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

func code(t *testing.T, body io.Reader) string {
	t.Helper()
	var env httpserver.ErrorResponse
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return string(env.Code)
}

var teapot = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

func TestHandlerRefusesMissingField(t *testing.T) {
	t.Parallel()
	full := func() server.Config {
		return server.Config{UI: teapot, Hosts: policy(t), Record: func(httpserver.RequestMetric) {}, Logger: discard()}
	}
	cases := map[string]func(*server.Config){
		"no UI":     func(c *server.Config) { c.UI = nil },
		"no hosts":  func(c *server.Config) { c.Hosts = nil },
		"no record": func(c *server.Config) { c.Record = nil },
		"no logger": func(c *server.Config) { c.Logger = nil },
	}
	for name, unset := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := full()
			unset(&c)
			if h, err := server.Handler(&c); err == nil || h != nil {
				t.Errorf("Handler = %v, %v; want nil and an error", h, err)
			}
		})
	}
}

func TestHandlerServesAdmittedHosts(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"localhost", "localhost:7466", "scout.example", "SCOUT.example:443", "192.168.1.20:7466", "[fd00::20]:7466"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			rec := &recorder{}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/anything", http.NoBody)
			req.Host = host
			w := httptest.NewRecorder()
			handler(t, teapot, rec).ServeHTTP(w, req)
			if w.Code != http.StatusTeapot {
				t.Errorf("status = %d, want %d", w.Code, http.StatusTeapot)
			}
			if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") || !strings.Contains(got, "frame-ancestors 'none'") {
				t.Errorf("Content-Security-Policy = %q", got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			want := []httpserver.RequestMetric{{Method: http.MethodGet, Path: "/", Status: http.StatusTeapot}}
			got := rec.metrics()
			for i := range got {
				got[i].Latency = 0
			}
			if !slices.Equal(got, want) {
				t.Errorf("metrics = %+v, want %+v", got, want)
			}
		})
	}
}

func TestHandlerRefusesOtherHost(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	called := false
	ui := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	handler(t, ui, rec).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if got := code(t, w.Body); got != "host_not_allowed" {
		t.Errorf("code = %q, want host_not_allowed", got)
	}
	if called {
		t.Error("the UI handler ran for a refused host")
	}
	if got := rec.metrics(); len(got) != 1 || got[0].Status != http.StatusForbidden {
		t.Errorf("metrics = %+v, want one 403", got)
	}
}

func TestHandlerRecoversPanic(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ui := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	handler(t, ui, rec).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if got := code(t, w.Body); got != "internal_error" {
		t.Errorf("code = %q, want internal_error", got)
	}
	if got := rec.metrics(); len(got) != 1 || got[0].Status != http.StatusInternalServerError {
		t.Errorf("metrics = %+v, want one 500", got)
	}
}

func TestHosts(t *testing.T) {
	t.Parallel()
	if got, want := server.Hosts(nil), []string{"localhost"}; !slices.Equal(got, want) {
		t.Errorf("Hosts(nil) = %q, want %q", got, want)
	}
	if got, want := server.Hosts([]string{"a.example", "b"}), []string{"localhost", "a.example", "b"}; !slices.Equal(got, want) {
		t.Errorf("Hosts = %q, want %q", got, want)
	}
}

func TestHostPolicyRefusesBadEntries(t *testing.T) {
	t.Parallel()
	p, err := server.HostPolicy([]string{"ok.example", "bad host", "a..b"}, discard())
	if err == nil || p != nil {
		t.Fatalf("HostPolicy = %v, %v; want nil and an error", p, err)
	}
	for _, want := range []string{`"bad host"`, `"a..b"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "ok.example") {
		t.Errorf("error %q names a valid entry", err)
	}
}

func TestHostPolicyCapsARefusedEntry(t *testing.T) {
	t.Parallel()
	long := "bad host " + strings.Repeat("x", 4096)
	_, err := server.HostPolicy([]string{long}, discard())
	if err == nil {
		t.Fatal("HostPolicy accepted the entry")
	}
	msg := err.Error()
	if strings.Contains(msg, long) || len(msg) > 128 {
		t.Errorf("error is %d bytes and echoes the whole entry: %q", len(msg), msg)
	}
	if !strings.Contains(msg, `"bad host xxx`) {
		t.Errorf("error %q does not name the entry's start", msg)
	}
}
