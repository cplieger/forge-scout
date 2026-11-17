package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// logBuf collects a daemon's JSON log lines.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// find returns the first line whose msg is msg.
func (l *logBuf) find(msg string) (map[string]any, bool) {
	for line := range strings.Lines(l.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == msg {
			return m, true
		}
	}
	return nil, false
}

func getenv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// newProc is a proc over vars that logs to the returned buffer.
func newProc(vars map[string]string, args ...string) (*proc, *logBuf, *bytes.Buffer) {
	logs, stderr := &logBuf{}, &bytes.Buffer{}
	level := &slog.LevelVar{}
	h := slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level})
	return &proc{args: args, getenv: getenv(vars), stderr: stderr, logger: slog.New(h), level: level}, logs, stderr
}

// daemon is a serve running in the test.
type daemon struct {
	logs   *logBuf
	level  *slog.LevelVar
	cancel context.CancelFunc
	code   chan int
	// listen and ops are the bound addresses the start line names.
	listen, ops string
}

// localVars are the variables of a daemon on free loopback ports in dir.
func localVars(dir string) map[string]string {
	return map[string]string{"DATA_DIR": dir, "LISTEN_ADDR": "127.0.0.1:0", "OPS_LISTEN_ADDR": "127.0.0.1:0"}
}

// start runs serve over vars and waits for its start line; it stops the
// daemon at cleanup.
func start(t *testing.T, vars map[string]string) *daemon {
	t.Helper()
	p, logs, _ := newProc(vars)
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{logs: logs, level: p.level, cancel: cancel, code: make(chan int, 1)}
	go func() { d.code <- run(ctx, p) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.code:
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if line, ok := logs.find("forge-scout started"); ok {
			d.listen, _ = line["listen"].(string)
			d.ops, _ = line["ops"].(string)
			return d
		}
		select {
		case c := <-d.code:
			d.code <- c
			t.Fatalf("serve exited %d before starting:\n%s", c, logs)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no start line within 10 s:\n%s", logs)
	return nil
}

// stop cancels the daemon and returns its exit code.
func (d *daemon) stop(t *testing.T) int {
	t.Helper()
	d.cancel()
	select {
	case c := <-d.code:
		d.code <- c
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop within 10 s")
		return -1
	}
}

// get sends GET path to addr with host as the Host header ("" keeps the
// address) and returns the status and body.
func get(t *testing.T, addr, host, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("GET %s%s: %v", addr, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s%s: %v", addr, path, err)
	}
	return resp.StatusCode, string(body)
}

// envelopeCode is the code of an error envelope body.
func envelopeCode(t *testing.T, body string) string {
	t.Helper()
	var env httpserver.ErrorResponse
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("not an error envelope: %q", body)
	}
	return string(env.Code)
}
