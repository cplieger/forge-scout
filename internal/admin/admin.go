// Package admin is the admin socket: HTTP over a unix socket in the data
// directory that only processes of forge-scout's own user can reach.
package admin

import (
	"log/slog"
	"net"
	"net/http"
	"path/filepath"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/ownersock"
)

// Path is the socket's path under dataDir, in a directory of its own as
// ownersock requires.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "run", "admin.sock")
}

// Listen binds the socket at Path(dataDir), creating its directory at
// 0700. A socket another live process answers on refuses the start.
// Closing the listener removes the socket.
func Listen(dataDir string, logger *slog.Logger) (net.Listener, error) {
	return ownersock.Listen(ownersock.Config{Path: Path(dataDir), Logger: logger})
}

// Server is the socket's server. It answers every request not_found until
// the socket has verbs.
func Server(logger *slog.Logger) *http.Server {
	srv := httpserver.New(httpserver.Chain(httpserver.NotFound(nil),
		httpserver.Logging(httpserver.WithLogger(logger)),
		httpserver.Recoverer(httpserver.WithRecoverLogger(logger)),
	), httpserver.WithSlogErrorLog(slog.LevelWarn))
	srv.ConnContext = ownersock.ConnContext
	return srv
}
