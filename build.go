package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/cplieger/forge-scout/internal/admin"
	"github.com/cplieger/forge-scout/internal/obs"
	"github.com/cplieger/forge-scout/internal/server"
	"github.com/cplieger/forge-scout/internal/store"
	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/httpserver/v2"
)

// serve runs the daemon until ctx is done: 0 after a clean stop, 1 when
// the start or a listener fails. A data file that does not open, its lock
// aside, is no start failure: both listeners serve and /healthz names it.
func serve(ctx context.Context, p *proc) int {
	logger := p.logger
	e, err := readEnv(p.getenv)
	if err != nil {
		logger.Error("forge-scout cannot start", "error", err)
		return 1
	}
	p.level.Set(e.level)
	if e.levelUnknown {
		logger.Warn("LOG_LEVEL names no level, logging at info", "valid", "debug, info, warn, error")
	}
	if err = httpserver.CheckListenAddrs(
		httpserver.ListenSetting{Name: string(envListen), Addr: e.listen},
		httpserver.ListenSetting{Name: string(envOps), Addr: e.ops},
	); err != nil {
		logger.Error("forge-scout cannot start", "error", err)
		return 1
	}
	hosts := server.Hosts(e.hosts)
	policy, err := server.HostPolicy(hosts, logger)
	if err != nil {
		logger.Error("forge-scout cannot start", "setting", string(envHosts), "error", err)
		return 1
	}
	schema, err := store.NewSchema()
	if err != nil {
		logger.Error("forge-scout cannot start", "error", err)
		return 1
	}
	st, err := store.Open(e.dataDir, schema, logger)
	if err != nil {
		logger.Error("forge-scout cannot start", "error", err)
		return 1
	}
	closeStore := sync.OnceFunc(func() {
		if cerr := st.Close(); cerr != nil {
			logger.Error("data file close failed", "file", st.Path(), "error", cerr)
		}
	})
	defer closeStore()
	a, err := assemble(ctx, &inputs{env: &e, store: st, hosts: hosts, policy: policy, logger: logger})
	if err != nil {
		logger.Error("forge-scout cannot start", "error", err)
		return 1
	}
	a.status.Set(true)
	class, failed := st.Failure()
	state := "open"
	if failed {
		state = class
	}
	logger.Info("forge-scout started", "version", version,
		"listen", a.product.Addr().String(), "ops", a.ops.Addr().String(),
		"file", st.Path(), "store", state)
	// The store closes after both servers drain, and before RunWithOps
	// stops the ops listener, so /healthz answers through the drain.
	left := atomic.Int32{}
	left.Store(2)
	done := func(context.Context) {
		if left.Add(-1) == 0 {
			closeStore()
		}
	}
	err = httpserver.RunWithOps(ctx, a.ops,
		httpserver.Served{Server: a.productSrv, Listener: a.product, Options: []httpserver.RunOption{
			httpserver.WithPreDrain(func(context.Context) { a.status.BeginDrain() }),
			httpserver.WithTeardown(done), httpserver.WithServeExit(done),
		}},
		httpserver.Served{Server: a.adminSrv, Listener: a.admin, Options: []httpserver.RunOption{
			httpserver.WithTeardown(done), httpserver.WithServeExit(done),
		}},
	)
	if err != nil {
		logger.Error("forge-scout stopped on a listener failure", "error", err)
		return 1
	}
	return 0
}

// inputs is what assemble wires: the environment, the open store and the
// host check both listeners apply.
type inputs struct {
	env    *env
	store  *store.Store
	policy *httpserver.HostPolicy
	logger *slog.Logger
	// hosts is the list policy was built from, which the ops listener
	// parses again.
	hosts []string
}

// assembly is every bound listener and its server, in the start order.
type assembly struct {
	status     *health.Status
	admin      net.Listener
	adminSrv   *http.Server
	product    net.Listener
	productSrv *http.Server
	ops        *httpserver.Ops
}

// assemble builds the series, the health checks and the servers, and
// binds the admin socket, then the product listener, then the ops
// listener. On an error it closes every listener it bound.
func assemble(ctx context.Context, in *inputs) (*assembly, error) {
	st, logger := in.store, in.logger
	storeFailed := func() bool { _, failed := st.Failure(); return failed }
	o, err := obs.New(obs.Deps{StoreOpenFailed: storeFailed, Version: version})
	if err != nil {
		return nil, err
	}
	a := &assembly{status: health.NewStatus(health.WithLogger(logger)), adminSrv: admin.Server(logger)}
	handler, err := server.Handler(&server.Config{UI: httpserver.NotFound(nil), Hosts: in.policy, Record: o.RecordHTTP, Logger: logger})
	if err != nil {
		return nil, err
	}
	a.productSrv = httpserver.New(handler, httpserver.WithSlogErrorLog(slog.LevelWarn))
	if a.admin, err = admin.Listen(in.env.dataDir, logger); err != nil {
		return nil, err
	}
	if a.product, err = httpserver.Listen(ctx, httpserver.ListenSetting{Name: string(envListen), Addr: in.env.listen}); err != nil {
		_ = a.admin.Close()
		return nil, err
	}
	a.ops, err = httpserver.ListenOps(ctx, httpserver.OpsConfig{
		Metrics: o.Handler(),
		Healthz: health.CheckHandler(healthChecks(a.status, st)...),
		Logger:  logger,
		Setting: string(envOps),
		Addr:    in.env.ops,
		Hosts:   in.hosts,
	})
	if err != nil {
		_ = a.product.Close()
		_ = a.admin.Close()
		return nil, err
	}
	return a, nil
}

// healthChecks are /healthz's checks. A reason carries at most a class
// word, never error text: /healthz answers without sign-in.
func healthChecks(status *health.Status, st *store.Store) []health.Check {
	// The class is the start's, exact while nothing swaps the file: a
	// Swap that can change it must rebuild this reason.
	opened := "the data file " + store.FileName + " cannot be opened"
	if class, failed := st.Failure(); failed {
		opened += ": " + class
	}
	return []health.Check{
		{Signal: status, Reason: "forge-scout is starting or shutting down"},
		{Signal: health.SignalFunc(func() bool { _, failed := st.Failure(); return !failed }), Reason: opened},
	}
}
