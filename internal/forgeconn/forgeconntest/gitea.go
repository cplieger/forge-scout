//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const gtREST = "/api/v1"

// giteaMaxItems is the max_response_items both products ship, the most rows
// one page serves whatever limit is asked for.
const giteaMaxItems = 50

// The versions a Gitea-family fake reports when World.Version is empty.
// Forgejo declares its whole-run re-run from 17.
const (
	giteaVersion   = "1.27.0"
	forgejoVersion = "17.0.0+gitea-1.22.0"
)

func (s *Server) serveGitea(w http.ResponseWriter, r *http.Request) bool {
	if s.product == Forgejo {
		remaining, reset := s.budget()
		w.Header().Set("RateLimit", `"default";r=`+itoa(remaining)+`;t=`+itoa(max(int(time.Until(reset).Seconds()), 0)))
	}
	if r.Method == http.MethodGet {
		if handled, ok := s.giteaInstanceRoute(w, r); ok {
			return handled
		}
	}
	return s.giteaRepoRoute(w, r)
}

// giteaInstanceRoute answers the routes no repository addresses; ok reports
// one.
func (s *Server) giteaInstanceRoute(w http.ResponseWriter, r *http.Request) (handled, ok bool) {
	switch r.URL.Path {
	case "/swagger.v1.json":
		s.giteaSwagger(w)
	case gtREST + "/version":
		writeJSON(w, http.StatusOK, map[string]any{"version": s.giteaVersion()})
	case gtREST + "/settings/api":
		writeJSON(w, http.StatusOK, map[string]any{"max_response_items": s.giteaMax()})
	case gtREST + "/user":
		writeJSON(w, http.StatusOK, map[string]any{"login": s.world.Login, "full_name": s.world.Login, "html_url": webURL(s.URL, s.world.Login)})
	case gtREST + "/repos/issues/search":
		s.giteaSearch(w, r)
	default:
		if owner, found := cutBetween(r.URL.Path, gtREST+"/users/", "/repos"); found {
			s.giteaRepos(w, r, owner)
			return true, true
		}
		// The owner is a user, which holds no organization labels.
		if _, found := cutBetween(r.URL.Path, gtREST+"/orgs/", "/labels"); found {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "GetOrgByName"})
			return true, true
		}
		return false, false
	}
	return true, true
}

// cutBetween is the one path segment between prefix and suffix.
func cutBetween(path, prefix, suffix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	segment, ok := strings.CutSuffix(rest, suffix)
	return segment, ok && segment != "" && !strings.Contains(segment, "/")
}

func (s *Server) giteaVersion() string {
	if s.world.Version != "" {
		return s.world.Version
	}
	if s.product == Forgejo {
		return forgejoVersion
	}
	return giteaVersion
}

// giteaMax is the max_response_items the instance states: World.PageSize
// where set, so a walk spans pages of that many rows.
func (s *Server) giteaMax() int { return cmp.Or(s.world.PageSize, giteaMaxItems) }

// giteaRerunRoute is the re-run route the instance's swagger declares, empty
// on a Forgejo before 17, which declares none.
func (s *Server) giteaRerunRoute() string {
	switch {
	case s.product == Gitea:
		return "rerun-failed-jobs"
	case versionAtLeast(s.giteaVersion(), 17, 0):
		return "rerun"
	}
	return ""
}

func (s *Server) giteaSwagger(w http.ResponseWriter) {
	title, runs, rerun := "Gitea API", "getWorkflowRuns", "/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs"
	if s.product == Forgejo {
		title, runs, rerun = "Forgejo API", "ListActionRuns", "/repos/{owner}/{repo}/actions/runs/{run_id}/rerun"
	}
	paths := map[string]any{
		"/repos/{owner}/{repo}/actions/runs": map[string]any{"get": map[string]any{"operationId": runs, "parameters": []any{map[string]any{"name": "head_sha", "in": "query"}}}},
	}
	if s.giteaRerunRoute() != "" {
		paths[rerun] = map[string]any{"post": map[string]any{"operationId": "rerun"}}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"swagger": "2.0", "basePath": gtREST, "info": map[string]any{"title": title, "version": s.giteaVersion()}, "paths": paths,
	})
}

