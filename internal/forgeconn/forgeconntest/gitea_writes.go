//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"net/http"
	"slices"
	"strconv"
)

// giteaCancel disarms an armed merge, 204, and answers 404 where none is
// armed, as both products answer a pull request that does not exist.
func giteaCancel(w http.ResponseWriter, it *Item) {
	if !it.AutoMerge {
		giteaNotFound(w)
		return
	}
	it.AutoMerge, it.Updated = false, later(it.Updated)
	w.WriteHeader(http.StatusNoContent)
}

// giteaSetState applies a state edit, answering 412 for a merged pull
// request, whose state both products refuse to change; it reports whether
// the caller answers the record.
func giteaSetState(w http.ResponseWriter, r *http.Request, it *Item) bool {
	var body struct {
		State string `json:"state"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "invalid body"})
		return false
	}
	if it.State == "merged" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"message": "cannot change state of this pull request, it was already merged"})
		return false
	}
	switch body.State {
	case "closed":
		it.State = "closed"
	case "open":
		it.State = "open"
	}
	it.Updated = later(it.Updated)
	return true
}

// giteaChecksPass reports whether the checks on the pull request's head
// allow a merge now: every one passed, or there is none.
func (r *Repo) giteaChecksPass(it *Item) bool {
	state, ok := r.combined(it.HeadSHA)
	return !ok || state == "success"
}

// giteaMerge answers the merge as both products order their checks on a
// repository that allows merge and squash and requires its checks: 405 for
// a pull request merged already, a work in progress, a conflict, a style the
// repository does not allow or, unless the merge is asked to wait, checks
// not passed; 409 for a head pin that moved or a merge already scheduled.
// A merge asked to wait is scheduled, 201; any other merges, 200.
func giteaMerge(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item) {
	var body struct {
		Do   string `json:"Do"`
		Head string `json:"head_commit_id"`
		Wait bool   `json:"merge_when_checks_succeed"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "invalid body"})
		return
	}
	switch {
	case it.State == "closed":
		giteaNotFound(w)
		return
	case it.State == "merged", it.Draft, it.Conflicting, body.Do != "merge" && body.Do != "squash", !body.Wait && !repo.giteaChecksPass(it):
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Please try again later"})
		return
	case body.Head != it.HeadSHA:
		writeJSON(w, http.StatusConflict, map[string]any{"message": "head out of date"})
		return
	case body.Wait && it.AutoMerge:
		writeJSON(w, http.StatusConflict, map[string]any{"message": "pull request is already scheduled to auto merge when checks succeed"})
		return
	case body.Wait:
		it.AutoMerge, it.Updated = true, later(it.Updated)
		w.WriteHeader(http.StatusCreated)
		return
	}
	it.State, it.AutoMerge, it.Updated = "merged", false, later(it.Updated)
	w.WriteHeader(http.StatusOK)
}

// giteaAddLabels puts the labels of the ids sent on an item, answering 422
// for an id the repository does not define.
func giteaAddLabels(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item) {
	var body struct {
		Labels []int `json:"labels"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "invalid body"})
		return
	}
	for _, id := range body.Labels {
		if id < 1 || id > len(repo.Labels) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "label does not exist [id: " + itoa(id) + "]"})
			return
		}
	}
	for _, id := range body.Labels {
		if name := repo.Labels[id-1]; !slices.Contains(it.Labels, name) {
			it.Labels = append(it.Labels, name)
		}
	}
	it.Updated = later(it.Updated)
	writeJSON(w, http.StatusOK, giteaLabels(repo, it.Labels))
}

// giteaRemoveLabel takes one label off an item: Gitea names it by id,
// Forgejo by name or id. It answers 404 for a label the repository does not
// define and 422 for one the item does not carry.
func (s *Server) giteaRemoveLabel(w http.ResponseWriter, repo *Repo, it *Item, segment string) {
	name := ""
	if id, err := strconv.Atoi(segment); err == nil && id >= 1 && id <= len(repo.Labels) {
		name = repo.Labels[id-1]
	} else if s.product == Forgejo && giteaLabelID(repo, segment) != 0 {
		name = segment
	}
	if name == "" {
		giteaNotFound(w)
		return
	}
	i := slices.Index(it.Labels, name)
	if i < 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "label not attached"})
		return
	}
	it.Labels = slices.Delete(it.Labels, i, i+1)
	it.Updated = later(it.Updated)
	w.WriteHeader(http.StatusNoContent)
}

// giteaRerun starts run id again, pending until it ends: Gitea's re-run of
// the failed jobs answers 201, Forgejo's of the whole run 204.
func (s *Server) giteaRerun(w http.ResponseWriter, repo *Repo, id string) bool {
	run := repo.run(id)
	if run == nil {
		return giteaNotFound(w)
	}
	run.Outcome, run.Updated = "pending", later(run.Updated)
	if s.product == Forgejo {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	writeJSON(w, http.StatusCreated, map[string]any{})
	return true
}
