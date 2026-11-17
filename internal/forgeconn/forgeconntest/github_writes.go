//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// githubWriteRoute answers the routes forgeapi's writes send on GitHub, and
// the pull request record they read around them, applying each write to the
// World.
func (s *Server) githubWriteRoute(w http.ResponseWriter, r *http.Request, repo *Repo, pattern string, args []string) bool {
	switch pattern {
	case "GET pulls/{n}":
		if it := repo.item(args[0], true); it != nil {
			writeJSON(w, http.StatusOK, s.githubPullRecord(repo, it))
			return true
		}
	case "PATCH pulls/{n}":
		if it := repo.item(args[0], true); it != nil {
			s.githubSetPullState(w, r, repo, it)
			return true
		}
	case "PATCH issues/{n}":
		if it, pr := repo.issueOrPR(args[0]); it != nil {
			s.githubSetIssueState(w, r, repo, it, pr)
			return true
		}
	case "PUT pulls/{n}/merge-async":
		if it := repo.item(args[0], true); it != nil {
			s.githubMerge(w, r, repo, it)
			return true
		}
	case "POST issues/{n}/labels":
		if it, _ := repo.issueOrPR(args[0]); it != nil {
			githubAddLabels(w, r, it)
			return true
		}
	case "DELETE issues/{n}/labels/{name}":
		if it, _ := repo.issueOrPR(args[0]); it != nil {
			githubRemoveLabel(w, it, args[1])
			return true
		}
	case "POST actions/runs/{id}/rerun-failed-jobs":
		return githubRerun(w, repo, args[0])
	default:
		return false
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	return true
}

// decodeBody reads a write's JSON body into v.
func decodeBody(r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return err == nil && json.Unmarshal(body, v) == nil
}

func nodeID(repo *Repo, number int) string { return fmt.Sprintf("PR_%s_%d", repo.Name, number) }

// githubMergeState is the REST record's mergeable_state of a pull request,
// as a repository whose checks are required answers it: draft, dirty with a
// conflict, blocked while a check fails or runs, clean otherwise.
func (r *Repo) githubMergeState(it *Item) string {
	switch state, _ := r.combined(it.HeadSHA); {
	case it.Draft:
		return "draft"
	case it.Conflicting:
		return "dirty"
	case state == "failure" || state == "pending":
		return "blocked"
	}
	return "clean"
}

func (s *Server) githubPullRecord(repo *Repo, it *Item) map[string]any {
	state := "open"
	if !it.open() {
		state = "closed"
	}
	var autoMerge any
	if it.AutoMerge {
		autoMerge = map[string]any{"enabled_by": map[string]any{"login": s.world.Login}, "merge_method": "squash"}
	}
	mergeState := repo.githubMergeState(it)
	return map[string]any{
		"number": it.Number, "node_id": nodeID(repo, it.Number), "state": state, "title": it.Title, "body": it.Body,
		"user":       map[string]any{"login": it.Author, "type": cmp.Or(it.AuthorType, "User")},
		"html_url":   webURL(s.URL, s.fullName(repo)+"/pull/"+itoa(it.Number)),
		"created_at": stamp(it.Created), "updated_at": stamp(it.Updated), "draft": it.Draft,
		"labels": labelObjects(it.Labels), "comments": it.Comments, "review_comments": 0,
		"head": map[string]any{
			"ref": cmp.Or(it.SourceBranch, "feature"), "sha": it.HeadSHA, "repo": map[string]any{"full_name": s.fullName(repo)},
		},
		"base":   map[string]any{"ref": repo.DefaultBranch},
		"merged": it.State == "merged", "mergeable": mergeState != "dirty", "mergeable_state": mergeState,
		"auto_merge": autoMerge,
	}
}

// githubSetPullState applies a pull request's state change. A merged pull
// request keeps its state, refused as GitHub refuses it.
func (s *Server) githubSetPullState(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item) {
	var body struct {
		State string `json:"state"`
	}
	if !decodeBody(r, &body) || (body.State != "open" && body.State != "closed") || it.State == "merged" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
		return
	}
	it.State, it.Updated = body.State, later(it.Updated)
	writeJSON(w, http.StatusOK, s.githubPullRecord(repo, it))
}

func (s *Server) githubSetIssueState(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item, pr bool) {
	var body struct {
		State string `json:"state"`
	}
	if !decodeBody(r, &body) || (body.State != "open" && body.State != "closed") || it.State == "merged" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
		return
	}
	it.State, it.Updated = body.State, later(it.Updated)
	writeJSON(w, http.StatusOK, s.githubIssueRecord(repo, it, pr))
}

