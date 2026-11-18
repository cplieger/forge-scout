// Package githubrest reads the two GitHub REST routes forgeapi does not model:
// a repository's open code-scanning alerts and its workflow definitions. Every
// status is classified on its code and headers, never on message text, and
// every failure it returns wraps one *forge.Error, except the caller's own
// cancelled or expired context, which stays the context error. Its reads go
// through httpcache's revalidation cache, which revalidates a page it holds.
package githubrest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/ghquota"
	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/httpcache"
	"github.com/cplieger/go/httpclient"
	"github.com/cplieger/go/jsonx"
)

const (
	perPage = 100
	// maxStateEcho bounds the bytes of an unknown workflow state an error quotes.
	maxStateEcho = 32
	// bodyCap rejects a runaway response; the decoders bound a page's rows at
	// perPage before decoding them.
	bodyCap = 8 << 20
)

// Client reads the GitHub REST API at one base URL with one token. It is not
// safe for concurrent use.
type Client struct {
	http    *http.Client
	meter   *ghquota.Meter
	cache   *httpcache.RoundTripper
	base    string
	token   string
	version string
	retry   []httpclient.DoOption
}

// New returns a client for the API at base (see APIBase). version is sent as
// X-GitHub-Api-Version, the one forgeapi pins, so both clients read one
// version's shapes. retry overrides the default retry policy. hc is copied,
// never changed: every request it sends, each redirect hop included, is
// admitted by meter, the connection's quota, which sees its response. Its
// transport sees each request as sent, a validator the read cache adds
// included.
func New(hc *http.Client, base, token, version string, meter *ghquota.Meter, logger *slog.Logger, retry ...httpclient.Option) *Client {
	opts := make([]httpclient.DoOption, 0, len(retry)+2)
	for _, o := range retry {
		opts = append(opts, o)
	}
	opts = append(opts, httpclient.WithLabel("github rest"), httpclient.WithLogger(logger))
	inner := hc.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	// GitHub bills no 304 to a conditional request
	// (https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate).
	// A read pages by number and reads no header of a 200, so a served
	// answer holds none.
	cache := httpcache.NewRoundTripper(admitting{next: meter.Observe(inner), meter: meter}, httpcache.Config{FreeNotModified: true})
	metered := *hc
	metered.Transport = cache
	return &Client{http: &metered, cache: cache, base: strings.TrimRight(base, "/"), token: token, version: version, retry: opts, meter: meter}
}

// ReadCache is what the client's read cache did since it was built.
func (c *Client) ReadCache() forge.ReadCache {
	s := c.cache.State()
	return forge.ReadCache{Hits: s.Hits, Misses: s.Misses, Saved: s.Saved}
}

// Budget is the connection's shared REST budget (see ghquota.Meter.Budget).
func (c *Client) Budget() forge.Budget { return c.meter.Budget() }

// admitting sends a request only when the meter admits it.
type admitting struct {
	next  http.RoundTripper
	meter *ghquota.Meter
}

func (a admitting) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := a.meter.Admit(); err != nil {
		return nil, err
	}
	return a.next.RoundTrip(req)
}

// APIBase is the REST root for a connection: apiURL when set, else
// forge.GitHubDotcomAPI for a GitHub.com host (forge.GitHubDotcom), else the
// GitHub Enterprise Server layout under webURL.
func APIBase(webURL, apiURL string) string {
	if apiURL != "" {
		return strings.TrimRight(apiURL, "/")
	}
	if u, err := url.Parse(webURL); err == nil && forge.GitHubDotcom(u.Hostname()) {
		return forge.GitHubDotcomAPI
	}
	return strings.TrimRight(webURL, "/") + "/api/v3"
}

type apiAlert struct {
	CreatedAt time.Time `json:"created_at"`
	Rule      struct {
		ID                    string `json:"id"`
		Description           string `json:"description"`
		Severity              string `json:"severity"`
		SecuritySeverityLevel string `json:"security_severity_level"`
	} `json:"rule"`
	Tool struct {
		Name string `json:"name"`
	} `json:"tool"`
	HTMLURL            string `json:"html_url"`
	MostRecentInstance struct {
		Location struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
		} `json:"location"`
	} `json:"most_recent_instance"`
	Number int64 `json:"number"`
}

