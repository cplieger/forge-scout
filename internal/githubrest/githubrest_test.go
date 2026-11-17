package githubrest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forge-scout/internal/ghquota"
	"github.com/cplieger/go/httpclient"
	"github.com/cplieger/go/ssrf"
)

const testVersion = "2026-03-10"

// armed is the quota meter of a connection that answered as GitHub.
func armed() *ghquota.Meter {
	m := ghquota.New(nil)
	m.Arm()
	return m
}

// newClient serves h to a client making one attempt per request.
func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	return newClientWith(t, h, httpclient.WithMaxAttempts(1))
}

func newClientWith(t *testing.T, h http.HandlerFunc, retry ...httpclient.Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(httpclient.New(5*time.Second), srv.URL, "test-token", testVersion, armed(), slog.New(slog.DiscardHandler), retry...)
	return c
}

var (
	repoKey = forge.RepoKey{Connection: "c1", Repo: "v1.R_1"}
	repo    = forge.Repo{Key: repoKey, Path: "o/r"}
)

// kindOf is the kind of the *forge.Error err wraps, empty when none.
func kindOf(err error) forge.ErrorKind {
	if fe, ok := errors.AsType[*forge.Error](err); ok {
		return fe.Kind
	}
	return ""
}

// alertsPage is a code-scanning page in the shape GitHub documents for
// GET /repos/{owner}/{repo}/code-scanning/alerts
// (https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository).
func alertsPage(from, n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"number":%d,"created_at":"2026-10-01T00:00:00Z","html_url":"https://github.com/o/r/security/code-scanning/%d",`+
			`"rule":{"id":"go/rule","description":"desc","severity":"warning","security_severity_level":"high"},"tool":{"name":"CodeQL"}}`, from+i, from+i)
	}
	b.WriteString("]")
	return b.String()
}

func TestCodeScanningAlerts_reads_and_maps_alerts(t *testing.T) {
	var gotAuth, gotAccept, gotVersion, gotQuery, gotPath string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept, gotVersion = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("X-GitHub-Api-Version")
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		io.WriteString(w, alertsPage(1, 2))
	})
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || got.End != forge.EndWhole || len(got.Rows) != 2 {
		t.Fatalf("CodeScanningAlerts = %d alerts ending %v, %v, want 2, whole", len(got.Rows), got.End, err)
	}
	want := forge.Alert{
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Repo: repoKey, Rule: "go/rule",
		SecuritySeverity: "high", ProblemSeverity: "warning", Tool: "CodeQL", URL: "https://github.com/o/r/security/code-scanning/1", Number: 1,
	}
	if got.Rows[0] != want {
		t.Errorf("CodeScanningAlerts row = %+v, want %+v", got.Rows[0], want)
	}
	if gotAuth != "Bearer test-token" || gotAccept != "application/vnd.github+json" || gotVersion != testVersion {
		t.Errorf("headers = auth %q accept %q version %q, want the bearer token, the GitHub media type, %s", gotAuth, gotAccept, gotVersion, testVersion)
	}
	if gotPath != "/repos/o/r/code-scanning/alerts" || !strings.Contains(gotQuery, "state=open") || !strings.Contains(gotQuery, "per_page=100") {
		t.Errorf("request = %s?%s, want the open alerts at 100 a page", gotPath, gotQuery)
	}
}

func TestDecodeAlerts_maps_both_severities_and_names_an_unknown_one(t *testing.T) {
	// GitHub's alert rule summary: severity is none, note, warning or error,
	// security_severity_level low, medium, high or critical, each nullable
	// (https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository).
	cases := []struct {
		name, rule, security, problem string
	}{
		{"no security severity, as a linter sets", `"severity":"error","security_severity_level":null`, "", "error"},
		{"both set", `"severity":"warning","security_severity_level":"critical"`, "critical", "warning"},
		{"neither set", `"severity":null,"security_severity_level":null`, "", ""},
		{"neither sent", ``, "", ""},
		{"none is a value", `"severity":"none"`, "", "none"},
		{"note", `"severity":"note"`, "", "note"},
		{"case folded", `"severity":"Error","security_severity_level":"HIGH"`, "high", "error"},
		{"an unknown problem severity", `"severity":"fatal"`, "", "unknown"},
		{"an unknown security severity", `"security_severity_level":"informational"`, "unknown", ""},
		{"a long unknown value", `"severity":"` + strings.Repeat("x", 4096) + `"`, "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			rule := `"id":"r"`
			if tc.rule != "" {
				rule += "," + tc.rule
			}
			rows, err := decodeAlerts(repoKey, []byte(`[{"number":1,"rule":{`+rule+`},"tool":{"name":"deadset"}}]`))
			if err != nil || len(rows) != 1 {
				t.Fatalf("decodeAlerts(%s) = %v, %v, want one alert", tc.rule, rows, err)
			}
			if rows[0].SecuritySeverity != tc.security || rows[0].ProblemSeverity != tc.problem {
				t.Errorf("decodeAlerts(%.80s) severities = %q, %q, want %q, %q", tc.rule, rows[0].SecuritySeverity, rows[0].ProblemSeverity, tc.security, tc.problem)
			}
		})
	}
}

func TestDecodeAlerts_places_the_most_recent_instance(t *testing.T) {
	for _, tc := range []struct {
		name, instance, path string
		line                 int
	}{
		{name: "with_location", instance: `,"most_recent_instance":{"ref":"refs/heads/main","location":{"path":"cmd/main.go","start_line":42,"end_line":44}}`, path: "cmd/main.go", line: 42},
		{name: "without_instance"},
		{name: "instance_without_location", instance: `,"most_recent_instance":{"ref":"refs/heads/main"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := decodeAlerts(repoKey, []byte(`[{"number":1,"rule":{"id":"r"}`+tc.instance+`}]`))
			if err != nil || len(rows) != 1 {
				t.Fatalf("decodeAlerts = %v, %v, want one alert", rows, err)
			}
			if rows[0].Path != tc.path || rows[0].StartLine != tc.line {
				t.Errorf("decodeAlerts location = %q line %d, want %q line %d", rows[0].Path, rows[0].StartLine, tc.path, tc.line)
			}
		})
	}
}

