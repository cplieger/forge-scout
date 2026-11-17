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

const glREST = "/api/v4"

// glVersion is the version a GitLab fake reports when World.Version is empty.
const glVersion = "18.4.0"

// gitlabStatus spells a run outcome as a pipeline status.
func gitlabStatus(outcome string) string {
	switch outcome {
	case "failure":
		return "failed"
	case "cancelled":
		return "canceled"
	case "pending":
		return "running"
	case "approval":
		return "manual"
	default:
		return outcome
	}
}

// gitlabSource spells a run's event as a pipeline source.
func gitlabSource(event string) string {
	switch event {
	case "pull_request":
		return "merge_request_event"
	case "workflow_dispatch":
		return "web"
	default:
		return event
	}
}

func (s *Server) serveGitLab(w http.ResponseWriter, r *http.Request, line string) (string, bool) {
	h := w.Header()
	h.Set("X-Gitlab-Meta", `{"correlation_id":"fake-correlation","version":"1"}`)
	remaining, reset := s.budget()
	h.Set("RateLimit-Name", "throttle_authenticated_api")
	h.Set("RateLimit-Limit", "2000")
	h.Set("RateLimit-Remaining", itoa(remaining))
	h.Set("RateLimit-Reset", itoa(int(reset.Unix())))
	if limit := s.projectsThrottle; limit > 0 && strings.HasSuffix(r.URL.Path, "/projects") {
		h.Set("RateLimit-Name", "burst_traffic_per_user_free_plan")
		h.Set("RateLimit-Limit", itoa(limit))
		h.Set("RateLimit-Remaining", itoa(limit-1))
	}
	if r.Method == http.MethodPost && r.URL.Path == graphQLPath {
		return line, s.serveGitLabGraphQL(w, r)
	}
	escaped := r.URL.EscapedPath()
	path, ok := strings.CutPrefix(escaped, glREST)
	if !ok {
		return line, false
	}
	if r.Method == http.MethodGet {
		switch path {
		case "/metadata":
			writeJSON(w, http.StatusOK, map[string]any{"version": s.gitlabVersion(), "enterprise": false, "revision": "fake"})
			return line, true
		case "/user":
			writeJSON(w, http.StatusOK, map[string]any{"username": s.world.Login, "name": s.world.Login, "web_url": webURL(s.URL, s.world.Login)})
			return line, true
		}
		if handled, named := s.gitlabNamespace(w, r, path); named {
			return line, handled
		}
	}
	rest, ok := strings.CutPrefix(path, "/projects/")
	if !ok {
		return line, false
	}
	encoded, tail, _ := strings.Cut(rest, "/")
	repo, ok := s.gitlabRepo(encoded)
	if !ok {
		return line, false
	}
	return line, s.gitlabProjectRoute(w, r, repo, tail)
}

func (s *Server) gitlabVersion() string { return cmp.Or(s.world.Version, glVersion) }

// gitlabRepo is the repository a project route segment names, by its
// escaped full path or by its numeric id, as GitLab takes either.
func (s *Server) gitlabRepo(segment string) (*Repo, bool) {
	if id, err := strconv.Atoi(segment); err == nil {
		for i := range s.world.Repos {
			if gitlabProjectID(i) == id {
				return &s.world.Repos[i], true
			}
		}
		return nil, false
	}
	full, err := url.PathUnescape(segment)
	if err != nil {
		return nil, false
	}
	name, ok := cutOwner(full, s.world.Owner)
	if !ok {
		return nil, false
	}
	return s.repo(name)
}

// cutOwner is a full path's repository name under owner, matched as GitLab
// matches paths, without regard to case.
func cutOwner(full, owner string) (string, bool) {
	prefix := owner + "/"
	if len(full) <= len(prefix) || !strings.EqualFold(full[:len(prefix)], prefix) {
		return "", false
	}
	return full[len(prefix):], true
}

// gitlabProjectID is the numeric id of the repository at index i.
func gitlabProjectID(i int) int { return 1001 + i }

func (s *Server) projectID(repo *Repo) int {
	for i := range s.world.Repos {
		if &s.world.Repos[i] == repo {
			return gitlabProjectID(i)
		}
	}
	panic("forgeconntest: repository outside the world")
}

func projectGID(id int) string { return "gid://gitlab/Project/" + itoa(id) }