// giteaPage is the rows of a page-numbered list request, at the asked limit
// capped by the stated maximum, as both products page their lists.
func (s *Server) giteaPage(r *http.Request, n int) (lo, hi int) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > s.giteaMax() {
		limit = s.giteaMax()
	}
	lo, hi, _ = s.window((pageOf(r)-1)*limit, limit, n)
	return lo, hi
}

// giteaRepos answers one page of owner's repositories with the stated total,
// and 404 for an owner the instance does not hold.
func (s *Server) giteaRepos(w http.ResponseWriter, r *http.Request, owner string) {
	if owner != s.world.Owner {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "user redirect does not exist [name: " + owner + "]"})
		return
	}
	lo, hi := s.giteaPage(r, len(s.world.Repos))
	rows := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		rows = append(rows, s.giteaRepoRecord(&s.world.Repos[i]))
	}
	w.Header().Set("X-Total-Count", itoa(len(s.world.Repos)))
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) giteaRepoRecord(repo *Repo) map[string]any {
	return map[string]any{
		"full_name": s.fullName(repo), "name": repo.Name, "owner": map[string]any{"login": s.world.Owner},
		"description": "", "html_url": webURL(s.URL, s.fullName(repo)), "clone_url": webURL(s.URL, s.fullName(repo)+".git"),
		"updated_at": stamp(time.Now()), "private": repo.Private, "archived": repo.Archived, "fork": repo.Fork,
		"default_branch": repo.DefaultBranch, "has_issues": true, "has_pull_requests": true,
		"permissions":         map[string]any{"admin": true, "push": true, "pull": true},
		"allow_merge_commits": true, "allow_squash_merge": true, "allow_rebase": false, "allow_rebase_explicit": false,
		"allow_fast_forward_only_merge": false, "default_merge_style": "merge",
	}
}

// giteaSearch answers one page of the issue search: the open pull requests
// or issues across the owner's repositories, 400 for an owner the instance
// does not hold.
func (s *Server) giteaSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("owner") != s.world.Owner {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "user does not exist [name: " + q.Get("owner") + "]"})
		return
	}
	prs := q.Get("type") == "pulls"
	var hits []searchHit
	for i := range s.world.Repos {
		hits = append(hits, openHits(&s.world.Repos[i], prs)...)
	}
	lo, hi := s.giteaPage(r, len(hits))
	rows := make([]map[string]any, 0, hi-lo)
	for _, hit := range hits[lo:hi] {
		row := s.giteaIssueRecord(hit.repo, hit.item, prs)
		row["repository"] = map[string]any{"full_name": s.fullName(hit.repo), "name": hit.repo.Name, "owner": s.world.Owner}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, rows)
}

// giteaRouteRepo is the repository a request's path addresses and the
// segments after it.
func (s *Server) giteaRouteRepo(r *http.Request) (*Repo, []string, bool) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), gtREST+"/repos/")
	if !ok {
		return nil, nil, false
	}
	parts, ok := segments(rest)
	if !ok || len(parts) < 2 || parts[0] != s.world.Owner {
		return nil, nil, false
	}
	repo, ok := s.repo(parts[1])
	return repo, parts[2:], ok
}