func TestCodeScanningAlerts_rule_falls_back_to_the_description(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"number":1,"rule":{"description":"only a description"},"tool":{"name":"Trivy"}}]`)
	})
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Rule != "only a description" {
		t.Errorf("CodeScanningAlerts = %+v, %v, want the description as the rule", got, err)
	}
}

func TestCodeScanningAlerts_pages_until_a_short_page(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, alertsPage(101, 3))
			return
		}
		io.WriteString(w, alertsPage(1, 100))
	})
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || got.End != forge.EndWhole || len(got.Rows) != 103 || calls.Load() != 2 {
		t.Errorf("CodeScanningAlerts over a full and a short page = %d alerts ending %v calls %d, %v, want 103, whole, 2", len(got.Rows), got.End, calls.Load(), err)
	}
}

// alertsServer answers total open alerts a page at a time, each response
// reporting one request fewer left in the REST pool.
func alertsServer(total int, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-n))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		from := (page-1)*perPage + 1
		io.WriteString(w, alertsPage(from, max(0, min(perPage, total-from+1))))
	}
}

func TestCodeScanningAlerts_a_repository_past_500_alerts_reads_whole(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, alertsServer(1166, &calls))
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || got.End != forge.EndWhole || len(got.Rows) != 1166 {
		t.Fatalf("CodeScanningAlerts over 1,166 alerts = %d alerts ending %v, %v, want all 1,166 read whole", len(got.Rows), got.End, err)
	}
	if calls.Load() != 12 || got.Rows[1165].Number != 1166 {
		t.Errorf("CodeScanningAlerts over 1,166 alerts = %d requests, last alert %d, want 12 pages ending at alert 1166", calls.Load(), got.Rows[1165].Number)
	}
	if b := c.Budget(); b.Remaining != 5000-12 {
		t.Errorf("the quota meter after 12 pages = %d remaining, want %d: every page is charged", b.Remaining, 5000-12)
	}
}

func TestCodeScanningAlerts_a_repository_past_the_page_ceiling_reads_partial(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, alertsServer(maxAlertPages*perPage+1, &calls))
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || got.End != forge.EndCut || got.Cut != forge.CutCeiling || len(got.Rows) != maxAlertPages*perPage || int(calls.Load()) != maxAlertPages {
		t.Errorf("CodeScanningAlerts past the ceiling = %d alerts ending %v (%v) in %d requests, %v, want %d, cut at the ceiling, %d",
			len(got.Rows), got.End, got.Cut, calls.Load(), err, maxAlertPages*perPage, maxAlertPages)
	}
}

func TestCodeScanningAlerts_alerts_past_the_byte_budget_fail_the_read(t *testing.T) {
	var calls atomic.Int32
	rule := strings.Repeat("r", 20_000)
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		io.WriteString(w, strings.ReplaceAll(alertsPage((page-1)*perPage+1, perPage), `"go/rule"`, `"`+rule+`"`))
	})
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if kindOf(err) != forge.KindUpstream || got.End != "" || len(got.Rows) != 0 {
		t.Errorf("CodeScanningAlerts over pages of %d-byte rules = %d alerts ending %v, %v, want an upstream failure with no rows", len(rule), len(got.Rows), got.End, err)
	}
	if want := maxAlertBytes/(perPage*len(rule)) + 1; int(calls.Load()) != want {
		t.Errorf("CodeScanningAlerts over the byte budget sent %d requests, want %d: the page crossing it ends the read", calls.Load(), want)
	}
}

// read is one of the two routes, as its row count, End and error.
type read func(c *Client, ctx context.Context) (rows int, end forge.End, err error)

var routes = map[string]read{
	"code_scanning": func(c *Client, ctx context.Context) (int, forge.End, error) {
		got, err := c.CodeScanningAlerts(ctx, &repo)
		return len(got.Rows), got.End, err
	},
	"workflows": func(c *Client, ctx context.Context) (int, forge.End, error) {
		got, err := c.Workflows(ctx, &repo)
		return len(got.Rows), got.End, err
	},
}

// pageOf is the route's page holding n rows, n of them over a page's
// size for an overfull one.
func pageOf(route string, n int) string {
	if route == "workflows" {
		return workflowsPage(slices.Repeat([]string{"active"}, n)...)
	}
	return alertsPage(1, n)
}

// TestRoutes_every_answer_reads_as_its_route_table_says holds each route to
// what GitHub documents its answers mean: only code scanning's first-page
// 404 is a repository with no analyses, and no other answer is a clean
// empty read.
func TestRoutes_every_answer_reads_as_its_route_table_says(t *testing.T) {
	type answer struct {
		page1, page2 func(w http.ResponseWriter, route string)
	}
	body := func(text string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, _ string) { io.WriteString(w, text) }
	}
	rows := func(n int) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, route string) { io.WriteString(w, pageOf(route, n)) }
	}
	status := func(code int, header ...string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, _ string) {
			for i := 0; i+1 < len(header); i += 2 {
				w.Header().Set(header[i], header[i+1])
			}
			w.WriteHeader(code)
		}
	}
	empty := func(w http.ResponseWriter, route string) {
		if route == "workflows" {
			io.WriteString(w, workflowsPage())
			return
		}
		io.WriteString(w, "[]")
	}
	tests := []struct {
		answer answer
		want   map[string]forge.End
		err    forge.ErrorKind
		name   string
	}{
		{name: "empty", answer: answer{page1: empty}, want: map[string]forge.End{"code_scanning": forge.EndWhole, "workflows": forge.EndWhole}},
		{name: "null", answer: answer{page1: body("null")}, err: forge.KindUpstream},
		{name: "overfull", answer: answer{page1: rows(perPage + 1)}, err: forge.KindUpstream},
		{name: "not_found_first_page", answer: answer{page1: status(http.StatusNotFound)}, want: map[string]forge.End{"code_scanning": forge.EndNone}, err: forge.KindNotFound},
		{name: "not_found_second_page", answer: answer{page1: rows(perPage), page2: status(http.StatusNotFound)}, err: forge.KindNotFound},
		{
			name: "throttled", answer: answer{page1: status(http.StatusForbidden, "X-RateLimit-Remaining", "4000", "Retry-After", "60")},
			err: forge.KindRateLimited,
		},
		{
			name: "forbidden", answer: answer{page1: status(http.StatusForbidden, "X-RateLimit-Remaining", "4000")},
			err: forge.KindForbidden,
		},
		{name: "gone", answer: answer{page1: status(http.StatusGone)}, err: forge.KindUpstream},
		{name: "server_error", answer: answer{page1: status(http.StatusBadGateway)}, err: forge.KindTransient},
	}
	for _, tc := range tests {
		for route, read := range routes {
			t.Run(tc.name+"_"+route, func(t *testing.T) {
				c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("page") != "1" {
						if tc.answer.page2 != nil {
							tc.answer.page2(w, route)
							return
						}
						empty(w, route)
						return
					}
					tc.answer.page1(w, route)
				})
				n, end, err := read(c, t.Context())
				want, ok := tc.want[route]
				switch {
				case ok && (err != nil || end != want):
					t.Errorf("%s answering %s = %d rows ending %v, %v, want ending %v", route, tc.name, n, end, err, want)
				case !ok && (err == nil || end != ""):
					t.Errorf("%s answering %s = %d rows ending %v, %v, want a failed read, never an empty one", route, tc.name, n, end, err)
				case !ok && kindOf(err) != tc.err:
					t.Errorf("%s answering %s = %v, want a forge error of kind %s", route, tc.name, err, tc.err)
				}
			})
		}
	}
}

// statusHandler answers every request with status and, when set, GitHub's
// X-RateLimit-Remaining and Retry-After headers. A primary limit sets
// remaining to 0; a secondary one may set Retry-After, or neither header
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
func statusHandler(status int, remaining, retryAfter string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if remaining != "" {
			w.Header().Set("X-RateLimit-Remaining", remaining)
			w.Header().Set("X-RateLimit-Reset", "1791374400")
		}
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
	}
}

func TestCodeScanningAlerts_status_mapping(t *testing.T) {
	tests := []struct {
		name       string
		remaining  string
		retryAfter string
		want       forge.ErrorKind
		wantWait   time.Duration
		status     int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: forge.KindUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, want: forge.KindForbidden},
		{name: "forbidden_with_budget", status: http.StatusForbidden, remaining: "4000", want: forge.KindForbidden},
		{name: "forbidden_budget_spent", status: http.StatusForbidden, remaining: "0", want: forge.KindRateLimited},
		{name: "secondary_limit", status: http.StatusForbidden, remaining: "4000", retryAfter: "60", want: forge.KindRateLimited, wantWait: time.Minute},
		{name: "too_many", status: http.StatusTooManyRequests, retryAfter: "1", want: forge.KindRateLimited, wantWait: time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, statusHandler(tc.status, tc.remaining, tc.retryAfter))
			_, err := c.CodeScanningAlerts(t.Context(), &repo)
			fe, ok := errors.AsType[*forge.Error](err)
			if !ok || fe.Kind != tc.want || fe.Status != tc.status || fe.RetryAfter != tc.wantWait {
				t.Errorf("CodeScanningAlerts on %d remaining %q retry-after %q = %#v, want kind %s, status %d, retry after %v",
					tc.status, tc.remaining, tc.retryAfter, err, tc.want, tc.status, tc.wantWait)
			}
		})
	}
}

func TestReads_a_forbidden_answer_names_the_permissions_GitHub_accepts(t *testing.T) {
	for _, tc := range []struct {
		name, header string
	}{
		{name: "named", header: "security_events=read"},
		{name: "not_sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("X-Accepted-GitHub-Permissions", tc.header)
				}
				w.WriteHeader(http.StatusForbidden)
			})
			_, err := c.CodeScanningAlerts(t.Context(), &repo)
			if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindForbidden || fe.Needs != tc.header {
				t.Errorf("CodeScanningAlerts on a 403 = %#v, want forbidden needing %q", err, tc.header)
			}
		})
	}
}

func TestReads_a_transport_failure_is_transient_and_a_cancelled_read_is_the_context(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := New(httpclient.New(5*time.Second), base, "test-token", testVersion, armed(), slog.New(slog.DiscardHandler), httpclient.WithMaxAttempts(1))
	if _, err := c.Workflows(t.Context(), &repo); kindOf(err) != forge.KindTransient {
		t.Errorf("Workflows against a closed server = %v, want a transient forge error", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Workflows(ctx, &repo); !errors.Is(err, context.Canceled) || kindOf(err) != "" {
		t.Errorf("Workflows on a cancelled context = %v, want context.Canceled and no forge error", err)
	}
}

func TestCodeScanningAlerts_refuses_unsafe_paths(t *testing.T) {
	c := newClient(t, func(http.ResponseWriter, *http.Request) { t.Error("an unsafe path reached the server") })
	for _, p := range []string{"o", "o/r/x", "o/..", "o/r?x", "../r", "o r/x"} {
		if _, err := c.CodeScanningAlerts(t.Context(), &forge.Repo{Key: repoKey, Path: p}); kindOf(err) != forge.KindInvalid {
			t.Errorf("CodeScanningAlerts(%q) = %v, want an invalid refusal", p, err)
		}
	}
}

// workflowsPage is a workflows page in the shape GitHub documents for
// GET /repos/{owner}/{repo}/actions/workflows, whose state enum is active,
// deleted, disabled_fork, disabled_inactivity, disabled_manually
// (https://docs.github.com/en/rest/actions/workflows#list-repository-workflows).
func workflowsPage(states ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"total_count":%d,"workflows":[`, len(states))
	for i, s := range states {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"wf %d","path":".github/workflows/w%d.yml","state":%q,"html_url":"https://github.com/o/r/blob/main/.github/workflows/w%d.yml"}`, i, i, i, s, i)
	}
	b.WriteString("]}")
	return b.String()
}

func TestWorkflows_keeps_every_definition_in_its_state(t *testing.T) {
	var path string
	states := []string{"active", "disabled_inactivity", "deleted", "disabled_manually", "disabled_fork"}
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		io.WriteString(w, workflowsPage(states...))
	})
	got, err := c.Workflows(t.Context(), &repo)
	if err != nil || got.End != forge.EndWhole || len(got.Rows) != len(states) {
		t.Fatalf("Workflows = %+v, %v, want all %d definitions, whole", got, err, len(states))
	}
	for i, s := range states {
		if w := got.Rows[i]; string(w.State) != s || w.Name != fmt.Sprintf("wf %d", i) || w.Repo != repoKey {
			t.Errorf("Workflows row %d = %+v, want wf %d of o/r in state %s", i, w, i, s)
		}
	}
	if got.Rows[1].Path != ".github/workflows/w1.yml" {
		t.Errorf("Workflows row 1 path = %q, want .github/workflows/w1.yml", got.Rows[1].Path)
	}
	if path != "/repos/o/r/actions/workflows" {
		t.Errorf("Workflows path = %s, want /repos/o/r/actions/workflows", path)
	}
}

func TestWorkflows_an_unknown_state_fails_the_read(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, workflowsPage("disabled_manually", "disabled_repository\u202e"+strings.Repeat("x", 100)))
	})
	got, err := c.Workflows(t.Context(), &repo)
	if err == nil {
		t.Fatalf("Workflows(unknown state) = %+v, nil error, want a failed read", got)
	}
	if msg := err.Error(); !strings.Contains(msg, `"disabled_repository\u202e`) || strings.Contains(msg, strings.Repeat("x", 30)) {
		t.Errorf("Workflows(unknown state) error = %q, want the state quoted with its control escaped and cut short", msg)
	}
}