// gitlabNamespace serves the user and group routes, answering 404 for a
// namespace other than the owner's as GitLab answers one that does not
// exist; ok reports a namespace route.
func (s *Server) gitlabNamespace(w http.ResponseWriter, r *http.Request, path string) (handled, ok bool) {
	if rest, found := strings.CutPrefix(path, "/users/"); found {
		id, tail, _ := strings.Cut(rest, "/")
		if tail != "projects" {
			return false, false
		}
		if !s.world.UserNamespace || !s.addresses(id, glUserID) {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 User Not Found"})
			return true, true
		}
		return s.gitlabGroup(w, r, "projects"), true
	}
	rest, found := strings.CutPrefix(path, "/groups/")
	if !found {
		return false, false
	}
	id, tail, _ := strings.Cut(rest, "/")
	if s.world.UserNamespace || !s.addresses(id, glGroupID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Group Not Found"})
		return true, true
	}
	return s.gitlabGroup(w, r, tail), true
}

// The ids the owner's group and user answer to. A route reads an owner named
// by digits alone as an id, so forgeapi resolves such a name to one first.
const (
	glGroupID = "4242"
	glUserID  = "77"
)

// addresses reports a route segment naming the owner: its id when the owner
// is digits alone, else its escaped name.
func (s *Server) addresses(segment, id string) bool {
	if strings.Trim(s.world.Owner, "0123456789") == "" {
		return segment == id
	}
	return segment == url.PathEscape(s.world.Owner)
}

// gitlabGroup answers one page of the owner's projects, or of the open merge
// requests or issues across them.
func (s *Server) gitlabGroup(w http.ResponseWriter, r *http.Request, collection string) bool {
	var rows []map[string]any
	switch collection {
	case "projects":
		for i := range s.world.Repos {
			rows = append(rows, s.gitlabProjectRecord(&s.world.Repos[i]))
		}
	case "merge_requests", "issues":
		mr := collection == "merge_requests"
		for i := range s.world.Repos {
			repo := &s.world.Repos[i]
			for _, hit := range openHits(repo, mr) {
				rows = append(rows, s.gitlabRecord(repo, hit.item, mr, true))
			}
		}
	default:
		return false
	}
	lo, hi := s.gitlabPage(w, r, len(rows))
	writeJSON(w, http.StatusOK, append([]map[string]any{}, rows[lo:hi]...))
	return true
}

// gitlabPage is the rows of a page-numbered list request, with GitLab's
// next-page header naming the page that follows, empty on the last.
func (s *Server) gitlabPage(w http.ResponseWriter, r *http.Request, n int) (lo, hi int) {
	size, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil || size < 1 {
		size = 20
	}
	if s.world.PageSize > 0 {
		size = min(size, s.world.PageSize)
	}
	page := pageOf(r)
	lo, hi, more := s.window((page-1)*size, size, n)
	next := ""
	if more {
		next = itoa(page + 1)
	}
	w.Header().Set("X-Next-Page", next)
	return lo, hi
}

// gitlabRoute is a project route's pattern, the item number and pipeline id
// segments standing as {n} and {id}, with those values.
func gitlabRoute(method, tail string) (pattern string, args []string) {
	parts := strings.Split(tail, "/")
	if len(parts) >= 2 {
		switch parts[0] {
		case "merge_requests", "issues":
			parts[1], args = "{n}", append(args, parts[1])
		case "pipelines":
			parts[1], args = "{id}", append(args, parts[1])
		}
	}
	return method + " " + strings.Join(parts, "/"), args
}

// gitlabProjectRoute answers one route under a project, reporting whether it
// serves it.
func (s *Server) gitlabProjectRoute(w http.ResponseWriter, r *http.Request, repo *Repo, tail string) bool {
	pattern, args := gitlabRoute(r.Method, tail)
	switch pattern {
	case "GET ":
		writeJSON(w, http.StatusOK, s.gitlabProjectRecord(repo))
	case "GET labels":
		lo, hi := s.gitlabPage(w, r, len(repo.Labels))
		writeJSON(w, http.StatusOK, gitlabLabels(repo.Labels[lo:hi]))
	case "GET pipelines":
		s.gitlabPipelines(w, r, repo)
	case "GET merge_requests":
		s.gitlabMergeRequests(w, r, repo)
	case "GET merge_requests/{n}":
		it := repo.item(args[0], true)
		if it == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return true
		}
		record := s.gitlabRecord(repo, it, true, false)
		record["head_pipeline"] = s.gitlabHeadPipeline(repo, it)
		writeJSON(w, http.StatusOK, record)
	case "GET issues/{n}":
		it := repo.item(args[0], false)
		if it == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return true
		}
		writeJSON(w, http.StatusOK, s.gitlabRecord(repo, it, false, false))
	default:
		return s.gitlabWriteRoute(w, r, repo, pattern, args)
	}
	return true
}

