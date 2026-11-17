//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub Enterprise Server's REST root, which forgeapi takes for a loopback
// instance; its GraphQL endpoint is graphQLPath.
const ghREST = "/api/v3"

// The contexts page each GitHub document asks for: a list folds over one
// context and the per-state counts, a single read over every context.
const (
	ghListFold = 1
	ghReadFold = 100
)

// Alert is one open code-scanning alert (GitHub only). Path and StartLine
// place its most recent instance; an empty Path sends no location.
type Alert struct {
	Created   time.Time
	Rule      string
	Severity  string
	Tool      string
	Path      string
	StartLine int
	Number    int64
}

// Workflow is one workflow definition (GitHub only). State is active,
// disabled_inactivity, disabled_manually, disabled_fork or deleted.
type Workflow struct {
	Name  string
	Path  string
	State string
	ID    int64
}

func (s *Server) serveGitHub(w http.ResponseWriter, r *http.Request) bool {
	h := w.Header()
	h.Set("X-GitHub-Request-Id", "FAKE:REQUEST")
	h.Set("X-GitHub-Api-Version-Selected", cmp.Or(r.Header.Get("X-GitHub-Api-Version"), "2022-11-28"))
	h.Set("X-RateLimit-Limit", "5000")
	remaining, reset := s.budget()
	h.Set("X-RateLimit-Remaining", itoa(remaining))
	h.Set("X-RateLimit-Reset", itoa(int(reset.Unix())))
	if r.Method == http.MethodPost && r.URL.Path == graphQLPath {
		h.Set("X-RateLimit-Resource", "graphql")
		return s.serveGitHubGraphQL(w, r)
	}
	h.Set("X-RateLimit-Resource", "core")
	if !strings.HasPrefix(r.URL.Path, ghREST+"/") {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, ghREST)
	if r.Method == http.MethodGet {
		switch path {
		case "/meta":
			writeJSON(w, http.StatusOK, map[string]any{"installed_version": "3.20.0", "verifiable_password_authentication": false})
			return true
		case "/versions":
			writeJSON(w, http.StatusOK, []string{"2026-03-10", "2022-11-28"})
			return true
		case "/user":
			h.Set("X-OAuth-Scopes", "repo, workflow")
			writeJSON(w, http.StatusOK, map[string]any{"login": s.world.Login, "name": s.world.Login, "html_url": webURL(s.URL, s.world.Login)})
			return true
		}
	}
	parts, ok := segments(strings.TrimPrefix(r.URL.EscapedPath(), ghREST+"/repos/"))
	if !strings.HasPrefix(path, "/repos/") || !ok || len(parts) < 2 || !strings.EqualFold(parts[0], s.world.Owner) {
		return false
	}
	repo, ok := s.repo(parts[1])
	if !ok {
		return false
	}
	return s.githubRepoRoute(w, r, repo, parts[2:])
}

// segments splits an escaped path into its unescaped segments, so a label
// name holding a slash stays one segment.
func segments(escaped string) ([]string, bool) {
	parts := strings.Split(escaped, "/")
	for i, p := range parts {
		u, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}
		parts[i] = u
	}
	return parts, true
}

// githubRoute is a repository route's pattern, the item number, run id or
// label name segments standing as {n}, {id} and {name}, with those values.
func githubRoute(method string, parts []string) (pattern string, args []string) {
	shape := append([]string(nil), parts...)
	if len(shape) >= 2 && (shape[0] == "issues" || shape[0] == "pulls") {
		shape[1], args = "{n}", append(args, parts[1])
	}
	if len(shape) == 4 && shape[0] == "issues" && shape[2] == "labels" {
		shape[3], args = "{name}", append(args, parts[3])
	}
	if len(shape) >= 3 && shape[0] == "actions" && shape[1] == "runs" {
		shape[2], args = "{id}", append(args, parts[2])
	}
	return method + " " + strings.Join(shape, "/"), args
}

