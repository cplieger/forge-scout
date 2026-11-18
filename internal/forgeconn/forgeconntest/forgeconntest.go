// Package forgeconntest serves fake forge instances over HTTP for tests: one
// World of repositories, runs, pull requests and issues, answered in the
// wire shape of GitHub, GitLab, Gitea or Forgejo, each written from that
// product's published API shape. Every request is recorded; one the fake
// does not serve answers 404 and is listed by Unhandled.
//
//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forge-scout/internal/forgeconn"
	"github.com/cplieger/forgeapi/v3"
)

// Product is the product a fake answers as.
type Product string

// The products.
const (
	GitHub  Product = "github"
	GitLab  Product = "gitlab"
	Gitea   Product = "gitea"
	Forgejo Product = "forgejo"
)

// Token is the credential Server.Config hands forgeconn.
const Token = "fake-token"

// World is what a fake instance holds. The zero Remaining reports 4900.
type World struct {
	Login string
	Owner string
	// Version is the version the instance reports, the fake's own default
	// for the product when empty.
	Version string
	Repos   []Repo
	// Remaining is the request budget each response reports (none on Gitea).
	Remaining int
	// PageSize caps the rows one page answers whatever the request asks, so
	// a walk spans pages with few rows; zero answers what was asked.
	PageSize int
	// UserNamespace makes the owner a GitLab user rather than a group: the
	// group routes answer 404 and the user's project route answers.
	UserNamespace bool
	// GraphQLErrors answers every GitHub search document and every GitLab
	// merge request list document with data beside an error, which forgeapi
	// marks as a partial answer.
	GraphQLErrors bool
	// RefuseDocuments has GitLab refuse every document but the owner lookup,
	// as an instance whose schema lacks a field they select does: forgeapi
	// then reads REST for the connection's life.
	RefuseDocuments bool
	// RejectToken answers the account route 401, as for a revoked token,
	// while the detection routes still answer.
	RejectToken bool
}

// Repo is one repository under the World's owner.
type Repo struct {
	// Checks maps a head commit to the state of each of its checks:
	// success, failure, neutral or pending. A commit with no entry has no
	// check at all. GitLab has no checks of its own: a head's pipeline is the
	// newest of Runs on it.
	Checks        map[string][]string
	Name          string
	DefaultBranch string
	// Labels are the labels the repository defines.
	Labels []string
	Runs   []Run
	PRs    []Item
	Issues []Item
	// Alerts nil answers the code-scanning route 404 (no analyses).
	Alerts    []Alert
	Workflows []Workflow
	Private   bool
	Archived  bool
	Fork      bool
}

// Run is one CI run. Outcome is success, failure, cancelled, pending or
// approval (a fork run waiting for a maintainer, GitHub only).
type Run struct {
	Created  time.Time
	Started  time.Time
	Updated  time.Time
	Workflow string
	// Title is the name the forge gives the run where it is not Workflow, as
	// a GitHub run-name or a GitLab pipeline name sets it; Gitea and Forgejo
	// name a run by its workflow file alone.
	Title string
	// Path is the run's workflow definition on GitHub,
	// .github/workflows/<Workflow>.yaml when empty.
	Path    string
	Branch  string
	HeadSHA string
	Event   string
	Outcome string
	ID      int64
	Number  int64
}

// Item is one pull request or issue.
type Item struct {
	Created time.Time
	Updated time.Time
	// State is open when empty, or closed, or merged (a pull request only).
	State  string
	Title  string
	Body   string
	Author string
	// AuthorType is the GitHub document's author __typename, such as Bot,
	// left out of the answer where empty.
	AuthorType string
	HeadSHA    string
	// SourceBranch is the pull request's head branch, feature when empty.
	SourceBranch string
	Labels       []string
	Number       int
	Comments     int
	// Approvals and ChangesRequested are the reviewers whose latest review
	// approves or requests changes.
	Approvals        int
	ChangesRequested int
	Draft            bool
	// AutoMerge is an auto-merge armed on the pull request.
	AutoMerge bool
	// Conflicting has the forge report the pull request unmergeable.
	Conflicting bool
	// ChecksForbidden has GitHub refuse the pull request's check rollup with
	// FORBIDDEN beside every other field, as it answers a fine-grained token
	// on a private repository.
	ChecksForbidden bool
}

// open reports an item whose State is open.
func (it *Item) open() bool { return it.State == "" || it.State == "open" }

// injected is one answer Answer queued.
type injected struct {
	header  http.Header
	message string
	status  int
}

// Server is one fake instance. Its methods are safe for concurrent use, and
// it answers one request at a time.
type Server struct {
	*httptest.Server
	// reset is when the reported budget renews; zero is an hour after each
	// response.
	reset    time.Time
	answers  map[string][]injected
	product  Product
	requests []string
	unhand   []string
	world    World
	mu       sync.Mutex
}