func (s *Server) gitlabProjectRecord(repo *Repo) map[string]any {
	visibility := "public"
	if repo.Private {
		visibility = "private"
	}
	row := map[string]any{
		"id": s.projectID(repo), "path_with_namespace": s.fullName(repo), "description": "",
		"web_url": webURL(s.URL, s.fullName(repo)), "http_url_to_repo": webURL(s.URL, s.fullName(repo)+".git"),
		"last_activity_at": stamp(time.Now()), "visibility": visibility,
		"default_branch": repo.DefaultBranch, "issues_enabled": true, "issues_access_level": "enabled",
		"permissions":  map[string]any{"project_access": map[string]any{"access_level": 40}, "group_access": nil},
		"merge_method": "merge", "squash_option": "default_off", "merge_trains_enabled": false, "archived": repo.Archived,
	}
	if repo.Fork {
		row["forked_from_project"] = map[string]any{"path_with_namespace": "upstream/" + repo.Name}
	}
	return row
}

// gitlabLabels is names as GitLab's label objects, each in one colour.
func gitlabLabels(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "color": "#ededed", "description": ""})
	}
	return out
}

// gitlabHeadRun is the pipeline GitLab matches to a merge request's head:
// the newest run on that commit, nil where none ran on it.
func (r *Repo) gitlabHeadRun(it *Item) *Run {
	var head *Run
	for i := range r.Runs {
		if run := &r.Runs[i]; it.HeadSHA != "" && run.HeadSHA == it.HeadSHA && (head == nil || run.ID > head.ID) {
			head = run
		}
	}
	return head
}

// gitlabMergeStatus is a merge request's detailed merge status, as a project
// that requires a pipeline to succeed answers it.
func (r *Repo) gitlabMergeStatus(it *Item) string {
	switch {
	case !it.open():
		return "not_open"
	case it.Draft:
		return "draft_status"
	case it.Conflicting:
		return "conflict"
	}
	if run := r.gitlabHeadRun(it); run != nil {
		switch run.Outcome {
		case "success":
		case "pending":
			return "ci_still_running"
		default:
			return "ci_must_pass"
		}
	}
	return "mergeable"
}

func gitlabState(it *Item, mr bool) string {
	switch {
	case it.open():
		return "opened"
	case it.State == "merged" && mr:
		return "merged"
	}
	return "closed"
}

// gitlabRecord is the REST record of a merge request or an issue, its labels
// as objects where the read asked for their details and as names otherwise,
// as GitLab answers a write.
func (s *Server) gitlabRecord(repo *Repo, it *Item, mr, details bool) map[string]any {
	sigil, path := "#", "/-/issues/"
	if mr {
		sigil, path = "!", "/-/merge_requests/"
	}
	var labels any = append([]string{}, it.Labels...)
	if details {
		labels = gitlabLabels(it.Labels)
	}
	id := s.projectID(repo)
	row := map[string]any{
		"iid": it.Number, "project_id": id, "state": gitlabState(it, mr), "title": it.Title, "description": it.Body,
		"author": map[string]any{"username": it.Author}, "web_url": webURL(s.URL, s.fullName(repo)+path+itoa(it.Number)),
		"created_at": stamp(it.Created), "updated_at": stamp(it.Updated), "labels": labels, "user_notes_count": it.Comments,
		"references": map[string]any{"full": s.fullName(repo) + sigil + itoa(it.Number)},
	}
	if !mr {
		return row
	}
	var mergedAt any
	if it.State == "merged" {
		mergedAt = stamp(it.Updated)
	}
	detailed := repo.gitlabMergeStatus(it)
	older := "can_be_merged"
	if detailed == "conflict" {
		older = "cannot_be_merged"
	}
	row["draft"] = it.Draft
	row["sha"] = it.HeadSHA
	row["source_branch"] = cmp.Or(it.SourceBranch, "feature")
	row["target_branch"] = repo.DefaultBranch
	row["source_project_id"], row["target_project_id"] = id, id
	row["merge_status"], row["detailed_merge_status"] = older, detailed
	row["merged_at"] = mergedAt
	row["merge_when_pipeline_succeeds"] = it.AutoMerge
	return row
}