// githubMerge answers the asynchronous merge: 400 for a closed or draft pull
// request or a pin naming another head, 405 for a method the repository
// turns off or a state that cannot merge now, otherwise the pending answer,
// with the merge made at once.
func (s *Server) githubMerge(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item) {
	var body struct {
		Method string `json:"merge_method"`
		SHA    string `json:"sha"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
		return
	}
	switch {
	case body.SHA != it.HeadSHA:
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Head branch was modified. Review and try the merge again."})
	case !it.open() || it.Draft:
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Pull Request is not open."})
	case body.Method == "rebase":
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Rebase merges are not allowed on this repository."})
	case repo.githubMergeState(it) != "clean":
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Pull Request is not mergeable"})
	default:
		it.State, it.AutoMerge, it.Updated = "merged", false, later(it.Updated)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "pending",
			"details": map[string]any{
				"message": "Merge request enqueued.", "uuid": "00000000-0000-4000-8000-" + fmt.Sprintf("%012d", it.Number),
				"merge_method": cmp.Or(body.Method, "merge"), "merge_action": "default", "expected_head_sha": body.SHA, "bypass_rules": false,
			},
		})
	}
}

func githubAddLabels(w http.ResponseWriter, r *http.Request, it *Item) {
	var body struct {
		Labels []string `json:"labels"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
		return
	}
	for _, name := range body.Labels {
		if !slices.Contains(it.Labels, name) {
			it.Labels = append(it.Labels, name)
		}
	}
	it.Updated = later(it.Updated)
	writeJSON(w, http.StatusOK, labelObjects(it.Labels))
}

// githubRemoveLabel takes one label off an item, answering 404 for a label
// the item does not carry, as GitHub does.
func githubRemoveLabel(w http.ResponseWriter, it *Item, name string) {
	i := slices.Index(it.Labels, name)
	if i < 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Label does not exist"})
		return
	}
	it.Labels = slices.Delete(it.Labels, i, i+1)
	it.Updated = later(it.Updated)
	writeJSON(w, http.StatusOK, labelObjects(it.Labels))
}

// githubRerun starts run id again: it is pending until it ends.
func githubRerun(w http.ResponseWriter, repo *Repo, id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false
	}
	for i := range repo.Runs {
		if run := &repo.Runs[i]; run.ID == n {
			run.Outcome, run.Updated = "pending", later(run.Updated)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
			return true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	return true
}

// githubMutation answers the two GraphQL mutations forgeapi sends, which
// carry no rateLimit selection, reporting whether op is one of them.
func (s *Server) githubMutation(op string, q *docQuery) (map[string]any, bool) {
	var field string
	switch op {
	case "EnableAutoMerge":
		field = "enablePullRequestAutoMerge"
	case "DisableAutoMerge":
		field = "disablePullRequestAutoMerge"
	default:
		return nil, false
	}
	id := q.str("pullRequestId")
	repo, it := s.byNodeID(id)
	if it == nil {
		return refusedMutation(field, "NOT_FOUND", "Could not resolve to a node with the global id of '"+id+"'"), true
	}
	if op == "DisableAutoMerge" {
		it.AutoMerge, it.Updated = false, later(it.Updated)
		return map[string]any{"data": map[string]any{field: map[string]any{
			"pullRequest": map[string]any{"number": it.Number, "autoMergeRequest": nil},
		}}}, true
	}
	if q.str("expectedHeadOid") != it.HeadSHA {
		return refusedMutation(field, "UNPROCESSABLE", "The expected head oid does not match the pull request's head."), true
	}
	it.AutoMerge, it.Updated = true, later(it.Updated)
	return map[string]any{"data": map[string]any{field: map[string]any{
		"pullRequest": map[string]any{
			"number": it.Number, "mergeStateStatus": strings.ToUpper(repo.githubMergeState(it)),
			"autoMergeRequest": map[string]any{"enabledAt": stamp(it.Updated), "mergeMethod": q.str("mergeMethod")},
		},
	}}}, true
}

func refusedMutation(field, kind, message string) map[string]any {
	return map[string]any{
		"data":   map[string]any{field: nil},
		"errors": []map[string]any{{"type": kind, "path": []string{field}, "message": message}},
	}
}

func (s *Server) byNodeID(id string) (*Repo, *Item) {
	for i := range s.world.Repos {
		repo := &s.world.Repos[i]
		for j := range repo.PRs {
			if nodeID(repo, repo.PRs[j].Number) == id {
				return repo, &repo.PRs[j]
			}
		}
	}
	return nil, nil
}