func (s *Server) giteaRepoRoute(w http.ResponseWriter, r *http.Request) bool {
	repo, tail, ok := s.giteaRouteRepo(r)
	if !ok {
		return false
	}
	pattern, args := restRoute(r.Method, tail)
	if route, ok := giteaItemRoutes[pattern]; ok {
		it, pr := repo.issueOrPR(args[0])
		if strings.HasPrefix(pattern, r.Method+" pulls/") {
			it, pr = repo.item(args[0], true), true
		}
		if it == nil {
			return giteaNotFound(w)
		}
		route(s, w, r, &itemCall{repo: repo, it: it, args: args, pr: pr})
		return true
	}
	switch pattern {
	case "GET ":
		writeJSON(w, http.StatusOK, s.giteaRepoRecord(repo))
	case "GET pulls":
		s.giteaPulls(w, r, repo)
	case "GET labels":
		lo, hi := s.giteaPage(r, len(repo.Labels))
		writeJSON(w, http.StatusOK, giteaLabels(repo, repo.Labels[lo:hi]))
	case "GET commits/{sha}/status":
		s.giteaStatus(w, r, repo, args[0])
	case "GET actions/runs":
		s.giteaRuns(w, r, repo)
	case "GET actions/runs/{id}":
		run := repo.run(args[0])
		if run == nil {
			return giteaNotFound(w)
		}
		writeJSON(w, http.StatusOK, s.giteaRun(repo, run))
	case "POST actions/runs/{id}/rerun-failed-jobs", "POST actions/runs/{id}/rerun":
		if pattern != "POST actions/runs/{id}/"+s.giteaRerunRoute() {
			return false
		}
		return s.giteaRerun(w, repo, args[0])
	default:
		return false
	}
	return true
}

// itemCall is a request on one pull request or issue: the route's values,
// and whether the item is a pull request.
type itemCall struct {
	repo *Repo
	it   *Item
	args []string
	pr   bool
}

// giteaItemRoutes are the routes on one pull request or issue, a pulls route
// addressing a pull request and an issues route either; a number the
// repository does not hold answers 404.
var giteaItemRoutes = map[string]func(*Server, http.ResponseWriter, *http.Request, *itemCall){
	"GET pulls/{n}": func(s *Server, w http.ResponseWriter, _ *http.Request, c *itemCall) {
		writeJSON(w, http.StatusOK, s.giteaPull(c.repo, c.it))
	},
	"GET pulls/{n}/reviews": func(s *Server, w http.ResponseWriter, r *http.Request, c *itemCall) {
		reviews := giteaReviews(c.it)
		lo, hi := s.giteaPage(r, len(reviews))
		writeJSON(w, http.StatusOK, reviews[lo:hi])
	},
	"GET issues/{n}": func(s *Server, w http.ResponseWriter, _ *http.Request, c *itemCall) {
		writeJSON(w, http.StatusOK, s.giteaIssueRecord(c.repo, c.it, c.pr))
	},
	"GET issues/{n}/labels": func(_ *Server, w http.ResponseWriter, _ *http.Request, c *itemCall) {
		writeJSON(w, http.StatusOK, giteaLabels(c.repo, c.it.Labels))
	},
	"PATCH pulls/{n}": func(s *Server, w http.ResponseWriter, r *http.Request, c *itemCall) {
		if giteaSetState(w, r, c.it) {
			writeJSON(w, http.StatusCreated, s.giteaPull(c.repo, c.it))
		}
	},
	"PATCH issues/{n}": func(s *Server, w http.ResponseWriter, r *http.Request, c *itemCall) {
		if giteaSetState(w, r, c.it) {
			writeJSON(w, http.StatusCreated, s.giteaIssueRecord(c.repo, c.it, c.pr))
		}
	},
	"POST pulls/{n}/merge": func(_ *Server, w http.ResponseWriter, r *http.Request, c *itemCall) {
		giteaMerge(w, r, c.repo, c.it)
	},
	"DELETE pulls/{n}/merge": func(_ *Server, w http.ResponseWriter, _ *http.Request, c *itemCall) {
		giteaCancel(w, c.it)
	},
	"POST issues/{n}/labels": func(_ *Server, w http.ResponseWriter, r *http.Request, c *itemCall) {
		giteaAddLabels(w, r, c.repo, c.it)
	},
	"DELETE issues/{n}/labels/{name}": func(s *Server, w http.ResponseWriter, _ *http.Request, c *itemCall) {
		s.giteaRemoveLabel(w, c.repo, c.it, c.args[1])
	},
}

func giteaNotFound(w http.ResponseWriter) bool {
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "The target couldn't be found."})
	return true
}