func TestWorkflows_pages_only_after_a_full_page_and_bounds_at_three(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		io.WriteString(w, pageOf("workflows", perPage))
	})
	got, err := c.Workflows(t.Context(), &repo)
	if err != nil || got.End != forge.EndCut || got.Cut != forge.CutCeiling || len(got.Rows) != 300 || calls.Load() != 3 {
		t.Errorf("Workflows over full pages = %d ending %v calls %d, %v, want 300, cut, 3", len(got.Rows), got.End, calls.Load(), err)
	}
	calls.Store(0)
	short := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		io.WriteString(w, workflowsPage("active"))
	})
	if got, err := short.Workflows(t.Context(), &repo); err != nil || got.End != forge.EndWhole || calls.Load() != 1 {
		t.Errorf("Workflows over a short page = ending %v calls %d, %v, want 1 call, whole", got.End, calls.Load(), err)
	}
}

func TestReads_a_page_at_the_requested_size_is_read(t *testing.T) {
	for route, read := range routes {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "1" {
				io.WriteString(w, pageOf(route, perPage))
				return
			}
			io.WriteString(w, pageOf(route, 0))
		})
		if n, end, err := read(c, t.Context()); err != nil || n != perPage || end != forge.EndWhole {
			t.Errorf("%s over a page of exactly %d rows = %d rows ending %v, %v, want them read whole", route, perPage, n, end, err)
		}
	}
}