// gitlabHeadPipeline is the REST record's head pipeline, null where none ran
// on the head.
func (s *Server) gitlabHeadPipeline(repo *Repo, it *Item) any {
	run := repo.gitlabHeadRun(it)
	if run == nil {
		return nil
	}
	return map[string]any{"id": run.ID, "sha": run.HeadSHA, "status": gitlabStatus(run.Outcome), "project_id": s.projectID(repo)}
}

// gitlabMergeRequests answers a project's merge request listing, which
// forgeapi reads only where the instance refuses its documents.
func (s *Server) gitlabMergeRequests(w http.ResponseWriter, r *http.Request, repo *Repo) {
	var items []*Item
	for i := range repo.PRs {
		if it := &repo.PRs[i]; r.URL.Query().Get("state") != "opened" || it.open() {
			items = append(items, it)
		}
	}
	lo, hi := s.gitlabPage(w, r, len(items))
	rows := make([]map[string]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		rows = append(rows, s.gitlabRecord(repo, it, true, r.URL.Query().Get("with_labels_details") == "true"))
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) gitlabPipelines(w http.ResponseWriter, r *http.Request, repo *Repo) {
	after := since(r, "created_after")
	all := runsHighestIDFirst(repo, time.Time{})
	runs := all[:0]
	for i := range all {
		if after.IsZero() || all[i].Created.After(after) {
			runs = append(runs, all[i])
		}
	}
	lo, hi := s.gitlabPage(w, r, len(runs))
	rows := make([]map[string]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		rows = append(rows, s.gitlabPipeline(repo, &runs[i]))
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) gitlabPipeline(repo *Repo, run *Run) map[string]any {
	return map[string]any{
		"id": run.ID, "iid": run.Number, "project_id": s.projectID(repo), "name": run.Title, "ref": run.Branch,
		"sha": run.HeadSHA, "status": gitlabStatus(run.Outcome), "source": gitlabSource(run.Event),
		"created_at": stamp(run.Created), "updated_at": stamp(run.Updated),
		"web_url": webURL(s.URL, s.fullName(repo)+"/-/pipelines/"+itoa(int(run.ID))),
	}
}

// serveGitLabGraphQL answers the documents forgeapi sends. With
// World.RefuseDocuments every document but the owner lookup is refused, as
// an instance whose schema lacks a field they select refuses them, and
// forgeapi reads REST for the connection's life.
func (s *Server) serveGitLabGraphQL(w http.ResponseWriter, r *http.Request) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req graphQLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	q := docQuery{vars: req.Variables}
	if req.OperationName == "OwnerLookup" {
		writeJSON(w, http.StatusOK, map[string]any{"data": s.gitlabOwnerLookup(&q)})
		return true
	}
	switch req.OperationName {
	case "DocumentAcceptance", "PRList", "PRRead", "RerunHead":
	default:
		return false
	}
	if s.world.RefuseDocuments {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []any{map[string]any{
			"message": "Field 'autoMergeEnabled' doesn't exist on type 'MergeRequest'", "extensions": map[string]any{"code": "undefinedField"},
		}}})
		return true
	}
	data := map[string]any{"project": nil, "queryComplexity": map[string]any{"score": s.cost, "limit": 200}}
	var errs []any
	if repo, ok := s.gitlabDocumentRepo(&q); ok && req.OperationName != "DocumentAcceptance" {
		data["project"], errs = s.gitlabDocumentProject(repo, req.OperationName, &q)
	}
	out := map[string]any{"data": data}
	if errs != nil {
		out["errors"] = errs
	}
	writeJSON(w, http.StatusOK, out)
	return true
}

func (s *Server) gitlabOwnerLookup(q *docQuery) map[string]any {
	var group, user any
	if q.str("fullPath") == s.world.Owner && !s.world.UserNamespace {
		group = map[string]any{"id": "gid://gitlab/Group/" + glGroupID}
	}
	if q.str("username") == s.world.Owner && s.world.UserNamespace {
		user = map[string]any{"id": "gid://gitlab/User/" + glUserID}
	}
	return map[string]any{"group": group, "user": user, "queryComplexity": map[string]any{"score": 3, "limit": 200}}
}

// gitlabDocumentRepo is the repository a document's fullPath names.
func (s *Server) gitlabDocumentRepo(q *docQuery) (*Repo, bool) {
	name, ok := cutOwner(q.str("fullPath"), s.world.Owner)
	if !ok {
		return nil, false
	}
	return s.repo(name)
}

