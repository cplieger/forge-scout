package obs_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/obs"
	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/measure/v2"
)

// page is an Obs whose store series read the two flags.
type page struct {
	obs                     *obs.Obs
	openFailed, writeFailed *atomic.Bool
}

func newPage(t *testing.T) page {
	t.Helper()
	p := page{openFailed: new(atomic.Bool), writeFailed: new(atomic.Bool)}
	o, err := obs.New(obs.Deps{
		Version:           "4.0.0-test",
		StoreOpenFailed:   p.openFailed.Load,
		StoreWriteFailing: p.writeFailed.Load,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.obs = o
	return p
}

// family gathers the page and returns the family named name.
func (p page) family(t *testing.T, name string) measure.Family {
	t.Helper()
	fams, err := p.obs.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	f, ok := measure.Find(fams, name)
	if !ok {
		t.Fatalf("no family %s", name)
	}
	return f
}

// value is the value of the series of family name with labelValues.
func (p page) value(t *testing.T, name string, labelValues ...string) float64 {
	t.Helper()
	f := p.family(t, name)
	s, ok := f.Sample(labelValues...)
	if !ok {
		t.Fatalf("%s has no series %q: %+v", name, labelValues, f.Samples)
	}
	return s.Value
}

func TestNew_refusesMissingDependency(t *testing.T) {
	ok := func() bool { return false }
	for _, tc := range []struct {
		name string
		deps obs.Deps
	}{
		{"no version", obs.Deps{StoreOpenFailed: ok, StoreWriteFailing: ok}},
		{"no open function", obs.Deps{Version: "v", StoreWriteFailing: ok}},
		{"no write function", obs.Deps{Version: "v", StoreOpenFailed: ok}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := obs.New(tc.deps)
			if err == nil || o != nil {
				t.Errorf("New = %v, %v; want an error", o, err)
			}
		})
	}
}

func TestBuildInfo(t *testing.T) {
	p := newPage(t)
	f := p.family(t, "forge_scout_build_info")
	if f.Type != measure.TypeGauge {
		t.Errorf("type = %s, want gauge", f.Type)
	}
	if len(f.Samples) != 1 {
		t.Fatalf("samples = %+v, want one", f.Samples)
	}
	s := f.Samples[0]
	if len(s.Labels) != 1 || s.Labels[0] != (measure.Label{Name: "version", Value: "4.0.0-test"}) {
		t.Errorf("labels = %+v, want version 4.0.0-test", s.Labels)
	}
	if s.Value != 1 {
		t.Errorf("value = %v, want 1", s.Value)
	}
}

func TestStoreSeries_readAtScrape(t *testing.T) {
	p := newPage(t)
	for _, tc := range []struct {
		name                     string
		openFailed, writeFailing bool
	}{
		{"healthy", false, false},
		{"open failed", true, false},
		{"write failing", false, true},
		{"both", true, true},
		{"restored", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.openFailed.Store(tc.openFailed)
			p.writeFailed.Store(tc.writeFailing)
			if got, want := p.value(t, "forge_scout_store_open_failed", "data"), flag(tc.openFailed); got != want {
				t.Errorf("store_open_failed{file=data} = %v, want %v", got, want)
			}
			if got, want := p.value(t, "forge_scout_store_write_failing"), flag(tc.writeFailing); got != want {
				t.Errorf("store_write_failing = %v, want %v", got, want)
			}
		})
	}
}

func TestStoreOpenFailed_dataFileOnly(t *testing.T) {
	f := newPage(t).family(t, "forge_scout_store_open_failed")
	if len(f.Samples) != 1 {
		t.Fatalf("samples = %+v, want the data file alone", f.Samples)
	}
	if got := f.Samples[0].Labels; len(got) != 1 || got[0] != (measure.Label{Name: "file", Value: obs.FileData}) {
		t.Errorf("labels = %+v, want file=data", got)
	}
}

func TestRecordHTTP(t *testing.T) {
	p := newPage(t)
	p.obs.RecordHTTP(httpserver.RequestMetric{Method: "GET", Path: "GET /healthz", Status: 200, Latency: 200 * time.Millisecond})
	p.obs.RecordHTTP(httpserver.RequestMetric{Method: "GET", Path: "GET /healthz", Status: 200, Latency: 2 * time.Second})
	p.obs.RecordHTTP(httpserver.RequestMetric{Method: "POST", Path: "unmatched", Status: 404, Latency: time.Millisecond})

	t.Run("counter", func(t *testing.T) {
		if got := p.value(t, "forge_scout_http_requests_total", "GET", "GET /healthz", "200"); got != 2 {
			t.Errorf("GET /healthz 200 = %v, want 2", got)
		}
		if got := p.value(t, "forge_scout_http_requests_total", "POST", "unmatched", "404"); got != 1 {
			t.Errorf("POST unmatched 404 = %v, want 1", got)
		}
	})
	t.Run("histogram", func(t *testing.T) {
		f := p.family(t, "forge_scout_http_request_duration_seconds")
		s, ok := f.Sample("GET", "GET /healthz", "200")
		if !ok || s.Histogram == nil {
			t.Fatalf("no GET /healthz 200 histogram: %+v", f.Samples)
		}
		if s.Histogram.Count != 2 || s.Histogram.Sum != 2.2 {
			t.Errorf("count, sum = %d, %v; want 2, 2.2", s.Histogram.Count, s.Histogram.Sum)
		}
	})
}

func TestHandler_servesThePage(t *testing.T) {
	p := newPage(t)
	p.openFailed.Store(true)
	p.obs.RecordHTTP(httpserver.RequestMetric{Method: "GET", Path: "GET /metrics", Status: 200, Latency: time.Millisecond})
	rec := httptest.NewRecorder()
	p.obs.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, line string }{
		{"build info", `forge_scout_build_info{version="4.0.0-test"} 1`},
		{"open failed", `forge_scout_store_open_failed{file="data"} 1`},
		{"write failing", `forge_scout_store_write_failing 0`},
		{"requests", `forge_scout_http_requests_total{code="200",method="GET",route="GET /metrics"} 1`},
		{"duration", `forge_scout_http_request_duration_seconds_count{code="200",method="GET",route="GET /metrics"} 1`},
		{"runtime", `go_goroutines `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(string(body), "\n"+tc.line) {
				t.Errorf("page lacks %q", tc.line)
			}
		})
	}
}

func flag(on bool) float64 {
	if on {
		return 1
	}
	return 0
}
