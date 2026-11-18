package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/forge-scout/internal/admin"
	"github.com/cplieger/forge-scout/internal/store"
	"github.com/cplieger/go/ownersock"
)

func TestServeStartsAndStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := start(t, localVars(dir))

	t.Run("start line", func(t *testing.T) {
		line, _ := d.logs.find("forge-scout started")
		for k, want := range map[string]string{"version": "dev", "file": filepath.Join(dir, store.FileName), "store": "open"} {
			if line[k] != want {
				t.Errorf("start line %s = %v, want %q", k, line[k], want)
			}
		}
	})
	t.Run("healthz", func(t *testing.T) {
		status, body := get(t, d.ops, "", "/healthz")
		if status != http.StatusOK || !strings.Contains(body, `"ok"`) {
			t.Errorf("/healthz = %d %s, want 200 ok", status, body)
		}
	})
	t.Run("metrics", func(t *testing.T) {
		status, body := get(t, d.ops, "", "/metrics")
		if status != http.StatusOK {
			t.Fatalf("/metrics = %d", status)
		}
		for _, want := range []string{
			`forge_scout_build_info{version="dev"} 1`,
			`forge_scout_store_open_failed{file="data"} 0`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("/metrics lacks %s", want)
			}
		}
	})
	t.Run("ops unknown path", func(t *testing.T) {
		status, body := get(t, d.ops, "", "/api/items")
		if status != http.StatusNotFound || envelopeCode(t, body) != "not_found" {
			t.Errorf("ops /api/items = %d %s, want 404 not_found", status, body)
		}
	})
	t.Run("product", func(t *testing.T) {
		status, body := get(t, d.listen, "", "/")
		if status != http.StatusNotFound || envelopeCode(t, body) != "not_found" {
			t.Errorf("product / = %d %s, want 404 not_found", status, body)
		}
		_, metrics := get(t, d.ops, "", "/metrics")
		if want := `forge_scout_http_requests_total{code="404",method="GET",route="/"}`; !strings.Contains(metrics, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	})
	t.Run("data file", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
			t.Errorf("data file: %v", err)
		}
	})
	t.Run("admin socket", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://admin/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ownersock.Client(admin.Path(dir)).Do(req)
		if err != nil {
			t.Fatalf("admin request: %v", err)
		}
		defer resp.Body.Close()
		var env struct{ Code string }
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound || env.Code != "not_found" {
			t.Errorf("admin / = %d %q, want 404 not_found", resp.StatusCode, env.Code)
		}
	})
	t.Run("health command", func(t *testing.T) {
		p, _, stderr := newProc(map[string]string{"OPS_LISTEN_ADDR": d.ops}, "health")
		if code := run(t.Context(), p); code != 0 {
			t.Errorf("health = %d, want 0; stderr %q", code, stderr)
		}
	})

	if code := d.stop(t); code != 0 {
		t.Fatalf("serve = %d after cancel, want 0:\n%s", code, d.logs)
	}
	if _, err := os.Lstat(admin.Path(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("admin socket after stop: %v, want it removed", err)
	}
	schema, err := store.NewSchema()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir, schema, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open after stop: %v, want the lock released", err)
	}
	if _, failed := st.Failure(); failed {
		t.Error("the data file does not open after a clean stop")
	}
	if err := st.Close(); err != nil {
		t.Error(err)
	}
}

func TestServeDataFileOutage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.FileName), []byte("not a bolt file"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := start(t, localVars(dir))

	line, _ := d.logs.find("forge-scout started")
	if line["store"] != "damaged" {
		t.Errorf("start line store = %v, want damaged", line["store"])
	}
	status, body := get(t, d.ops, "", "/healthz")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "the data file forge-scout.db cannot be opened: damaged") {
		t.Errorf("/healthz = %d %s, want 503 naming the file and damaged", status, body)
	}
	_, metrics := get(t, d.ops, "", "/metrics")
	if want := `forge_scout_store_open_failed{file="data"} 1`; !strings.Contains(metrics, want) {
		t.Errorf("/metrics lacks %s", want)
	}
	if status, _ := get(t, d.listen, "", "/"); status != http.StatusNotFound {
		t.Errorf("product / = %d, want the listener serving", status)
	}
	if code := d.stop(t); code != 0 {
		t.Errorf("serve = %d after cancel, want 0", code)
	}
}