// githubRepoRoute answers one repository route, reporting whether it serves
// it.
func (s *Server) githubRepoRoute(w http.ResponseWriter, r *http.Request, repo *Repo, parts []string) bool {
	pattern, args := githubRoute(r.Method, parts)
	switch pattern {
	case "GET ":
		s.githubRepoRecord(w, repo)
	case "GET issues/{n}":
		return s.githubIssue(w, repo, args[0])
	case "GET actions/runs":
		if repo.RunsForbidden {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Resource not accessible by personal access token"})
			return true
		}
		s.githubRuns(w, r, repo)
	case "GET actions/workflows":
		s.githubWorkflows(w, r, repo)
	case "GET code-scanning/alerts":
		s.githubAlerts(w, r, repo)
	case "GET labels":
		s.githubLabels(w, r, repo)
	default:
		return s.githubWriteRoute(w, r, repo, pattern, args)
	}
	return true
}

func (s *Server) githubRepoRecord(w http.ResponseWriter, repo *Repo) {
	writeJSON(w, http.StatusOK, map[string]any{
		"full_name": s.fullName(repo), "name": repo.Name, "owner": map[string]any{"login": s.world.Owner},
		"description": "", "html_url": webURL(s.URL, s.fullName(repo)), "clone_url": webURL(s.URL, s.fullName(repo)+".git"),
		"updated_at": stamp(time.Now()), "private": repo.Private, "archived": repo.Archived, "fork": repo.Fork,
		"default_branch": repo.DefaultBranch, "has_issues": true,
		"permissions":        map[string]any{"admin": false, "push": true, "pull": true},
		"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": false,
	})
}

// githubIssue answers the REST record of issue n, which on GitHub may be a
// pull request.
func (s *Server) githubIssue(w http.ResponseWriter, repo *Repo, n string) bool {
	it, pr := repo.issueOrPR(n)
	if it == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return true
	}
	writeJSON(w, http.StatusOK, s.githubIssueRecord(repo, it, pr))
	return true
}

// issueOrPR is issue or pull request n of the repository, and whether it is a
// pull request; nil where it holds neither.
func (r *Repo) issueOrPR(n string) (it *Item, pr bool) {
	if it := r.item(n, false); it != nil {
		return it, false
	}
	if it := r.item(n, true); it != nil {
		return it, true
	}
	return nil, false
}

// item is pull request n of the repository, or its issue n; nil where there
// is none.
func (r *Repo) item(n string, pr bool) *Item {
	number, err := strconv.Atoi(n)
	if err != nil {
		return nil
	}
	items := r.Issues
	if pr {
		items = r.PRs
	}
	for i := range items {
		if items[i].Number == number {
			return &items[i]
		}
	}
	return nil
}

func (s *Server) githubIssueRecord(repo *Repo, it *Item, pr bool) map[string]any {
	n := itoa(it.Number)
	state := "open"
	if !it.open() {
		state = "closed"
	}
	row := map[string]any{
		"number": it.Number, "state": state, "title": it.Title, "body": it.Body,
		"user": map[string]any{"login": it.Author, "type": cmp.Or(it.AuthorType, "User")}, "html_url": webURL(s.URL, s.fullName(repo)+"/issues/"+n),
		"created_at": stamp(it.Created), "updated_at": stamp(it.Updated), "labels": labelObjects(it.Labels), "comments": it.Comments,
	}
	if pr {
		row["pull_request"] = map[string]any{"url": webURL(s.URL, s.fullName(repo)+"/pull/"+n)}
	}
	return row
}

