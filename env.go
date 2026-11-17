package main

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/slogx"
)

// The variables the process reads, with their defaults.
const (
	envListen   envx.Key = "LISTEN_ADDR"
	envOps      envx.Key = "OPS_LISTEN_ADDR"
	envDataDir  envx.Key = "DATA_DIR"
	envHosts    envx.Key = "ALLOWED_HOSTS"
	envLogLevel envx.Key = "LOG_LEVEL"

	defaultListen  = ":7466"
	defaultOps     = ":7467"
	defaultDataDir = "/data"
)

// env is the environment as the process uses it.
type env struct {
	listen  string
	ops     string
	dataDir string
	// hosts are ALLOWED_HOSTS's entries, blank ones dropped; the host
	// check refuses the start on one it cannot use.
	hosts []string
	level slog.Level
	// levelUnknown is true when LOG_LEVEL names no level.
	levelUnknown bool
}

// readEnv reads the environment through get. It fails, naming the
// variable, only when DATA_DIR is not an existing absolute directory.
func readEnv(get func(string) string) (env, error) {
	src := envx.Source{Get: get}
	e := env{
		// Edge spaces in a bind address fail as a host lookup naming
		// neither the variable nor the cause.
		listen:  cmp.Or(strings.TrimSpace(src.String(envListen)), defaultListen),
		ops:     readOpsAddr(get),
		dataDir: cmp.Or(src.String(envDataDir), defaultDataDir),
	}
	for h := range strings.SplitSeq(src.String(envHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			e.hosts = append(e.hosts, h)
		}
	}
	level, ok := slogx.ParseLevel(src.String(envLogLevel), slog.LevelInfo)
	e.level, e.levelUnknown = level, !ok
	if !filepath.IsAbs(e.dataDir) {
		return env{}, fmt.Errorf("%s %q is not an absolute path", envDataDir, e.dataDir)
	}
	info, err := os.Stat(e.dataDir)
	if err != nil {
		return env{}, fmt.Errorf("%s %q: %w", envDataDir, e.dataDir, err)
	}
	if !info.IsDir() {
		return env{}, fmt.Errorf("%s %q: %w", envDataDir, e.dataDir, errNotDir)
	}
	return e, nil
}

var errNotDir = errors.New("not a directory")

// readOpsAddr is OPS_LISTEN_ADDR as the ops listener binds it, which the
// health probe reads too.
func readOpsAddr(get func(string) string) string {
	return httpserver.OpsAddr(envx.Source{Get: get}.String(envOps), defaultOps)
}
