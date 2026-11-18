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
	"github.com/cplieger/go/textsafe"
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
		listen:  httpserver.ListenAddr(src.String(envListen), defaultListen),
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
	shown := textsafe.SanitizeSingleLineBounded(e.dataDir, maxShown)
	if !filepath.IsAbs(e.dataDir) {
		return env{}, fmt.Errorf("%s %q is not an absolute path", envDataDir, shown)
	}
	info, err := os.Stat(e.dataDir)
	if err != nil {
		// os.Stat's *PathError repeats the whole path; only its cause is kept.
		return env{}, fmt.Errorf("%s %q: %w", envDataDir, shown, errors.Unwrap(err))
	}
	if !info.IsDir() {
		return env{}, fmt.Errorf("%s %q: %w", envDataDir, shown, errNotDir)
	}
	return e, nil
}

var errNotDir = errors.New("not a directory")

// maxShown caps the bytes of a refused DATA_DIR a start error echoes.
const maxShown = 256

// readOpsAddr is OPS_LISTEN_ADDR as the ops listener binds it, which the
// health probe reads too.
func readOpsAddr(get func(string) string) string {
	return httpserver.ListenAddr(envx.Source{Get: get}.String(envOps), defaultOps)
}