// restPage is the rows of a page-numbered REST list request, with GitHub's
// link header naming the next page when rows follow.
func (s *Server) restPage(w http.ResponseWriter, r *http.Request, n int) (lo, hi int) {
	size, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil || size < 1 {
		size = 30
	}
	if s.world.PageSize > 0 {
		size = min(size, s.world.PageSize)
	}
	page := pageOf(r)
	lo, hi, more := s.window((page-1)*size, size, n)
	if more {
		next := *r.URL
		q := next.Query()
		q.Set("page", itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", `<`+s.URL+next.RequestURI()+`>; rel="next"`)
	}
	return lo, hi
}

func githubConclusion(outcome string) (status string, conclusion any) {
	switch outcome {
	case "pending":
		return "in_progress", nil
	case "approval":
		return "completed", "action_required"
	default:
		return "completed", outcome
	}
}

// createdRange reads the created filter of a GitHub run listing: >=from, or
// from..to with both ends included. The zero time is an open end.
func createdRange(raw string) (from, to time.Time) {
	if rest, ok := strings.CutPrefix(raw, ">="); ok {
		from, _ = time.Parse(time.RFC3339, rest)
		return from, time.Time{}
	}
	lo, hi, ok := strings.Cut(raw, "..")
	if !ok {
		return time.Time{}, time.Time{}
	}
	from, _ = time.Parse(time.RFC3339, lo)
	to, _ = time.Parse(time.RFC3339, hi)
	return from, to
}

func (s *Server) githubRuns(w http.ResponseWriter, r *http.Request, repo *Repo) {
	from, to := createdRange(r.URL.Query().Get("created"))
	head := r.URL.Query().Get("head_sha")
	all := runsHighestIDFirst(repo, from)
	runs := all[:0:0]
	for i := range all {
		if (to.IsZero() || !all[i].Created.Truncate(time.Second).After(to)) && (head == "" || all[i].HeadSHA == head) {
			runs = append(runs, all[i])
		}
	}
	lo, hi := s.restPage(w, r, len(runs))
	rows := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		run := &runs[i]
		status, conclusion := githubConclusion(run.Outcome)
		rows = append(rows, map[string]any{
			"id": run.ID, "run_number": run.Number, "event": run.Event, "name": cmp.Or(run.Title, run.Workflow), "path": run.githubPath(),
			"head_branch": run.Branch, "head_sha": cmp.Or(run.HeadSHA, "0000000000000000000000000000000000000001"),
			"status": status, "conclusion": conclusion,
			"html_url":   webURL(s.URL, s.fullName(repo)+"/actions/runs/"+itoa(int(run.ID))),
			"created_at": stamp(run.Created), "run_started_at": stamp(run.Started), "updated_at": stamp(run.Updated),
			"repository": map[string]any{"full_name": s.fullName(repo)},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "workflow_runs": rows})
}

func (r *Run) githubPath() string {
	return cmp.Or(r.Path, ".github/workflows/"+r.Workflow+".yaml")
}

func (s *Server) githubLabels(w http.ResponseWriter, r *http.Request, repo *Repo) {
	lo, hi := s.restPage(w, r, len(repo.Labels))
	writeJSON(w, http.StatusOK, labelObjects(repo.Labels[lo:hi]))
}

func (s *Server) githubWorkflows(w http.ResponseWriter, r *http.Request, repo *Repo) {
	rows := []map[string]any{}
	if pageOf(r) == 1 {
		for _, wf := range repo.Workflows {
			rows = append(rows, map[string]any{
				"id": wf.ID, "name": wf.Name, "path": wf.Path, "state": wf.State,
				"html_url": webURL(s.URL, s.fullName(repo)+"/blob/main/"+wf.Path),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repo.Workflows), "workflows": rows})
}

func (s *Server) githubAlerts(w http.ResponseWriter, r *http.Request, repo *Repo) {
	if repo.AlertsRedirect != "" {
		http.Redirect(w, r, repo.AlertsRedirect, http.StatusFound)
		return
	}
	if repo.Alerts == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "no analysis found"})
		return
	}
	rows := []map[string]any{}
	if pageOf(r) == 1 {
		for _, a := range repo.Alerts {
			row := map[string]any{
				"number": a.Number, "created_at": stamp(a.Created),
				"html_url": webURL(s.URL, s.fullName(repo)+"/security/code-scanning/"+itoa(int(a.Number))),
				"rule":     map[string]any{"id": a.Rule, "description": a.Rule, "security_severity_level": a.Severity},
				"tool":     map[string]any{"name": a.Tool},
			}
			if a.Path != "" {
				row["most_recent_instance"] = map[string]any{"location": map[string]any{"path": a.Path, "start_line": a.StartLine}}
			}
			rows = append(rows, row)
		}
	}
	writeJSON(w, http.StatusOK, rows)
}

type graphQLRequest struct {
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
}