func TestAPIBase(t *testing.T) {
	tests := []struct{ url, api, want string }{
		{"https://github.com", "", "https://api.github.com"},
		{"https://GitHub.com/", "", "https://api.github.com"},
		{"https://www.github.com", "", "https://api.github.com"},
		{"https://api.github.com", "", "https://api.github.com"},
		{"http://github.com", "", "https://api.github.com"},
		{"https://ghe.example.test", "", "https://ghe.example.test/api/v3"},
		{"https://ghe.example.test", "https://api.ghe.example.test", "https://api.ghe.example.test"},
	}
	for _, tc := range tests {
		if got := APIBase(tc.url, tc.api); got != tc.want {
			t.Errorf("APIBase(%q, %q) = %q, want %q", tc.url, tc.api, got, tc.want)
		}
	}
}

func TestCodeScanningAlerts_retries_a_server_error(t *testing.T) {
	var calls atomic.Int32
	c := newClientWith(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, alertsPage(1, 1))
	}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond))
	got, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil || len(got.Rows) != 1 || calls.Load() != 2 {
		t.Errorf("CodeScanningAlerts after a 500 = %d alerts, %d calls, %v, want the retry to answer 1 alert", len(got.Rows), calls.Load(), err)
	}
}

// A status answer is retried exactly when httpclient's one rule says so: 408
// and a 5xx whose answer can change, never 501, 505 or 511, whose answer repeats.
func TestCodeScanningAlerts_retries_by_the_shared_status_rule(t *testing.T) {
	for _, tc := range []struct {
		status int
		calls  int32
	}{
		{http.StatusRequestTimeout, 2},
		{http.StatusInternalServerError, 2},
		{http.StatusBadGateway, 2},
		{http.StatusNotImplemented, 1},
		{http.StatusHTTPVersionNotSupported, 1},
		{http.StatusNetworkAuthenticationRequired, 1},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			c := newClientWith(t, func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(tc.status)
					return
				}
				io.WriteString(w, alertsPage(1, 1))
			}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond))
			_, _ = c.CodeScanningAlerts(t.Context(), &repo)
			if got := calls.Load(); got != tc.calls {
				t.Errorf("a %d answer drew %d calls, want %d", tc.status, got, tc.calls)
			}
		})
	}
}

