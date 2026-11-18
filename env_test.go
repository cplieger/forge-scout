package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadEnvDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e, err := readEnv(getenv(map[string]string{"DATA_DIR": dir}))
	if err != nil {
		t.Fatal(err)
	}
	if e.listen != ":7466" || e.ops != ":7467" || e.dataDir != dir || e.hosts != nil || e.level != slog.LevelInfo || e.levelUnknown {
		t.Errorf("readEnv = %+v, want the defaults", e)
	}
}

func TestReadEnvDefaultDataDir(t *testing.T) {
	t.Parallel()
	// Where /data exists the default is read; elsewhere the refusal names it.
	e, err := readEnv(getenv(nil))
	if err == nil && e.dataDir != "/data" {
		t.Errorf("dataDir = %q, want /data", e.dataDir)
	}
	if err != nil && !strings.Contains(err.Error(), `DATA_DIR "/data"`) {
		t.Errorf("error = %v, want DATA_DIR and its default named", err)
	}
}

func TestReadEnvValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e, err := readEnv(getenv(map[string]string{
		"DATA_DIR":        dir,
		"LISTEN_ADDR":     " 127.0.0.1:8000\t",
		"OPS_LISTEN_ADDR": " :9000 ",
		"ALLOWED_HOSTS":   " a.example, ,b ,",
		"LOG_LEVEL":       "warning",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if e.listen != "127.0.0.1:8000" {
		t.Errorf("listen = %q", e.listen)
	}
	if e.ops != ":9000" {
		t.Errorf("ops = %q", e.ops)
	}
	if want := []string{"a.example", "b"}; !slices.Equal(e.hosts, want) {
		t.Errorf("hosts = %q, want %q", e.hosts, want)
	}
	if e.level != slog.LevelWarn || e.levelUnknown {
		t.Errorf("level = %v unknown %v, want warn", e.level, e.levelUnknown)
	}
}

func TestReadEnvCapsARefusedDataDir(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("./", 300)
	cases := map[string]string{
		"relative":        "data/" + long,
		"missing":         "/nonexistent-forge-scout/" + long,
		"not a directory": filepath.Dir(file) + "/" + long + "file",
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := readEnv(getenv(map[string]string{"DATA_DIR": dir}))
			if err == nil {
				t.Fatal("readEnv accepted the directory")
			}
			msg := err.Error()
			if strings.Contains(msg, dir) || len(msg) > 400 {
				t.Errorf("error is %d bytes and echoes the whole value: %q", len(msg), msg)
			}
			if !strings.HasPrefix(msg, `DATA_DIR "`+dir[:16]) {
				t.Errorf("error %q does not name DATA_DIR and the value's start", msg)
			}
		})
	}
}

func TestReadEnvBlankAddresses(t *testing.T) {
	t.Parallel()
	e, err := readEnv(getenv(map[string]string{"DATA_DIR": t.TempDir(), "LISTEN_ADDR": "  ", "OPS_LISTEN_ADDR": " "}))
	if err != nil {
		t.Fatal(err)
	}
	if e.listen != ":7466" || e.ops != ":7467" {
		t.Errorf("listen, ops = %q, %q; want the defaults", e.listen, e.ops)
	}
}

func TestReadEnvUnknownLevel(t *testing.T) {
	t.Parallel()
	e, err := readEnv(getenv(map[string]string{"DATA_DIR": t.TempDir(), "LOG_LEVEL": "loud"}))
	if err != nil {
		t.Fatal(err)
	}
	if e.level != slog.LevelInfo || !e.levelUnknown {
		t.Errorf("level = %v unknown %v, want info and unknown", e.level, e.levelUnknown)
	}
}
