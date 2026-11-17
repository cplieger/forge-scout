// Package main is forge-scout v4: a web UI over the items waiting on you
// across GitHub, GitLab, Gitea and Forgejo instances.
//
// main is the composition root; all logic lives in internal/*.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cplieger/go/slogx"
)

func main() {
	slogx.Setup(slogx.Options{Format: slogx.JSON, Output: os.Stdout})
	if len(os.Args) > 1 {
		slog.Error("unknown subcommand", "arg", os.Args[1], "valid", "no argument for the daemon")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx)
	stop()
	os.Exit(code)
}

// run serves until ctx is done and returns the process exit code.
func run(ctx context.Context) int {
	slog.Info("forge-scout started")
	<-ctx.Done()
	return 0
}