// reporting answers with GitHub's rate-limit headers at remaining(n) for the
// n-th request, with the window renewing at reset, then serves h.
func reporting(remaining func(n int) int, reset time.Time, h http.HandlerFunc) http.HandlerFunc {
	var calls atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining(n)))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		h(w, r)
	}
}

func TestBudget_a_read_that_leaves_the_budget_at_the_reserve_holds_the_next(t *testing.T) {
	var calls atomic.Int32
	reset := time.Now().Add(time.Hour)
	c := newClient(t, reporting(func(int) int { return forge.ReadReserve }, reset, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, pageOf(map[bool]string{true: "workflows", false: "alerts"}[strings.HasSuffix(r.URL.Path, "/workflows")], 1))
	}))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); err != nil {
		t.Fatalf("CodeScanningAlerts = %v, want the read", err)
	}
	if _, err := c.Workflows(t.Context(), &repo); kindOf(err) != forge.KindDeferred {
		t.Errorf("Workflows at the reserve = %v, want a deferral", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests sent = %d, want 1: the response left the budget at the reserve", n)
	}
	if b := c.Budget(); b.Remaining != forge.ReadReserve || !b.Reset.Equal(reset.Truncate(time.Second).UTC()) {
		t.Errorf("Budget = %+v, want %d until the reported reset", b, forge.ReadReserve)
	}
}