// run is run id of the repository, nil where it holds none.
func (r *Repo) run(id string) *Run {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil
	}
	for i := range r.Runs {
		if r.Runs[i].ID == n {
			return &r.Runs[i]
		}
	}
	return nil
}

// giteaLabelID is the id of the label name the repository defines, its
// place in Repo.Labels counted from 1, and 0 for a name it does not define.
func giteaLabelID(repo *Repo, name string) int {
	for i, l := range repo.Labels {
		if l == name {
			return i + 1
		}
	}
	return 0
}

// giteaLabels is names as label objects with the ids the repository gives
// them, each in one colour.
func giteaLabels(repo *Repo, names []string) []map[string]any {
	out := labelObjects(names)
	for i, n := range names {
		out[i]["id"] = giteaLabelID(repo, n)
	}
	return out
}

// giteaUser is an account object: Gitea names its type, Forgejo does not.
func (s *Server) giteaUser(login, kind string) map[string]any {
	user := map[string]any{"login": login}
	if s.product == Gitea {
		user["type"] = cmp.Or(kind, "User")
	}
	return user
}

func giteaState(it *Item) string {
	if it.open() {
		return "open"
	}
	return "closed"
}

// giteaMergeable is the record's mergeable flag, which both products clear
// for a conflict and for a work-in-progress pull request, whatever its
// checks.
func giteaMergeable(it *Item) bool { return !it.Conflicting && !it.Draft }

func (s *Server) giteaItemURL(repo *Repo, it *Item, pr bool) string {
	path := "/issues/"
	if pr {
		path = "/pulls/"
	}
	return webURL(s.URL, s.fullName(repo)+path+itoa(it.Number))
}

// giteaPull is a pull request's REST record.
func (s *Server) giteaPull(repo *Repo, it *Item) map[string]any {
	full := map[string]any{"full_name": s.fullName(repo)}
	return map[string]any{
		"number": it.Number, "state": giteaState(it), "title": it.Title, "body": it.Body,
		"user": s.giteaUser(it.Author, it.AuthorType), "html_url": s.giteaItemURL(repo, it, true),
		"created_at": stamp(it.Created), "updated_at": stamp(it.Updated), "draft": it.Draft,
		"merged": it.State == "merged", "mergeable": giteaMergeable(it), "labels": giteaLabels(repo, it.Labels),
		"comments": it.Comments, "review_comments": 0,
		"head": map[string]any{"ref": cmp.Or(it.SourceBranch, "feature"), "sha": it.HeadSHA, "repo": full},
		"base": map[string]any{"ref": repo.DefaultBranch, "repo": full},
	}
}

// giteaIssueRecord is an issue's REST record, which for a pull request
// carries the pull request object both products share the number space with.
func (s *Server) giteaIssueRecord(repo *Repo, it *Item, pr bool) map[string]any {
	row := map[string]any{
		"number": it.Number, "state": giteaState(it), "title": it.Title, "body": it.Body,
		"user": s.giteaUser(it.Author, it.AuthorType), "html_url": s.giteaItemURL(repo, it, pr),
		"created_at": stamp(it.Created), "updated_at": stamp(it.Updated), "labels": giteaLabels(repo, it.Labels),
		"comments": it.Comments,
	}
	if pr {
		row["pull_request"] = map[string]any{"draft": it.Draft, "merged": it.State == "merged", "html_url": s.giteaItemURL(repo, it, true)}
	}
	return row
}

func (s *Server) giteaPulls(w http.ResponseWriter, r *http.Request, repo *Repo) {
	var items []*Item
	for i := range repo.PRs {
		if it := &repo.PRs[i]; r.URL.Query().Get("state") != "open" || it.open() {
			items = append(items, it)
		}
	}
	lo, hi := s.giteaPage(r, len(items))
	rows := make([]map[string]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		rows = append(rows, s.giteaPull(repo, it))
	}
	writeJSON(w, http.StatusOK, rows)
}