func (s *Server) serveGitHubGraphQL(w http.ResponseWriter, r *http.Request) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req graphQLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	q := docQuery{vars: req.Variables}
	if mutation, ok := s.githubMutation(req.OperationName, &q); ok {
		writeJSON(w, http.StatusOK, mutation)
		return true
	}
	remaining, reset := s.budget()
	data := map[string]any{"rateLimit": map[string]any{
		"cost": s.cost, "limit": 5000, "nodeCount": 0, "remaining": remaining, "resetAt": stamp(reset), "used": 5000 - remaining,
	}}
	var errs []map[string]any
	switch req.OperationName {
	case "RepoOwned":
		data["repositoryOwner"], errs = s.githubOwner(&q)
	case "PRMine":
		data["search"], errs = s.githubSearch(&q, true)
		errs = append(errs, s.searchErrors()...)
	case "IssueMine":
		data["search"], _ = s.githubSearch(&q, false)
		errs = s.searchErrors()
	case "PRList":
		data["repository"], errs = s.githubPRList(&q)
	case "PRRead":
		data["repository"], errs = s.githubPRRead(&q)
	default:
		return false
	}
	out := map[string]any{"data": data}
	if errs != nil {
		out["errors"] = errs
	}
	writeJSON(w, http.StatusOK, out)
	return true
}

// docQuery is one document's variables.
type docQuery struct {
	vars map[string]any
}

func (q *docQuery) str(key string) string {
	v, _ := q.vars[key].(string)
	return v
}

func (q *docQuery) num(key string) int {
	v, _ := q.vars[key].(float64)
	return int(v)
}

// on reads a boolean variable, true when the request leaves it out, as the
// PRRead document defaults its two.
func (q *docQuery) on(key string) bool {
	v, ok := q.vars[key].(bool)
	return !ok || v
}

// offset is where a page whose after cursor is the fake's own starts.
func (q *docQuery) offset() int {
	n, _ := strconv.Atoi(strings.TrimPrefix(q.str("after"), "c"))
	return n
}

// pageInfo is the connection position after rows up to hi.
func pageInfo(hi int, more bool) map[string]any {
	if !more {
		return map[string]any{"hasNextPage": false, "endCursor": nil}
	}
	return map[string]any{"hasNextPage": true, "endCursor": "c" + itoa(hi)}
}

func (s *Server) searchErrors() []map[string]any {
	if !s.world.GraphQLErrors {
		return nil
	}
	return []map[string]any{{"message": "Something went wrong while executing your query.", "path": []string{"search"}}}
}

func notFound(field, message string) []map[string]any {
	return []map[string]any{{"type": "NOT_FOUND", "path": []string{field}, "message": message}}
}

func (s *Server) githubOwner(q *docQuery) (node any, errs []map[string]any) {
	owner := q.str("owner")
	if !strings.EqualFold(owner, s.world.Owner) {
		return nil, notFound("repositoryOwner", "Could not resolve to a RepositoryOwner with the login of '"+owner+"'.")
	}
	lo, hi, more := s.window(q.offset(), q.num("first"), len(s.world.Repos))
	nodes := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		repo := &s.world.Repos[i]
		nodes = append(nodes, map[string]any{
			"nameWithOwner": s.fullName(repo), "description": "", "url": webURL(s.URL, s.fullName(repo)),
			"isPrivate": repo.Private, "isArchived": repo.Archived, "isFork": repo.Fork, "updatedAt": stamp(time.Now()),
			"defaultBranchRef": map[string]any{"name": repo.DefaultBranch}, "hasIssuesEnabled": true,
			"viewerPermission": "ADMIN", "mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": true,
		})
	}
	return map[string]any{"repositories": map[string]any{"pageInfo": pageInfo(hi, more), "nodes": nodes}}, nil
}

// searchHit is one search result: an item and its repository.
type searchHit struct {
	repo *Repo
	item *Item
}

// openHits is repo's open pull requests, or its open issues.
func openHits(repo *Repo, prs bool) []searchHit {
	items := repo.Issues
	if prs {
		items = repo.PRs
	}
	var out []searchHit
	for i := range items {
		if items[i].open() {
			out = append(out, searchHit{repo: repo, item: &items[i]})
		}
	}
	return out
}