func TestBudget_a_retried_read_charges_each_attempt_and_holds_the_next(t *testing.T) {
	var calls atomic.Int32
	reset := time.Now().Add(time.Hour)
	c := newClientWith(t, func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(forge.ReadReserve+2))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		case 2:
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, alertsPage(1, 1))
	}, httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond))
	for range 2 {
		if _, err := c.CodeScanningAlerts(t.Context(), &repo); err != nil {
			t.Fatalf("CodeScanningAlerts = %v, want the read", err)
		}
	}
	if b := c.Budget(); b.Remaining != forge.ReadReserve {
		t.Errorf("Budget after a read of two attempts with no rate-limit header = %+v, want %d: each attempt spends one", b, forge.ReadReserve)
	}
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); kindOf(err) != forge.KindDeferred {
		t.Errorf("CodeScanningAlerts at the reserve = %v, want a deferral", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("requests sent = %d, want 3: the read that set the budget, then the 502 and its retry", n)
	}
}

func TestBudget_pages_and_retries_stop_at_the_reserve(t *testing.T) {
	var calls atomic.Int32
	c := newClientWith(t, reporting(func(n int) int { return forge.ReadReserve + 3 - n }, time.Now().Add(time.Hour), func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, alertsPage(1, perPage))
	}), httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); kindOf(err) != forge.KindDeferred {
		t.Errorf("CodeScanningAlerts over a retry and full pages = %v, want a deferral at the reserve", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("requests sent = %d, want 3: the retry and two pages, the last reporting the reserve", n)
	}
}

func TestBudget_a_followed_redirect_is_admitted_and_charged(t *testing.T) {
	var calls atomic.Int32
	redirecting := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/moved" {
			http.Redirect(w, r, "/moved", http.StatusFound)
			return
		}
		io.WriteString(w, "[]")
	}
	c := newClient(t, reporting(func(int) int { return forge.ReadReserve }, time.Now().Add(time.Hour), redirecting))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); kindOf(err) != forge.KindDeferred || calls.Load() != 1 {
		t.Errorf("CodeScanningAlerts redirected by a response at the reserve = %v after %d requests, want a deferral after 1: the hop would spend below the reserve",
			err, calls.Load())
	}
	calls.Store(0)
	c = newClient(t, reporting(func(n int) int { return forge.ReadReserve + 5 - n }, time.Now().Add(time.Hour), redirecting))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); err != nil || calls.Load() != 2 {
		t.Fatalf("CodeScanningAlerts redirected with budget left = %v after %d requests, want the read after 2", err, calls.Load())
	}
	if b := c.Budget(); b.Remaining != forge.ReadReserve+3 {
		t.Errorf("Budget after a redirected read = %d, want %d: the hop's response is observed too", b.Remaining, forge.ReadReserve+3)
	}
}

func TestNew_leaves_the_callers_client_unchanged(t *testing.T) {
	hc := httpclient.New(5 * time.Second)
	New(hc, "https://api.example.test", "test-token", testVersion, armed(), slog.New(slog.DiscardHandler))
	if hc.Transport != nil {
		t.Errorf("New replaced the caller's transport with %T, want it untouched", hc.Transport)
	}
}

func TestBudget_unknown_until_a_response_reports_it(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, alertsPage(1, 1)) })
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); err != nil || c.Budget().Known() {
		t.Errorf("CodeScanningAlerts with no rate-limit headers = %v, budget %+v, want the read and the budget unknown", err, c.Budget())
	}
}

func TestBudget_a_reserve_whose_window_renewed_admits_the_next_read(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		remaining, reset := "4999", time.Now().Add(time.Hour)
		if calls.Add(1) == 1 {
			remaining, reset = strconv.Itoa(forge.ReadReserve), time.Now().Add(-time.Minute)
		}
		w.Header().Set("X-RateLimit-Remaining", remaining)
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		io.WriteString(w, alertsPage(1, 1))
	})
	for range 2 {
		if _, err := c.CodeScanningAlerts(t.Context(), &repo); err != nil {
			t.Fatalf("CodeScanningAlerts = %v, want the read: a reserve whose window renewed holds nothing", err)
		}
	}
	if b := c.Budget(); calls.Load() != 2 || b.Remaining != 4999 {
		t.Errorf("after two reads = %d requests, budget %d, want 2 and the 4999 the second response reported", calls.Load(), b.Remaining)
	}
}

func TestReads_a_403_with_no_throttle_header_holds_no_later_read(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "4000")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		io.WriteString(w, workflowsPage("active"))
	})
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); kindOf(err) != forge.KindForbidden {
		t.Fatalf("CodeScanningAlerts on 403 = %v, want forbidden", err)
	}
	if _, err := c.Workflows(t.Context(), &repo); err != nil || calls.Load() != 2 {
		t.Errorf("Workflows after a 403 = %v after %d requests, want the read after 2: a refusal answers that request alone", err, calls.Load())
	}
}

func TestCodeScanningAlerts_a_refused_cross_host_redirect_is_not_retried(t *testing.T) {
	var calls atomic.Int32
	c := newClientWith(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "https://elsewhere.example.test/alerts", http.StatusFound)
	}, httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); err == nil || calls.Load() != 1 {
		t.Errorf("CodeScanningAlerts redirected to another host = %v after %d requests, want an error after 1: the refusal cannot heal", err, calls.Load())
	}
}