// The values GitHub documents for an alert rule's severity and security
// severity, each nullable
// (https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository).
var (
	problemSeverities  = []string{"none", "note", "warning", "error"}
	securitySeverities = []string{"low", "medium", "high", "critical"}
)

// severity is raw as one of known in lower case, empty where GitHub sent
// none, and unknown for any other value, so an undocumented value is named,
// never dropped or echoed.
func severity(raw string, known []string) string {
	if raw == "" {
		return ""
	}
	if v := strings.ToLower(raw); slices.Contains(known, v) {
		return v
	}
	return "unknown"
}

// maxAlertPages bounds one repository's code-scanning read at 10,000 open
// alerts, a safety ceiling rather than a working limit: each page costs one
// request of the connection's quota (see ghquota.Meter.Admit).
const maxAlertPages = 100

// maxAlertBytes bounds what one repository's alerts hold, at what one
// response may already hold: a GitHub alert row is about 250 bytes, so
// 10,000 fit three times over, while a forge answering long strings on every
// page fails the read instead of filling memory (see alertBytes).
const maxAlertBytes = bodyCap

// alertRowBytes is the fixed part of one held alert; its Repo shares the
// strings every row holds.
var alertRowBytes = int(reflect.TypeFor[forge.Alert]().Size())

// alertBytes is what a holds beyond the strings it shares.
func alertBytes(a *forge.Alert) int {
	return alertRowBytes + len(a.Rule) + len(a.SecuritySeverity) + len(a.ProblemSeverity) + len(a.Tool) + len(a.URL) + len(a.Path)
}

// route is one listing this package reads and what its answers mean.
type route struct {
	what string
	// firstPageNotFound is the End a 404 on the first page reads as; empty
	// makes every 404 a failed read.
	firstPageNotFound forge.End
	// maxPages bounds one repository's read; a full last page there ends it
	// cut at the ceiling.
	maxPages int
}

var (
	// codeScanning answers 404 on its first page for a repository with no
	// analyses
	// (https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository).
	codeScanning = route{what: "code-scanning alerts", maxPages: maxAlertPages, firstPageNotFound: forge.EndNone}
	// workflows documents no 404.
	workflows = route{what: "workflows", maxPages: 3}
)

// list reads every page of rt for repo, decoding each with decode. Any
// answer its table does not name fails the read (see classify), never an
// empty listing.
func list[T any](ctx context.Context, c *Client, rt route, repo *forge.Repo, q url.Values, path string,
	decode func(repo forge.RepoKey, body []byte) ([]T, error),
) (forge.Listing[T], error) {
	var rows []T
	for page := 1; page <= rt.maxPages; page++ {
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		body, err := c.get(ctx, path, q)
		if fe, ok := errors.AsType[*forge.Error](err); ok && fe.Kind == forge.KindNotFound && page == 1 && rt.firstPageNotFound != "" {
			return forge.Listing[T]{End: rt.firstPageNotFound}, nil
		}
		var got []T
		if err == nil {
			got, err = decode(repo.Key, body)
		}
		if err != nil {
			return forge.Listing[T]{}, fmt.Errorf("%s of %s page %d: %w", rt.what, repo.Path, page, err)
		}
		rows = append(rows, got...)
		if len(got) < perPage {
			return forge.Listing[T]{Rows: rows, End: forge.EndWhole}, nil
		}
	}
	return forge.Listing[T]{Rows: rows, End: forge.EndCut, Cut: forge.CutCeiling}, nil
}

