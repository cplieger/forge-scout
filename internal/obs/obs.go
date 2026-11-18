// Package obs is forge-scout's /metrics page: every series it serves, under
// the forge_scout prefix. Read series are taken at each scrape from the
// functions New is given, and every label value comes from a fixed list in
// the code or from the process.
package obs

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/measure/v2"
)

// FileData is the store_open_failed file label of the product file.
const FileData = "data"

// Deps is what the read series read at each scrape. Every field is required.
type Deps struct {
	// StoreOpenFailed reports whether the product file is in its outage:
	// it could not be opened and has not been restored since.
	StoreOpenFailed func() bool
	// Version is the running build's version, the build_info label.
	Version string
}

// Obs holds the registry and the series recorded into it. Build it with New.
type Obs struct {
	reg          *measure.Registry
	httpRequests *measure.LabeledCounter
	httpDuration *measure.LabeledHistogram
}

// New registers every series of the page. It refuses a missing dependency,
// so a scrape never calls a nil function.
func New(d Deps) (*Obs, error) {
	switch {
	case d.Version == "":
		return nil, errors.New("obs: no version")
	case d.StoreOpenFailed == nil:
		return nil, errors.New("obs: no StoreOpenFailed function")
	}
	httpLabels := []string{"method", "route", "code"}
	o := &Obs{
		reg: measure.NewRegistry("forge_scout"),
		httpRequests: measure.NewLabeledCounter("http_requests_total",
			"HTTP requests, by method, route template and status code.", measure.UnitCount, httpLabels),
		httpDuration: measure.NewLabeledHistogram("http_request_duration_seconds",
			"HTTP request latency, by method, route template and status code.", measure.UnitSeconds, httpLabels),
	}
	buildInfo := measure.NewInfoDesc("build_info", "Always 1, labelled with the running version.", []string{"version"})
	openFailed := measure.NewGaugeDesc("store_open_failed",
		"1 while the file could not be opened at start and has not been restored since, by file.",
		measure.UnitNone, []string{"file"})
	read := measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		e.Emit(buildInfo, 1, d.Version)
		e.Emit(openFailed, flag(d.StoreOpenFailed()), FileData)
	}, buildInfo, openFailed)
	for _, m := range []measure.Metric{o.httpRequests, o.httpDuration, read} {
		if err := o.reg.Register(m); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// Handler serves the page, with the Go runtime and process families.
func (o *Obs) Handler() http.Handler { return o.reg.Handler() }

// RecordHTTP records one completed request. It is the hook for
// httpserver.WithRecordRouteMetric, whose Method and Path are bounded: one of
// the standard methods or "other", and a registered route template or
// "unmatched".
func (o *Obs) RecordHTTP(m httpserver.RequestMetric) {
	labels := []string{m.Method, m.Path, strconv.Itoa(m.Status)}
	measure.RecordHTTP(o.httpRequests, nil, m.Latency, labels...)
	o.httpDuration.Observe(m.Latency.Seconds(), labels...)
}

func flag(on bool) float64 {
	if on {
		return 1
	}
	return 0
}