func TestServeHostCheck(t *testing.T) {
	t.Parallel()
	vars := localVars(t.TempDir())
	vars["ALLOWED_HOSTS"] = " scout.example, ,forge-scout "
	d := start(t, vars)
	listeners := map[string]struct{ addr, path string }{"product": {d.listen, "/"}, "ops": {d.ops, "/healthz"}}
	for name, l := range listeners {
		t.Run(name+" refuses", func(t *testing.T) {
			status, body := get(t, l.addr, "evil.example", l.path)
			if status != http.StatusForbidden || envelopeCode(t, body) != "host_not_allowed" {
				t.Errorf("Host evil.example = %d %s, want 403 host_not_allowed", status, body)
			}
		})
		for _, host := range []string{"localhost:7466", "scout.example", "forge-scout:7467"} {
			t.Run(name+" admits "+host, func(t *testing.T) {
				if status, _ := get(t, l.addr, host, l.path); status == http.StatusForbidden {
					t.Errorf("Host %s refused", host)
				}
			})
		}
	}
}

func TestServeLogLevel(t *testing.T) {
	t.Parallel()
	t.Run("set", func(t *testing.T) {
		t.Parallel()
		vars := localVars(t.TempDir())
		vars["LOG_LEVEL"] = "DEBUG"
		d := start(t, vars)
		if got := d.level.Level(); got != slog.LevelDebug {
			t.Errorf("level = %v, want debug", got)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		vars := localVars(t.TempDir())
		vars["LOG_LEVEL"] = "loud"
		d := start(t, vars)
		if got := d.level.Level(); got != slog.LevelInfo {
			t.Errorf("level = %v, want info", got)
		}
		line, ok := d.logs.find("LOG_LEVEL names no level, logging at info")
		if !ok || line["level"] != "WARN" {
			t.Errorf("warning line = %v, want one WARN", line)
		}
		if strings.Contains(d.logs.String(), "loud") {
			t.Error("the log echoes the unknown value")
		}
	})
}

func TestServeRefusesStart(t *testing.T) {
	t.Parallel()
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		set  map[string]string
		want []string
	}{
		"same port":        {map[string]string{"LISTEN_ADDR": "127.0.0.1:17466", "OPS_LISTEN_ADDR": "127.0.0.1:17466"}, []string{"LISTEN_ADDR", "OPS_LISTEN_ADDR"}},
		"unspecified host": {map[string]string{"LISTEN_ADDR": ":17466", "OPS_LISTEN_ADDR": "127.0.0.1:17466"}, []string{"LISTEN_ADDR", "OPS_LISTEN_ADDR"}},
		"bad host":         {map[string]string{"ALLOWED_HOSTS": "ok.example,bad host"}, []string{"ALLOWED_HOSTS", `bad host`}},
		"relative dir":     {map[string]string{"DATA_DIR": "data"}, []string{"DATA_DIR", "not an absolute path"}},
		"missing dir":      {map[string]string{"DATA_DIR": "/nonexistent-forge-scout"}, []string{"DATA_DIR", "/nonexistent-forge-scout"}},
		"dir is a file":    {map[string]string{"DATA_DIR": notDir}, []string{"DATA_DIR", "not a directory"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			vars := localVars(dir)
			maps.Copy(vars, c.set)
			p, logs, _ := newProc(vars)
			if code := run(t.Context(), p); code != 1 {
				t.Errorf("serve = %d, want 1", code)
			}
			line, ok := logs.find("forge-scout cannot start")
			if !ok || line["level"] != "ERROR" {
				t.Fatalf("no ERROR start refusal:\n%s", logs)
			}
			for _, want := range c.want {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("refusal does not name %q:\n%s", want, logs)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, store.FileName)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("data file after a refused start: %v, want none", err)
			}
		})
	}
}

func TestServeRefusesHeldDataFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schema, err := store.NewSchema()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir, schema, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, logs, _ := newProc(localVars(dir))
	if code := run(t.Context(), p); code != 1 {
		t.Errorf("serve = %d, want 1", code)
	}
	if !strings.Contains(logs.String(), filepath.Join(dir, store.FileName)) {
		t.Errorf("refusal does not name the file:\n%s", logs)
	}
	if _, err := os.Lstat(admin.Path(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("admin socket: %v, want none", err)
	}
}

// TestServeClosesOnBindFailure takes each listener's port first, so its
// bind fails after the ones before it bound.
func TestServeClosesOnBindFailure(t *testing.T) {
	t.Parallel()
	for _, setting := range []string{"LISTEN_ADDR", "OPS_LISTEN_ADDR"} {
		t.Run(setting, func(t *testing.T) {
			t.Parallel()
			taken, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = taken.Close() })
			dir := t.TempDir()
			vars := localVars(dir)
			vars[setting] = taken.Addr().String()
			p, logs, _ := newProc(vars)
			if code := run(t.Context(), p); code != 1 {
				t.Errorf("serve = %d, want 1", code)
			}
			if !strings.Contains(logs.String(), setting) {
				t.Errorf("refusal does not name %s:\n%s", setting, logs)
			}
			if _, err := os.Lstat(admin.Path(dir)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("admin socket: %v, want it closed", err)
			}
			schema, err := store.NewSchema()
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(dir, schema, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("Open after a failed start: %v, want the lock released", err)
			}
			_ = st.Close()
		})
	}
}