// CodeScanningAlerts reads repo's open code-scanning alerts: EndNone where
// the repository has no analyses (see codeScanning). Alerts past
// maxAlertBytes fail the read as upstream.
func (c *Client) CodeScanningAlerts(ctx context.Context, repo *forge.Repo) (forge.Listing[forge.Alert], error) {
	owner, name, err := segments(repo.Path)
	if err != nil {
		return forge.Listing[forge.Alert]{}, err
	}
	left := maxAlertBytes
	decode := func(repo forge.RepoKey, body []byte) ([]forge.Alert, error) {
		rows, err := decodeAlerts(repo, body)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			left -= alertBytes(&rows[i])
		}
		if left < 0 {
			return nil, upstream(fmt.Sprintf("alerts hold more than %d bytes", maxAlertBytes))
		}
		return rows, nil
	}
	return list(ctx, c, codeScanning, repo, url.Values{"state": {"open"}}, "/repos/"+owner+"/"+name+"/code-scanning/alerts", decode)
}

// decodeAlerts maps one code-scanning alerts page of repo. A body that is not
// a JSON array, null included, or that holds more rows than a page asks for,
// is an upstream failure, never an empty or a cut page.
func decodeAlerts(repo forge.RepoKey, body []byte) ([]forge.Alert, error) {
	d := jsonx.NewDecoder(bytes.NewReader(body), perPage)
	rows, err := d.Array(nil, perPage, "alerts page", func(a *apiAlert) error { return d.Decode(a) })
	if err == nil {
		err = d.End()
	}
	if err != nil {
		return nil, upstream("decode alerts page: " + err.Error())
	}
	if rows == nil {
		return nil, upstream("decode alerts page: not an array")
	}
	alerts := make([]forge.Alert, 0, len(rows))
	for i := range rows {
		a := &rows[i]
		alerts = append(alerts, forge.Alert{
			CreatedAt: a.CreatedAt, Repo: repo, Rule: cmp.Or(a.Rule.ID, a.Rule.Description),
			SecuritySeverity: severity(a.Rule.SecuritySeverityLevel, securitySeverities),
			ProblemSeverity:  severity(a.Rule.Severity, problemSeverities),
			Tool:             a.Tool.Name, URL: a.HTMLURL, Number: a.Number,
			Path: a.MostRecentInstance.Location.Path, StartLine: a.MostRecentInstance.Location.StartLine,
		})
	}
	return alerts, nil
}

type apiWorkflow struct {
	Name    string              `json:"name"`
	Path    string              `json:"path"`
	State   forge.WorkflowState `json:"state"`
	HTMLURL string              `json:"html_url"`
}

// Workflows reads repo's workflow definitions in every state; a state
// outside the five GitHub documents fails the read.
func (c *Client) Workflows(ctx context.Context, repo *forge.Repo) (forge.Listing[forge.Workflow], error) {
	owner, name, err := segments(repo.Path)
	if err != nil {
		return forge.Listing[forge.Workflow]{}, err
	}
	return list(ctx, c, workflows, repo, url.Values{}, "/repos/"+owner+"/"+name+"/actions/workflows", decodeWorkflows)
}

// decodeWorkflows maps one workflows page of repo. A page without a
// workflows array, null included, holding more rows than a page asks for,
// or naming a state outside the five GitHub documents
// (https://docs.github.com/en/rest/actions/workflows#list-repository-workflows),
// is an upstream failure.
func decodeWorkflows(repo forge.RepoKey, body []byte) ([]forge.Workflow, error) {
	d := jsonx.NewDecoder(bytes.NewReader(body), perPage)
	var page []apiWorkflow
	err := d.Object(func(key string) error {
		if !strings.EqualFold(key, "workflows") {
			return d.Skip()
		}
		var aerr error
		page, aerr = d.Array(page, perPage, "workflows page", func(w *apiWorkflow) error { return d.Decode(w) })
		return aerr
	})
	if err == nil {
		err = d.End()
	}
	if err != nil {
		return nil, upstream("decode workflows page: " + err.Error())
	}
	if page == nil {
		return nil, upstream("decode workflows page: no workflows array")
	}
	defs := make([]forge.Workflow, 0, len(page))
	for i := range page {
		w := &page[i]
		switch w.State {
		case forge.WorkflowActive, forge.WorkflowDeleted, forge.WorkflowDisabledFork, forge.WorkflowDisabledInactivity, forge.WorkflowDisabledManually:
			defs = append(defs, forge.Workflow{Repo: repo, Name: w.Name, Path: w.Path, State: w.State, URL: w.HTMLURL})
		default:
			return nil, upstream(fmt.Sprintf("decode workflows page: unknown workflow state %q", w.State[:min(len(w.State), maxStateEcho)]))
		}
	}
	return defs, nil
}