// New starts a fake instance of product serving a copy of w. The caller
// closes it.
func New(product Product, w *World) *Server {
	world := *w
	if world.Remaining == 0 {
		world.Remaining = 4900
	}
	s := &Server{product: product, world: world, answers: map[string][]injected{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Config is a connection named connection at this instance, with a static
// token, private addresses and plain HTTP allowed, and its log discarded.
func (s *Server) Config(connection string) forgeconn.Config {
	return forgeconn.Config{
		Connection: connection, Web: s.URL, Credential: staticToken(Token), Logger: slog.New(slog.DiscardHandler),
		PrivateAddresses: true, PlaintextHTTP: true,
	}
}

// SetBudget changes the budget later responses report.
func (s *Server) SetBudget(remaining int, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.world.Remaining, s.reset = remaining, reset
}

// Answer has the next request on route answer status with header and a
// body naming message, once. A route is "METHOD /path" with no query, or
// "POST graphql <operation>" for a GraphQL document, as Requests spells it.
func (s *Server) Answer(route string, status int, header http.Header, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[route] = append(s.answers[route], injected{status: status, header: header.Clone(), message: message})
}

// EditPR changes pull request number of repo. touch moves its updated time
// on, as an edit on the forge does; without it the record moves as a check
// finishing does, with no new updated time.
func (s *Server) EditPR(repo string, number int, touch bool, edit func(*Item)) {
	s.edit(repo, number, true, touch, edit)
}

// EditIssue changes issue number of repo, as EditPR does.
func (s *Server) EditIssue(repo string, number int, touch bool, edit func(*Item)) {
	s.edit(repo, number, false, touch, edit)
}

// PushHead moves pull request number of repo to the head sha, as a push
// does.
func (s *Server) PushHead(repo string, number int, sha string) {
	s.EditPR(repo, number, true, func(it *Item) { it.HeadSHA = sha })
}

// PR is a copy of pull request number of repo as the fake holds it now, for
// what a product's answers do not carry, such as Gitea's armed merge.
func (s *Server) PR(repo string, number int) Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.mustRepo(repo).item(itoa(number), true)
	if it == nil {
		panic(fmt.Sprintf("forgeconntest: %s holds no pull request %d", repo, number))
	}
	out := *it
	out.Labels = append([]string(nil), it.Labels...)
	return out
}

// SetChecks sets the states of the checks on commit sha of repo.
func (s *Server) SetChecks(repo, sha string, states ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.mustRepo(repo)
	if r.Checks == nil {
		r.Checks = map[string][]string{}
	}
	r.Checks[sha] = append([]string(nil), states...)
}

func (s *Server) edit(repo string, number int, pr, touch bool, edit func(*Item)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.mustRepo(repo)
	items := r.Issues
	if pr {
		items = r.PRs
	}
	for i := range items {
		if items[i].Number != number {
			continue
		}
		edit(&items[i])
		if touch {
			items[i].Updated = later(items[i].Updated)
		}
		return
	}
	panic(fmt.Sprintf("forgeconntest: %s holds no item %d", repo, number))
}

// later is a time after t at whole seconds, now where now is after it.
func later(t time.Time) time.Time {
	now := time.Now().UTC().Truncate(time.Second)
	if next := t.Add(time.Second); !now.After(next) {
		return next
	}
	return now
}

func (s *Server) mustRepo(name string) *Repo {
	r, ok := s.repo(name)
	if !ok {
		panic("forgeconntest: no repository " + name)
	}
	return r
}

// budget is the budget a response reports now; s.mu is held.
func (s *Server) budget() (remaining int, reset time.Time) {
	if s.reset.IsZero() {
		return s.world.Remaining, time.Now().Add(time.Hour)
	}
	return s.world.Remaining, s.reset
}

// Requests lists every request served, as "METHOD path?query" (a GraphQL
// request as "POST graphql <operation>").
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Unhandled lists the requests the fake did not serve.
func (s *Server) Unhandled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unhand...)
}