// githubSearch answers one search page of open items under the owner the
// query names, with the FORBIDDEN error of each pull request whose check
// rollup it refuses.
func (s *Server) githubSearch(q *docQuery, prs bool) (page map[string]any, refused []map[string]any) {
	var hits []searchHit
	if strings.Contains(strings.ToLower(q.str("q")), "user:"+strings.ToLower(s.world.Owner)+" ") {
		for i := range s.world.Repos {
			hits = append(hits, openHits(&s.world.Repos[i], prs)...)
		}
	}
	lo, hi, more := s.window(q.offset(), q.num("first"), len(hits))
	nodes := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		repo, it := hits[i].repo, hits[i].item
		var node map[string]any
		if prs {
			node = s.githubPull(repo, it, q, ghListFold)
			if it.ChecksForbidden {
				refused = append(refused, rollupRefusal("search", "nodes", len(nodes)))
			}
		} else {
			node = s.githubIssueNode(repo, it)
		}
		node["repository"] = map[string]any{"nameWithOwner": s.fullName(repo)}
		nodes = append(nodes, node)
	}
	return map[string]any{"issueCount": len(hits), "pageInfo": pageInfo(hi, more), "nodes": nodes}, refused
}

func (s *Server) githubPRList(q *docQuery) (repository any, errs []map[string]any) {
	repo, ok := s.repo(q.str("name"))
	if !ok || !strings.EqualFold(q.str("owner"), s.world.Owner) {
		return nil, notFound("repository", "Could not resolve to a Repository with the name '"+q.str("owner")+"/"+q.str("name")+"'.")
	}
	open := openHits(repo, true)
	lo, hi, more := s.window(q.offset(), q.num("first"), len(open))
	nodes := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		if open[i].item.ChecksForbidden {
			errs = append(errs, rollupRefusal("repository", "pullRequests", "nodes", len(nodes)))
		}
		nodes = append(nodes, s.githubPull(repo, open[i].item, q, ghListFold))
	}
	return map[string]any{
		"nameWithOwner": s.fullName(repo), "viewerPermission": "WRITE",
		"pullRequests": map[string]any{"totalCount": len(open), "pageInfo": pageInfo(hi, more), "nodes": nodes},
	}, errs
}

func (s *Server) githubPRRead(q *docQuery) (repository any, errs []map[string]any) {
	repo, ok := s.repo(q.str("name"))
	if !ok || !strings.EqualFold(q.str("owner"), s.world.Owner) {
		return nil, notFound("repository", "Could not resolve to a Repository with the name '"+q.str("owner")+"/"+q.str("name")+"'.")
	}
	out := map[string]any{"nameWithOwner": s.fullName(repo), "viewerPermission": "WRITE", "pullRequest": nil}
	for i := range repo.PRs {
		it := &repo.PRs[i]
		if it.Number != q.num("number") {
			continue
		}
		out["pullRequest"] = s.githubPull(repo, it, q, ghReadFold)
		if it.ChecksForbidden {
			return out, []map[string]any{rollupRefusal("repository", "pullRequest")}
		}
		return out, nil
	}
	return out, []map[string]any{{
		"type": "NOT_FOUND", "path": []string{"repository", "pullRequest"},
		"message": "Could not resolve to a PullRequest with the number of " + itoa(q.num("number")) + ".",
	}}
}

// rollupRefusal is the error GitHub answers beside a refused rollup, at the
// pull request under path.
func rollupRefusal(path ...any) map[string]any {
	return map[string]any{
		"type": "FORBIDDEN", "message": "Resource not accessible by personal access token",
		"path": append(path, "commits", "nodes", 0, "commit", "statusCheckRollup", "contexts", "nodes"),
	}
}

func githubAuthor(it *Item) map[string]any {
	author := map[string]any{"login": it.Author}
	if it.AuthorType != "" {
		author["__typename"] = it.AuthorType
	}
	return author
}

func (s *Server) githubIssueNode(repo *Repo, it *Item) map[string]any {
	return map[string]any{
		"__typename": "Issue", "number": it.Number, "title": it.Title, "body": it.Body, "state": strings.ToUpper(cmp.Or(it.State, "open")),
		"url":       webURL(s.URL, s.fullName(repo)+"/issues/"+itoa(it.Number)),
		"createdAt": stamp(it.Created), "updatedAt": stamp(it.Updated), "author": githubAuthor(it),
		"labels":   map[string]any{"totalCount": len(it.Labels), "nodes": labelObjects(it.Labels)},
		"comments": map[string]any{"totalCount": it.Comments},
	}
}