func TestHTTPClient_a_redirect_to_another_port_on_the_host_gets_no_token(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		io.WriteString(w, "[]")
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	var ports []uint16
	for _, s := range []*httptest.Server{srv, other} {
		u, err := url.Parse(s.URL)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		n, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		ports = append(ports, uint16(n))
	}
	// Both ports are admitted so only the redirect policy can stop the hop,
	// as it must for a base on a custom port redirecting to 443.
	c := New(httpClient(srv.URL, true, true, ssrf.WithAllowedPorts(ports...)), srv.URL, "test-token", testVersion, armed(),
		slog.New(slog.DiscardHandler), httpclient.WithMaxAttempts(1))
	if _, err := c.CodeScanningAlerts(t.Context(), &repo); err == nil || leaked.Load() != 0 {
		t.Errorf("CodeScanningAlerts redirected to another port = %v with the token sent %d times, want an error and no token sent", err, leaked.Load())
	}
}

// RFC 4007 section 11.2 leaves zone-ID representation implementation-defined,
// so httpclient compares its bytes exactly and treats a case change as another
// origin.
func TestHTTPClient_a_redirect_to_the_host_under_another_zone_case_is_refused(t *testing.T) {
	const base = "https://[fe80::1%25eth0]"
	hc := httpClient(base, true, false)
	hop := func(target string) error {
		t.Helper()
		orig, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/repos/acme/app/code-scanning/alerts", http.NoBody)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		next, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		return hc.CheckRedirect(next, []*http.Request{orig})
	}
	err := hop("https://[fe80::1%25ETH0]/next")
	if refused, ok := errors.AsType[*httpclient.RedirectRefusedError](err); !ok || refused.Reason != httpclient.RedirectOtherOrigin {
		t.Errorf("CheckRedirect to zone ETH0 from zone eth0 = %v, want a refusal as another origin", err)
	}
	if err := hop("https://[fe80::1%25eth0]/next"); err != nil {
		t.Errorf("CheckRedirect to the same zone = %v, want it followed", err)
	}
}

// loopbackResolver answers every name with 127.0.0.1, as a rebound DNS
// answer would, and counts its lookups.
type loopbackResolver struct{ lookups atomic.Int32 }

func (r *loopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.lookups.Add(1)
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestHTTPClient_dials_a_private_address_only_when_the_connection_allows_it(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run("private_addresses_"+strconv.FormatBool(private), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				io.WriteString(w, "[]")
			}))
			t.Cleanup(srv.Close)
			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			base := "http://forge.example.test:" + u.Port()
			resolver := &loopbackResolver{}
			c := New(httpClient(base, private, true, ssrf.WithResolver(resolver)), base, "test-token", testVersion, armed(),
				slog.New(slog.DiscardHandler), httpclient.WithMaxAttempts(1))
			_, err = c.CodeScanningAlerts(t.Context(), &repo)
			if resolver.lookups.Load() == 0 {
				t.Fatalf("CodeScanningAlerts resolved %s 0 times, want the dial to go through the resolver", base)
			}
			if private && (err != nil || calls.Load() != 1) {
				t.Errorf("CodeScanningAlerts on loopback with private_addresses = %v after %d requests, want the read", err, calls.Load())
			}
			if !private && (err == nil || calls.Load() != 0) {
				t.Errorf("CodeScanningAlerts on loopback without private_addresses = %v after %d requests, want a refused dial and no request", err, calls.Load())
			}
		})
	}
}

func TestReads_a_rate_limit_holds_the_next_read_until_the_reset(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.CodeScanningAlerts(t.Context(), &repo)
	if kindOf(err) != forge.KindRateLimited {
		t.Errorf("CodeScanningAlerts on a spent budget = %v, want rate limited", err)
	}
	if b := c.Budget(); b.Remaining != 0 || !b.Reset.Equal(reset) {
		t.Errorf("Budget after the limit = %+v, want the REST pool's 0 until %s", b, reset)
	}
	_, err = c.Workflows(t.Context(), &repo)
	if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindDeferred || fe.RetryAfter <= 0 || fe.RetryAfter > time.Hour {
		t.Errorf("Workflows held at the reserve = %v, want a deferral waiting for the reset within the hour", err)
	}
}

// validating answers body with an ETag, and 304 to a request naming it, as
// GitHub answers a conditional request
// (https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate),
// recording the If-None-Match each request carried.
func validating(body string, asked *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("X-RateLimit-Remaining", "4000")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, body)
	}
}

// meteredClient serves h to a client making one attempt per request, every
// attempt on the wire counted by the returned counter.
func meteredClient(t *testing.T, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sent := new(atomic.Int32)
	hc := httpclient.New(5 * time.Second)
	hc.Transport = countingTransport{next: http.DefaultTransport, sent: sent}
	c := New(hc, srv.URL, "test-token", testVersion, armed(), slog.New(slog.DiscardHandler), httpclient.WithMaxAttempts(1))
	return c, sent
}

