package admin_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/cplieger/forge-scout/internal/admin"
	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/ownersock"
)

func TestPath(t *testing.T) {
	t.Parallel()
	if got, want := admin.Path("/data"), "/data/run/admin.sock"; got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestSocketAnswersNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)
	ln, err := admin.Listen(dir, logger)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := admin.Server(logger)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	path := admin.Path(dir)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no socket at Path: %v", err)
	}
	t.Run("not found", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://admin/refresh", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ownersock.Client(path).Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		var env httpserver.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.Code != "not_found" {
			t.Errorf("code = %q, want not_found", env.Code)
		}
	})

	if err := srv.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Serve = %v, want http.ErrServerClosed", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket after close: %v, want it removed", err)
	}
}