// githubPull is the pull-request selection every GitHub document shares,
// with a check fold over at most fold contexts and the fields the request's
// mergeState and reviews variables leave in.
func (s *Server) githubPull(repo *Repo, it *Item, q *docQuery, fold int) map[string]any {
	state := strings.ToUpper(cmp.Or(it.State, "open"))
	node := map[string]any{
		"__typename": "PullRequest", "number": it.Number, "title": it.Title, "body": it.Body, "state": state,
		"merged": state == "MERGED", "isDraft": it.Draft,
		"url":       webURL(s.URL, s.fullName(repo)+"/pull/"+itoa(it.Number)),
		"createdAt": stamp(it.Created), "updatedAt": stamp(it.Updated), "author": githubAuthor(it),
		"headRefName": cmp.Or(it.SourceBranch, "feature"), "headRepository": map[string]any{"nameWithOwner": s.fullName(repo)},
		"baseRefName": repo.DefaultBranch, "headRefOid": it.HeadSHA,
		"autoMergeRequest": nil, "mergeQueueEntry": nil,
		"labels":             map[string]any{"totalCount": len(it.Labels), "nodes": labelObjects(it.Labels)},
		"totalCommentsCount": it.Comments,
		"commits":            map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": it.HeadSHA, "statusCheckRollup": s.githubRollup(repo, it, fold)}}}},
	}
	if it.AutoMerge {
		node["autoMergeRequest"] = map[string]any{"enabledAt": stamp(it.Updated), "mergeMethod": "SQUASH"}
	}
	if q.on("mergeState") {
		node["mergeable"], node["mergeStateStatus"] = "MERGEABLE", "CLEAN"
		if it.Conflicting {
			node["mergeable"], node["mergeStateStatus"] = "CONFLICTING", "DIRTY"
		}
	}
	if q.on("reviews") {
		reviews := make([]any, 0, it.Approvals+it.ChangesRequested)
		for range it.Approvals {
			reviews = append(reviews, map[string]any{"state": "APPROVED"})
		}
		for range it.ChangesRequested {
			reviews = append(reviews, map[string]any{"state": "CHANGES_REQUESTED"})
		}
		node["latestOpinionatedReviews"] = map[string]any{"totalCount": len(reviews), "nodes": reviews}
	}
	return node
}

// githubRollup is the check rollup of a pull request's head: null with no
// check, the counts by state beside at most fold context rows, or the counts
// with null rows where the rollup is refused.
func (s *Server) githubRollup(repo *Repo, it *Item, fold int) any {
	states, ok := repo.Checks[it.HeadSHA]
	if !ok {
		return nil
	}
	counts := map[string]int{}
	nodes := []any{}
	for i, st := range states {
		status, conclusion := "COMPLETED", strings.ToUpper(st)
		if st == "pending" {
			status, conclusion = "IN_PROGRESS", ""
		}
		counts[cmp.Or(conclusion, status)]++
		if i < fold {
			nodes = append(nodes, map[string]any{
				"__typename": "CheckRun", "name": "check-" + itoa(i), "status": status, "conclusion": nullable(conclusion),
				"detailsUrl": webURL(s.URL, s.fullName(repo)+"/runs/"+itoa(i)),
			})
		}
	}
	byState := make([]any, 0, len(counts))
	for state, n := range counts {
		byState = append(byState, map[string]any{"state": state, "count": n})
	}
	rollupState, _ := repo.combined(it.HeadSHA)
	contexts := map[string]any{
		"totalCount": len(states), "checkRunCount": len(states), "statusContextCount": 0,
		"checkRunCountsByState": byState, "statusContextCountsByState": []any{},
		"pageInfo": pageInfo(min(fold, len(states)), fold < len(states)), "nodes": nodes,
	}
	if it.ChecksForbidden {
		contexts["nodes"] = nil
	}
	return map[string]any{"state": strings.ToUpper(rollupState), "contexts": contexts}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