// giteaReviews is one review per reviewer whose latest review approves or
// requests changes.
func giteaReviews(it *Item) []map[string]any {
	out := make([]map[string]any, 0, it.Approvals+it.ChangesRequested)
	for i := range it.Approvals + it.ChangesRequested {
		state := "APPROVED"
		if i >= it.Approvals {
			state = "REQUEST_CHANGES"
		}
		out = append(out, map[string]any{"id": i + 1, "user": map[string]any{"id": 100 + i}, "state": state, "dismissed": false, "stale": false})
	}
	return out
}

// giteaStatusState spells a check's state as a commit status.
func giteaStatusState(state string) string {
	if state == "neutral" {
		return "warning"
	}
	return state
}

// giteaStatus answers one page of the combined status of commit sha: one
// status per check, and none for a commit with no check.
func (s *Server) giteaStatus(w http.ResponseWriter, r *http.Request, repo *Repo, sha string) {
	checks := repo.Checks[sha]
	lo, hi := s.giteaPage(r, len(checks))
	statuses := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		statuses = append(statuses, map[string]any{
			"context": "ci/" + itoa(i+1), "description": "", "target_url": "", "status": giteaStatusState(checks[i]),
		})
	}
	state, _ := repo.combined(sha)
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "state": state, "total_count": len(checks), "statuses": statuses})
}

// giteaRunStatus spells an outcome: Gitea's completed run carries its
// conclusion beside status completed, Forgejo's status names the outcome.
func (s *Server) giteaRunStatus(outcome string) (status, conclusion string) {
	switch {
	case s.product == Forgejo && outcome == "pending":
		return "running", ""
	case s.product == Forgejo && outcome == "approval":
		return "blocked", ""
	case s.product == Forgejo:
		return outcome, ""
	case outcome == "pending":
		return "in_progress", ""
	case outcome == "approval":
		return "waiting", ""
	}
	return "completed", outcome
}

// giteaRuns answers one page of the repository's runs, highest id first,
// filtered to a head_sha where one is asked, with the filtered total.
func (s *Server) giteaRuns(w http.ResponseWriter, r *http.Request, repo *Repo) {
	runs := runsHighestIDFirst(repo, time.Time{})
	if head := r.URL.Query().Get("head_sha"); head != "" {
		kept := runs[:0]
		for i := range runs {
			if runs[i].HeadSHA == head {
				kept = append(kept, runs[i])
			}
		}
		runs = kept
	}
	lo, hi := s.giteaPage(r, len(runs))
	rows := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		rows = append(rows, s.giteaRun(repo, &runs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "workflow_runs": rows})
}

// giteaRun is a run in its product's spelling: Gitea's GitHub-shaped
// workflow run, or Forgejo's action run.
func (s *Server) giteaRun(repo *Repo, run *Run) map[string]any {
	status, conclusion := s.giteaRunStatus(run.Outcome)
	row := map[string]any{
		"id": run.ID, "status": status, "html_url": webURL(s.URL, s.fullName(repo)+"/actions/runs/"+itoa(int(run.ID))),
		"repository": map[string]any{"full_name": s.fullName(repo)},
	}
	if s.product == Forgejo {
		row["index_in_repo"], row["workflow_id"], row["prettyref"], row["title"] = run.Number, run.Workflow+".yml", run.Branch, run.Title
		row["trigger_event"], row["commit_sha"] = run.Event, run.HeadSHA
		row["created"], row["started"], row["updated"] = stamp(run.Created), stamp(run.Started), stamp(run.Updated)
		return row
	}
	row["conclusion"] = conclusion
	row["run_number"], row["path"], row["head_branch"] = run.Number, ".gitea/workflows/"+run.Workflow+".yml@refs/heads/"+run.Branch, run.Branch
	row["event"], row["head_sha"], row["display_title"] = run.Event, run.HeadSHA, run.Title
	row["created_at"], row["started_at"], row["updated_at"] = stamp(run.Created), stamp(run.Started), stamp(run.Updated)
	return row
}