// segments splits a GitHub display path into its owner and name, each safe
// to place in a URL path; any other path is refused as invalid.
func segments(path string) (owner, name string, err error) {
	if err := forgeapi.ValidateSelector(forgeapi.FamilyGitHub, path); err != nil {
		return "", "", &forge.Error{Kind: forge.KindInvalid, Message: fmt.Sprintf("repository path %q is not a safe owner/name pair: %v", path, err)}
	}
	owner, name, _ = strings.Cut(path, "/")
	return owner, name, nil
}

// get reads one page's body under the client's retry policy, which repeats
// the answers and transport errors httpclient.IsTransient accepts, then
// classifies what failed.
func (c *Client) get(ctx context.Context, route string, q url.Values) ([]byte, error) {
	body, err := httpclient.Do(ctx, func(ctx context.Context) ([]byte, error) {
		return c.attempt(ctx, c.base+route+"?"+q.Encode())
	}, c.retry...)
	if err != nil {
		return nil, classify(ctx, err)
	}
	return body, nil
}

func (c *Client) attempt(ctx context.Context, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		httpclient.Drain(resp.Body)
		return nil, err
	}
	return httpclient.ReadLimitedBody(resp.Body, bodyCap)
}

// statusError classifies a non-2xx answer on its status code and headers,
// never its body. A 403 that ghquota.Throttled names, and a 429, are a rate
// limit; any other 403 is forbidden, naming the permissions GitHub's
// X-Accepted-GitHub-Permissions header lists
// (https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens).
// Every other answer is httpclient's *StatusError, which the retry loop
// repeats when httpclient.IsRetryableStatus accepts its code.
func statusError(resp *http.Response) error {
	code := resp.StatusCode
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return &forge.Error{Kind: forge.KindUnauthorized, Status: code, Message: "GitHub rejected the token"}
	case code == http.StatusTooManyRequests, code == http.StatusForbidden && ghquota.Throttled(resp.Header):
		return &forge.Error{Kind: forge.KindRateLimited, Status: code, RetryAfter: httpclient.ParseRetryAfterResponse(resp), Message: "GitHub rate limit"}
	case code == http.StatusForbidden:
		return &forge.Error{Kind: forge.KindForbidden, Status: code, Needs: resp.Header.Get("X-Accepted-GitHub-Permissions"), Message: "GitHub refused the read to this token"}
	case code == http.StatusNotFound:
		return &forge.Error{Kind: forge.KindNotFound, Status: code}
	default:
		return httpclient.CheckStatus(resp)
	}
}

// classify is err as a *forge.Error. The caller's own cancelled or expired
// context stays itself. A status or a failure without an answer is
// transient when httpclient would retry it, so a later read may succeed,
// and upstream otherwise: a refused redirect or dial, an oversized body or
// a status that repeats.
func classify(ctx context.Context, err error) error {
	if fe, ok := errors.AsType[*forge.Error](err); ok {
		return fe
	}
	if ctx.Err() != nil {
		return err
	}
	kind := forge.KindUpstream
	if httpclient.IsTransient(err) {
		kind = forge.KindTransient
	}
	fe := &forge.Error{Kind: kind, Message: err.Error()}
	if se, ok := errors.AsType[*httpclient.StatusError](err); ok {
		fe.Status, fe.RetryAfter = se.Code, se.RetryAfter
	}
	return fe
}

// upstream is an answer GitHub sent that the client cannot use.
func upstream(message string) *forge.Error {
	return &forge.Error{Kind: forge.KindUpstream, Message: message}
}
