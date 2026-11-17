// Package main is forge-scout v4: a web UI over the items waiting on you
// across GitHub, GitLab, Gitea and Forgejo instances.
//
// main is the composition root; all logic lives in internal/*.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/go/health/v2/probe"
	"github.com/cplieger/go/slogx"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// probeTimeout is the health probe's budget, below the image's 5 s
// HEALTHCHECK --timeout so a hung endpoint fails with its reason written.
const probeTimeout = 4 * time.Second

// proc is what run reads from the process.
type proc struct {
	getenv func(string) string
	stderr io.Writer
	logger *slog.Logger
	// level is the logger's level, which LOG_LEVEL sets.
	level *slog.LevelVar
	args  []string
}

func main() {
	level := slogx.Setup(slogx.Options{Format: slogx.JSON, Output: os.Stdout})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, &proc{args: os.Args[1:], getenv: os.Getenv, stderr: os.Stderr, logger: slog.Default(), level: level})
	stop()
	os.Exit(code)
}

// run dispatches the command in p.args and returns the process exit code:
// no command serves until ctx is done, health probes a running server, and
// anything else is a usage error, exit 2.
func run(ctx context.Context, p *proc) int {
	switch {
	case len(p.args) == 0:
		return serve(ctx, p)
	case len(p.args) == 1 && p.args[0] == "health":
		return probeHealth(p)
	}
	fmt.Fprintf(p.stderr, "forge-scout: unknown command %q; run it with no command to serve, or with health to probe a running server\n", strings.Join(p.args, " "))
	return 2
}

// probeHealth asks the ops listener's /healthz on loopback: 0 when it
// answers 2xx, 1 otherwise, the reason on stderr.
func probeHealth(p *proc) int {
	u, err := probe.LocalURL(readOpsAddr(p.getenv), "/healthz")
	if err != nil {
		fmt.Fprintf(p.stderr, "forge-scout health: %s: %v\n", envOps, err)
		return 1
	}
	return probe.Check(p.stderr, probeTimeout, u)
}
