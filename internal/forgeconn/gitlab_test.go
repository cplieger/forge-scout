package forgeconn_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forgeapi/v3"
)

// sent reports whether the fake served a request whose line starts with
// prefix.
func sent(srv *forgeconntest.Server, prefix string) bool {
	return slices.ContainsFunc(srv.Requests(), func(line string) bool { return strings.HasPrefix(line, prefix) })
}

func TestGitLab_a_user_owner_lists_its_projects_and_no_cross_project_items(t *testing.T) {
	t.Parallel()
	w := world()
	w.UserNamespace = true
	srv, c := open(t, forgeconntest.GitLab, w)
	l, err := c.Repos(t.Context(), owner)
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	whole(t, &l)
	if len(l.Rows) != 2 || l.Rows[0].Key != appKey || l.Rows[1].Key != libKey {
		t.Errorf("Repos = %+v, want app and lib", l.Rows)
	}
	if !sent(srv, "GET /api/v4/users/acme/projects") {
		t.Errorf("requests = %q, want the user's project route after the group route's 404", srv.Requests())
	}
	_, err = c.MyPRs(t.Context(), owner)
	wantErr(t, err, forge.KindNotFound, forgeapi.CodeOwnerUnresolved)
	_, err = c.MyIssues(t.Context(), owner)
	wantErr(t, err, forge.KindNotFound, forgeapi.CodeOwnerUnresolved)
	handledAll(t, srv)
}

func TestGitLab_an_owner_named_by_digits_is_resolved_to_its_id_first(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		user  bool
		route string
	}{
		{"group", false, "GET /api/v4/groups/4242/projects"},
		{"user", true, "GET /api/v4/users/77/projects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := world()
			w.Owner, w.UserNamespace = "2048", tc.user
			srv, c := open(t, forgeconntest.GitLab, w)
			l, err := c.Repos(t.Context(), "2048")
			if err != nil {
				t.Fatalf("Repos: %v", err)
			}
			whole(t, &l)
			if len(l.Rows) != 2 || l.Rows[0].Path != "2048/app" || l.Rows[0].Key.Repo != repoID("2048/app") {
				t.Errorf("Repos = %+v, want 2048/app and 2048/lib", l.Rows)
			}
			if !sent(srv, "POST graphql OwnerLookup") || !sent(srv, tc.route) {
				t.Errorf("requests = %q, want the owner lookup, then %s", srv.Requests(), tc.route)
			}
			handledAll(t, srv)
		})
	}
}

func TestGitLab_an_instance_refusing_documents_is_read_over_rest(t *testing.T) {
	t.Parallel()
	w := world()
	w.RefuseDocuments = true
	srv, c := open(t, forgeconntest.GitLab, w)
	// The REST record's merge status may be one nothing refreshed and it
	// names no reviews, so forgeapi answers each unknown. A list row carries
	// no pipeline; a single read carries the stored head pipeline, which
	// speaks for the head where it ran on it.
	unread := forge.Head{
		Mergeable: forge.SupportUnknown, MergeBlocked: forge.MergeBlockUnknown, AutoMergeArmed: forge.SupportUnknown,
		Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Approvals: forge.CountUnknown, ChangesRequested: forge.CountUnknown, Read: true,
	}
	headOf := func(sha string, checks forge.CheckState) forge.Head {
		h := unread
		h.ForHead, h.Checks = sha, checks
		return h
	}
	t.Run("RepoPRs", func(t *testing.T) {
		l, err := c.RepoPRs(t.Context(), appKey)
		if err != nil {
			t.Fatalf("RepoPRs: %v", err)
		}
		whole(t, &l)
		heads(t, l.Rows, []forge.Head{headOf(passed, forge.CheckUnknown), headOf(failed, forge.CheckUnknown), headOf(bare, forge.CheckUnknown)})
	})
	t.Run("ReadPR", func(t *testing.T) {
		pr, err := c.ReadPR(t.Context(), appItem(3))
		if err != nil {
			t.Fatalf("ReadPR: %v", err)
		}
		if pr.State != forge.ItemMerged || pr.Head != headOf(passed, forge.CheckPassing) {
			t.Errorf("ReadPR = state %q head %+v, want merged with its passing pipeline", pr.State, pr.Head)
		}
	})
	t.Run("MergeStatus", func(t *testing.T) {
		s, err := c.MergeStatus(t.Context(), appItem(3))
		if err != nil || s.Merged != forge.SupportYes {
			t.Errorf("MergeStatus = %+v, %v, want merged", s, err)
		}
	})
	t.Run("RerunFailedChecks", func(t *testing.T) {
		if err := c.RerunFailedChecks(t.Context(), appItem(2), failed); err != nil {
			t.Fatalf("RerunFailedChecks: %v", err)
		}
		if got := runState(t, c, 102); got != forge.CheckPending {
			t.Errorf("run 102 after the re-run = %q, want pending", got)
		}
	})
	for _, doc := range []string{"POST graphql PRList", "POST graphql PRRead", "POST graphql RerunHead"} {
		if sent(srv, doc) {
			t.Errorf("requests = %q, want no %s after the refused acceptance question", srv.Requests(), doc)
		}
	}
	handledAll(t, srv)
}

func TestGitLab_cancel_auto_merge_takes_the_route_the_version_serves(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		route   string
	}{
		{"18.4.0", "POST /api/v4/projects/acme%2Fapp/merge_requests/2/cancel_merge_when_pipeline_succeeds"},
		{"19.5.0", "POST /api/v4/projects/acme%2Fapp/merge_requests/2/cancel_auto_merge"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			w := world()
			w.Version = tc.version
			srv, c := open(t, forgeconntest.GitLab, w)
			head := armable(t, forgeconntest.GitLab, srv)
			if _, err := c.Merge(t.Context(), appItem(2), forgeconn.MergeRequest{HeadSHA: head, AutoMerge: true}); err != nil {
				t.Fatalf("Merge: %v", err)
			}
			for _, step := range []string{"armed", "nothing_armed"} {
				if err := c.CancelAutoMerge(t.Context(), appItem(2)); err != nil {
					t.Errorf("CancelAutoMerge with %s = %v, want success", step, err)
				}
			}
			if pr := readPR(t, c, 2); pr.Head.AutoMergeArmed != forge.SupportNo {
				t.Errorf("ReadPR after the cancel = armed %q, want no", pr.Head.AutoMergeArmed)
			}
			if n := len(slices.DeleteFunc(srv.Requests(), func(line string) bool { return line != tc.route })); n != 2 {
				t.Errorf("requests = %q, want %s twice", srv.Requests(), tc.route)
			}
			handledAll(t, srv)
		})
	}
}
