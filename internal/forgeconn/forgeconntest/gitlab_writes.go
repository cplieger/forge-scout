//nolint:goconst // wire bodies spell each product's JSON keys inline, as its API docs do
package forgeconntest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// gitlabWriteRoute answers the routes forgeapi's writes send on GitLab,
// applying each write to the World.
func (s *Server) gitlabWriteRoute(w http.ResponseWriter, r *http.Request, repo *Repo, pattern string, args []string) bool {
	switch pattern {
	case "PUT merge_requests/{n}", "PUT issues/{n}":
		mr := pattern == "PUT merge_requests/{n}"
		if it := repo.item(args[0], mr); it != nil {
			s.gitlabUpdate(w, r, repo, it, mr)
			return true
		}
	case "PUT merge_requests/{n}/merge":
		if it := repo.item(args[0], true); it != nil {
			s.gitlabMerge(w, r, repo, it)
			return true
		}
	case "POST merge_requests/{n}/cancel_merge_when_pipeline_succeeds":
		if it := repo.item(args[0], true); it != nil {
			gitlabCancelDeprecated(w, it)
			return true
		}
	case "POST merge_requests/{n}/cancel_auto_merge":
		if !versionAtLeast(s.gitlabVersion(), 19, 5) {
			return false
		}
		if it := repo.item(args[0], true); it != nil {
			s.gitlabCancel(w, repo, it)
			return true
		}
	case "POST pipelines/{id}/retry":
		return s.gitlabRetry(w, repo, args[0])
	default:
		return false
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
	return true
}

// gitlabUpdate applies a merge request or issue update: a state event, and
// labels added or removed. An event the item's state cannot take changes
// nothing and still answers 200, as GitLab's update does.
func (s *Server) gitlabUpdate(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item, mr bool) {
	var body struct {
		StateEvent string   `json:"state_event"`
		Add        []string `json:"add_labels"`
		Remove     []string `json:"remove_labels"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "400 Bad request"})
		return
	}
	switch {
	case body.StateEvent == "close" && it.open():
		it.State = "closed"
	case body.StateEvent == "reopen" && it.State == "closed":
		it.State = "open"
	}
	for _, name := range body.Add {
		if !slices.Contains(it.Labels, name) {
			it.Labels = append(it.Labels, name)
		}
	}
	it.Labels = slices.DeleteFunc(it.Labels, func(name string) bool { return slices.Contains(body.Remove, name) })
	it.Updated = later(it.Updated)
	writeJSON(w, http.StatusOK, s.gitlabRecord(repo, it, mr, false))
}

// gitlabMerge answers the merge as GitLab's merge endpoint orders its
// checks: 405 where the merge request can neither merge now nor arm its
// merge for a running pipeline, 422 where it is a draft or conflicts, 409
// where the pin names another head; otherwise it arms the merge when asked
// and the pipeline runs, and merges at once when it can.
func (s *Server) gitlabMerge(w http.ResponseWriter, r *http.Request, repo *Repo, it *Item) {
	var body struct {
		SHA       string `json:"sha"`
		AutoMerge bool   `json:"auto_merge"`
	}
	if !decodeBody(r, &body) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "400 Bad request"})
		return
	}
	now := repo.gitlabMergeStatus(it) == "mergeable"
	run := repo.gitlabHeadRun(it)
	armable := body.AutoMerge && it.open() && run != nil && run.Outcome == "pending"
	switch {
	case !now && !armable:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "405 Method Not Allowed"})
		return
	case it.Draft || it.Conflicting:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Branch cannot be merged"})
		return
	case body.SHA != it.HeadSHA:
		writeJSON(w, http.StatusConflict, map[string]any{"message": "SHA does not match HEAD of source branch: " + it.HeadSHA})
		return
	case armable:
		it.AutoMerge = true
	default:
		it.State, it.AutoMerge = "merged", false
	}
	it.Updated = later(it.Updated)
	writeJSON(w, http.StatusOK, s.gitlabRecord(repo, it, true, false))
}

// gitlabCancelDeprecated answers the auto-merge cancel route GitLab before
// 19.5 serves: 201 with a status of success where a merge was armed, and of
// error where none was.
func gitlabCancelDeprecated(w http.ResponseWriter, it *Item) {
	status := "error"
	if it.AutoMerge {
		it.AutoMerge, it.Updated, status = false, later(it.Updated), "success"
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": status})
}

// gitlabCancel answers the auto-merge cancel route of GitLab 19.5 on: 201
// with the merge request where a merge was armed, 409 where none was.
func (s *Server) gitlabCancel(w http.ResponseWriter, repo *Repo, it *Item) {
	if !it.AutoMerge {
		writeJSON(w, http.StatusConflict, map[string]any{"message": "409 Conflict"})
		return
	}
	it.AutoMerge, it.Updated = false, later(it.Updated)
	writeJSON(w, http.StatusCreated, s.gitlabRecord(repo, it, true, false))
}

// gitlabRetry retries pipeline id's failed jobs: it is pending until it
// ends.
func (s *Server) gitlabRetry(w http.ResponseWriter, repo *Repo, id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false
	}
	for i := range repo.Runs {
		if run := &repo.Runs[i]; run.ID == n {
			run.Outcome, run.Updated = "pending", later(run.Updated)
			writeJSON(w, http.StatusCreated, s.gitlabPipeline(repo, run))
			return true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
	return true
}

// versionAtLeast reports whether a dotted version such as 19.5.0 is at or
// past major.minor.
func versionAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	gotMajor, majorErr := strconv.Atoi(parts[0])
	gotMinor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}