type countingTransport struct {
	next http.RoundTripper
	sent *atomic.Int32
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.sent.Add(1)
	return c.next.RoundTrip(r)
}

func TestReads_an_unchanged_page_is_revalidated_and_billed_nothing(t *testing.T) {
	for _, tc := range []struct {
		read func(c *Client) (int, error)
		name string
		body string
	}{
		{name: "code_scanning", body: alertsPage(1, 2), read: func(c *Client) (int, error) {
			l, err := c.CodeScanningAlerts(t.Context(), &repo)
			return len(l.Rows), err
		}},
		{name: "workflows", body: workflowsPage("active", "disabled_manually"), read: func(c *Client) (int, error) {
			l, err := c.Workflows(t.Context(), &repo)
			return len(l.Rows), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			c, spent := meteredClient(t, validating(tc.body, &asked))
			if n, err := tc.read(c); err != nil || n != 2 {
				t.Fatalf("Setup: first read = %d rows, %v, want 2", n, err)
			}
			before := spent.Load()
			n, err := tc.read(c)
			if err != nil || n != 2 {
				t.Errorf("second read = %d rows, %v, want the 2 rows of the held answer", n, err)
			}
			if !slices.Equal(asked, []string{"", `"v1"`}) {
				t.Errorf("If-None-Match sent = %q, want none on the first read and the held ETag on the second", asked)
			}
			if got := spent.Load() - before; got != 1 {
				t.Errorf("attempts of the second read = %d, want 1 conditional request on the wire", got)
			}
			if got, want := c.ReadCache(), (forge.ReadCache{Hits: 1, Misses: 1, Saved: 1}); got != want {
				t.Errorf("ReadCache = %+v, want %+v: GitHub bills no 304 to a conditional request", got, want)
			}
		})
	}
}

func TestReads_a_304_to_an_unconditional_request_is_billed_and_fails_the_read(t *testing.T) {
	var calls atomic.Int32
	c, spent := meteredClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.WriteHeader(http.StatusNotModified)
	})
	got, err := c.Workflows(t.Context(), &repo)
	if err == nil {
		t.Errorf("Workflows answered 304 with nothing held = %+v, want a failed read, never an empty listing", got)
	}
	if s := spent.Load(); s != calls.Load() || s < 1 {
		t.Errorf("attempts = %d over %d requests served, want each attempt on the wire", s, calls.Load())
	}
	if rc := c.ReadCache(); rc.Saved != 0 || rc.Hits != 0 {
		t.Errorf("ReadCache = %+v, want no hit or saving from a 304 nothing held vouched for", rc)
	}
}

func TestReads_the_GitHub_fake_answers_in_the_shapes_this_client_reads(t *testing.T) {
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	srv := forgeconntest.New(forgeconntest.GitHub, &forgeconntest.World{Owner: "o", Repos: []forgeconntest.Repo{{
		Name: "r", DefaultBranch: "main",
		Alerts: []forgeconntest.Alert{
			{Number: 1, Created: created, Rule: "go/sql-injection", Severity: "high", Tool: "CodeQL", Path: "db/query.go", StartLine: 42},
			{Number: 2, Created: created, Rule: "go/unused", Tool: "CodeQL"},
		},
		Workflows: []forgeconntest.Workflow{{ID: 1, Name: "ci", Path: ".github/workflows/ci.yaml", State: "disabled_inactivity"}},
	}}})
	t.Cleanup(srv.Close)
	c := New(httpclient.New(5*time.Second), srv.URL+"/api/v3", forgeconntest.Token, testVersion, armed(), slog.New(slog.DiscardHandler), httpclient.WithMaxAttempts(1))
	alerts, err := c.CodeScanningAlerts(t.Context(), &repo)
	if err != nil {
		t.Fatalf("CodeScanningAlerts: %v", err)
	}
	if alerts.End != forge.EndWhole || len(alerts.Rows) != 2 {
		t.Fatalf("alerts = %+v, want both, read whole", alerts)
	}
	placed, unplaced := alerts.Rows[0], alerts.Rows[1]
	if placed.Rule != "go/sql-injection" || placed.SecuritySeverity != "high" || placed.Path != "db/query.go" || placed.StartLine != 42 || !placed.CreatedAt.Equal(created) {
		t.Errorf("alert 1 = %+v, want the high go/sql-injection at db/query.go:42", placed)
	}
	if unplaced.Path != "" || unplaced.StartLine != 0 || unplaced.SecuritySeverity != "" {
		t.Errorf("alert 2 = %+v, want no location and no severity", unplaced)
	}
	defs, err := c.Workflows(t.Context(), &repo)
	if err != nil {
		t.Fatalf("Workflows: %v", err)
	}
	if len(defs.Rows) != 1 || defs.Rows[0].Path != ".github/workflows/ci.yaml" || !defs.Rows[0].DisabledByInactivity() {
		t.Errorf("workflows = %+v, want ci disabled for inactivity", defs.Rows)
	}
	if u := srv.Unhandled(); len(u) != 0 {
		t.Errorf("the fake did not serve %q", u)
	}
}