// gitlabDocumentProject is the project selection of the merge request list,
// the single read or the re-run's head read, with the error GitLab answers
// beside a list it cut where World.GraphQLErrors is set.
func (s *Server) gitlabDocumentProject(repo *Repo, op string, q *docQuery) (project map[string]any, errs []any) {
	id := s.projectID(repo)
	project = map[string]any{
		"id": projectGID(id), "fullPath": s.fullName(repo),
		"userPermissions": map[string]any{"pushCode": true, "readMergeRequest": true},
	}
	if op != "PRList" {
		project["mergeRequest"] = nil
		if it := repo.item(q.str("iid"), true); it != nil {
			project["mergeRequest"] = s.gitlabNode(repo, it, op == "PRRead", q.on("reviews") && op == "PRRead")
		}
		return project, nil
	}
	var open []*Item
	for i := range repo.PRs {
		if q.str("state") != "opened" || repo.PRs[i].open() {
			open = append(open, &repo.PRs[i])
		}
	}
	lo, hi, more := s.window(q.offset(), q.num("first"), len(open))
	nodes := make([]map[string]any, 0, hi-lo)
	for _, it := range open[lo:hi] {
		nodes = append(nodes, s.gitlabNode(repo, it, q.on("mergeState"), q.on("reviews")))
	}
	project["mergeRequests"] = map[string]any{"pageInfo": pageInfo(hi, more), "nodes": nodes}
	if s.world.GraphQLErrors {
		errs = []any{map[string]any{"message": "Internal server error", "path": []string{"project", "mergeRequests"}}}
	}
	return project, errs
}

// gitlabNode is the merge request selection GitLab's documents share, with
// the merge state and review fields their include directives leave in.
func (s *Server) gitlabNode(repo *Repo, it *Item, mergeState, reviews bool) map[string]any {
	var mergedAt any
	if it.State == "merged" {
		mergedAt = stamp(it.Updated)
	}
	var pipeline any
	if run := repo.gitlabHeadRun(it); run != nil {
		pipeline = map[string]any{
			"id":     "gid://gitlab/Ci::Pipeline/" + itoa(int(run.ID)),
			"status": strings.ToUpper(gitlabStatus(run.Outcome)), "project": map[string]any{"id": projectGID(s.projectID(repo))},
		}
	}
	labels := make([]map[string]any, 0, len(it.Labels))
	for _, l := range gitlabLabels(it.Labels) {
		labels = append(labels, map[string]any{"title": l["name"], "color": l["color"], "description": l["description"]})
	}
	node := map[string]any{
		"iid": itoa(it.Number), "title": it.Title, "description": it.Body,
		"author":       map[string]any{"username": it.Author, "bot": it.AuthorType == "Bot"},
		"sourceBranch": cmp.Or(it.SourceBranch, "feature"), "sourceProject": map[string]any{"fullPath": s.fullName(repo)},
		"targetBranch": repo.DefaultBranch, "webUrl": webURL(s.URL, s.fullName(repo)+"/-/merge_requests/"+itoa(it.Number)),
		"diffHeadSha": it.HeadSHA, "createdAt": stamp(it.Created), "updatedAt": stamp(it.Updated),
		"state": gitlabState(it, true), "draft": it.Draft, "mergedAt": mergedAt, "autoMergeEnabled": it.AutoMerge,
		"labels": map[string]any{
			"count": len(it.Labels), "pageInfo": map[string]any{"hasNextPage": false}, "nodes": labels,
		},
		"userNotesCount": it.Comments, "headPipeline": pipeline,
	}
	if mergeState {
		detailed := repo.gitlabMergeStatus(it)
		node["mergeable"], node["detailedMergeStatus"] = detailed == "mergeable", strings.ToUpper(detailed)
	}
	if reviews {
		reviewers := make([]any, 0, it.Approvals+it.ChangesRequested)
		for range it.Approvals {
			reviewers = append(reviewers, map[string]any{"mergeRequestInteraction": map[string]any{"reviewState": "APPROVED"}})
		}
		for range it.ChangesRequested {
			reviewers = append(reviewers, map[string]any{"mergeRequestInteraction": map[string]any{"reviewState": "REQUESTED_CHANGES"}})
		}
		node["approvedBy"] = map[string]any{"count": it.Approvals}
		node["reviewers"] = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": reviewers}
	}
	return node
}