func (s *Server) record(line string, handled bool) {
	s.requests = append(s.requests, line)
	if !handled {
		s.unhand = append(s.unhand, line)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer(w, r)
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	line := r.Method + " " + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		line += "?" + r.URL.RawQuery
	}
	route := r.Method + " " + r.URL.EscapedPath()
	if op, ok := graphQLOperation(r); ok {
		line, route = "POST graphql "+op, "POST graphql "+op
	}
	if s.inject(w, route) {
		s.record(line, true)
		return
	}
	if s.foreignProbe(r) {
		s.record(line, true)
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	if s.world.RejectToken && r.URL.Path == s.accountRoute() {
		s.record(line, true)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
		return
	}
	var handled bool
	switch s.product {
	case GitHub:
		handled = s.serveGitHub(w, r)
	case GitLab:
		line, handled = s.serveGitLab(w, r, line)
	default:
		handled = s.serveGitea(w, r)
	}
	s.record(line, handled)
	if !handled {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
}

// inject writes the first answer Answer queued for route, reporting whether
// one was.
func (s *Server) inject(w http.ResponseWriter, route string) bool {
	queued := s.answers[route]
	if len(queued) == 0 {
		return false
	}
	a := queued[0]
	s.answers[route] = queued[1:]
	maps.Copy(w.Header(), a.header)
	writeJSON(w, a.status, map[string]any{"message": a.message})
	return true
}

// graphQLPath is where GitHub Enterprise Server and GitLab both serve
// GraphQL.
const graphQLPath = "/api/graphql"

// graphQLOperation reads the operation name of a GraphQL request,
// leaving the body for the handler to read again.
func graphQLOperation(r *http.Request) (string, bool) {
	if r.Method != http.MethodPost || r.URL.Path != graphQLPath {
		return "", false
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req graphQLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "<undecodable>", true
	}
	return req.OperationName, true
}

// foreignProbe reports another family's detection read, which a real
// instance of this product answers 404: forgeapi asks GitLab's metadata route,
// then GitHub's, before the Gitea family's version route.
func (s *Server) foreignProbe(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v4/metadata":
		return s.product != GitLab
	case "/api/v3/meta":
		return s.product != GitHub
	}
	return false
}

func (s *Server) accountRoute() string {
	switch s.product {
	case GitHub:
		return ghREST + "/user"
	case GitLab:
		return glREST + "/user"
	default:
		return gtREST + "/user"
	}
}

func (s *Server) repo(name string) (*Repo, bool) {
	for i := range s.world.Repos {
		if strings.EqualFold(s.world.Repos[i].Name, name) {
			return &s.world.Repos[i], true
		}
	}
	return nil, false
}

func (s *Server) fullName(r *Repo) string { return s.world.Owner + "/" + r.Name }

// combined is the one state the checks on sha fold to: failure over pending
// over success.
func (r *Repo) combined(sha string) (string, bool) {
	states, ok := r.Checks[sha]
	if !ok {
		return "", false
	}
	out := "success"
	for _, st := range states {
		switch st {
		case "failure":
			return "failure", true
		case "pending":
			out = "pending"
		}
	}
	return out, true
}

// window is the rows a page of size starting at offset answers out of n,
// capped by World.PageSize, and whether rows follow it.
func (s *Server) window(offset, size, n int) (lo, hi int, more bool) {
	if s.world.PageSize > 0 {
		size = min(size, s.world.PageSize)
	}
	lo = min(max(offset, 0), n)
	hi = min(lo+max(size, 1), n)
	return lo, hi, hi < n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func stamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// since parses a time query parameter, the zero time when absent.
func since(r *http.Request, key string) time.Time {
	raw := strings.TrimPrefix(r.URL.Query().Get(key), ">=")
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// runsHighestIDFirst returns the repository's runs created at or after bound,
// newest id first.
func runsHighestIDFirst(repo *Repo, bound time.Time) []Run {
	out := make([]Run, 0, len(repo.Runs))
	for i := range repo.Runs {
		if bound.IsZero() || !repo.Runs[i].Created.Before(bound) {
			out = append(out, repo.Runs[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// restRoute is a GitHub or Gitea repository route's pattern, the item
// number, label, run id and commit segments standing as {n}, {name}, {id}
// and {sha}, with those values in path order.
func restRoute(method string, parts []string) (pattern string, args []string) {
	shape := append([]string(nil), parts...)
	if len(shape) >= 2 && (shape[0] == "issues" || shape[0] == "pulls" || shape[0] == "commits") {
		shape[1], args = "{n}", append(args, parts[1])
		if shape[0] == "commits" {
			shape[1] = "{sha}"
		}
	}
	if len(shape) == 4 && shape[0] == "issues" && shape[2] == "labels" {
		shape[3], args = "{name}", append(args, parts[3])
	}
	if len(shape) >= 3 && shape[0] == "actions" && shape[1] == "runs" {
		shape[2], args = "{id}", append(args, parts[2])
	}
	return method + " " + strings.Join(shape, "/"), args
}

// labelObjects is names as label objects, each in one colour.
func labelObjects(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "color": "ededed", "description": ""})
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

func pageOf(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

func webURL(base, path string) string { return fmt.Sprintf("%s/%s", base, path) }

// staticToken is a token held for the life of the connection.
type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }
func (staticToken) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (staticToken) State() forgeapi.CredState               { return forgeapi.CredValid }
