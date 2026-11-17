package forgeconn_test

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forgeapi/v3"
)

func wantErr(t *testing.T, err error, kind forge.ErrorKind, code string) *forge.Error {
	t.Helper()
	fe, ok := errors.AsType[*forge.Error](err)
	if !ok {
		t.Fatalf("error = %v, want a forge %s error", err, kind)
	}
	if fe.Kind != kind || fe.Code != code {
		t.Errorf("error = %v, want kind %q code %q", err, kind, code)
	}
	return fe
}

func readPR(t *testing.T, c *forgeconn.Client, n int) forge.PullRequest {
	t.Helper()
	pr, err := c.ReadPR(forgeconn.ActionReads(t.Context(), 10), appItem(n))
	if err != nil {
		t.Fatalf("ReadPR #%d: %v", n, err)
	}
	return pr
}

func readIssue(t *testing.T, c *forgeconn.Client, n int) forge.Issue {
	t.Helper()
	is, err := c.ReadIssue(forgeconn.ActionReads(t.Context(), 10), appItem(n))
	if err != nil {
		t.Fatalf("ReadIssue #%d: %v", n, err)
	}
	return is
}

// mergeable makes pull request #2, whose head has a failing and a running
// check, a ready non-draft pull request with no conflict and nothing armed.
func mergeable(srv *forgeconntest.Server) {
	srv.EditPR("app", 2, false, func(it *forgeconntest.Item) { it.Draft, it.Conflicting, it.AutoMerge = false, false, false })
}

func TestMerge_merges_a_pull_request_that_can_merge_at_its_head(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	out, err := c.Merge(t.Context(), appItem(1), forgeconn.MergeRequest{HeadSHA: passed})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	want := forge.MergeOutcome{State: forge.MergeOutcomeAccepted, Code: forgeapi.CodeAlreadyEnqueued, Queue: forge.QueueUnknown, QueuePosition: forge.CountUnknown}
	if out != want {
		t.Errorf("Merge = %+v, want %+v: GitHub merges in the background and the answer has no verdict", out, want)
	}
	s, err := c.MergeStatus(t.Context(), appItem(1))
	if err != nil || s.Merged != forge.SupportYes {
		t.Errorf("MergeStatus after the merge = %+v, %v, want merged", s, err)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemMerged {
		t.Errorf("ReadPR after the merge = state %q, want merged", pr.State)
	}
	handledAll(t, srv)
}

func TestMerge_a_refused_merge_leaves_the_pull_request_open(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		number int
		req    forgeconn.MergeRequest
		kind   forge.ErrorKind
		code   string
		status int
		sent   bool
	}{
		{"head_moved", 1, forgeconn.MergeRequest{HeadSHA: failed}, forge.KindNotMergeable, forgeapi.CodeNotMergeable, http.StatusBadRequest, true},
		{"strategy_turned_off", 1, forgeconn.MergeRequest{HeadSHA: passed, Strategy: "rebase"}, forge.KindNotMergeable, forgeapi.CodeNotMergeable, http.StatusMethodNotAllowed, true},
		{"checks_failing", 2, forgeconn.MergeRequest{HeadSHA: failed}, forge.KindNotMergeable, forgeapi.CodeNotMergeable, http.StatusMethodNotAllowed, true},
		{"strategy_unknown", 1, forgeconn.MergeRequest{HeadSHA: passed, Strategy: "fast-forward"}, forge.KindInvalid, forgeapi.CodeStrategyNotAllowed, 0, false},
		{"no_head", 1, forgeconn.MergeRequest{}, forge.KindInvalid, forgeapi.CodeMissingSHA, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := open(t, world())
			mergeable(srv)
			sent := len(srv.Requests())
			_, err := c.Merge(t.Context(), appItem(tc.number), tc.req)
			if fe := wantErr(t, err, tc.kind, tc.code); fe.Status != tc.status {
				t.Errorf("status = %d, want %d", fe.Status, tc.status)
			}
			if got := len(srv.Requests()) > sent; got != tc.sent {
				t.Errorf("a request was sent = %v, want %v", got, tc.sent)
			}
			if pr := readPR(t, c, tc.number); pr.State != forge.ItemOpen {
				t.Errorf("ReadPR after the refusal = state %q, want open", pr.State)
			}
			handledAll(t, srv)
		})
	}
}

func TestMerge_auto_merge_arms_a_pull_request_that_cannot_merge_now(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	mergeable(srv)
	out, err := c.Merge(t.Context(), appItem(2), forgeconn.MergeRequest{HeadSHA: failed, AutoMerge: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if out.State != forge.MergeOutcomeEnqueued || out.Code != "" {
		t.Errorf("Merge = %+v, want enqueued with no code", out)
	}
	pr := readPR(t, c, 2)
	if pr.State != forge.ItemOpen || pr.Head.AutoMergeArmed != forge.SupportYes {
		t.Errorf("ReadPR after arming = state %q armed %q, want open and armed", pr.State, pr.Head.AutoMergeArmed)
	}
	if err := c.CancelAutoMerge(t.Context(), appItem(2)); err != nil {
		t.Fatalf("CancelAutoMerge: %v", err)
	}
	if pr := readPR(t, c, 2); pr.Head.AutoMergeArmed != forge.SupportNo {
		t.Errorf("ReadPR after the cancel = armed %q, want no", pr.Head.AutoMergeArmed)
	}
	if err := c.CancelAutoMerge(t.Context(), appItem(2)); err != nil {
		t.Errorf("CancelAutoMerge with nothing armed = %v, want success", err)
	}
	handledAll(t, srv)
}

func TestMerge_auto_merge_on_a_pull_request_that_can_merge_merges_it(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	out, err := c.Merge(t.Context(), appItem(1), forgeconn.MergeRequest{HeadSHA: passed, Strategy: "squash", AutoMerge: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if out.State != forge.MergeOutcomeAccepted {
		t.Errorf("Merge = %+v, want the merge accepted", out)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemMerged {
		t.Errorf("ReadPR = state %q, want merged", pr.State)
	}
	handledAll(t, srv)
}

func TestClosePR_and_ReopenPR_move_the_pull_request(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	if err := c.ClosePR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemClosed || !pr.UpdatedAt.After(t0.Add(time.Hour)) {
		t.Errorf("ReadPR after closing = state %q updated %v, want closed with a newer updated time", pr.State, pr.UpdatedAt)
	}
	if err := c.ReopenPR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemOpen {
		t.Errorf("ReadPR after reopening = state %q, want open", pr.State)
	}
	fe := wantErr(t, c.ReopenPR(t.Context(), appItem(3)), forge.KindUpstream, forgeapi.CodeValidation)
	if fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("ReopenPR of a merged pull request = status %d, want 422", fe.Status)
	}
	handledAll(t, srv)
}

func TestCloseIssue_and_ReopenIssue_move_the_issue(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	if err := c.CloseIssue(t.Context(), appItem(10)); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if is := readIssue(t, c, 10); is.State != forge.ItemClosed {
		t.Errorf("ReadIssue after closing = state %q, want closed", is.State)
	}
	if err := c.ReopenIssue(t.Context(), appItem(11)); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	if is := readIssue(t, c, 11); is.State != forge.ItemOpen {
		t.Errorf("ReadIssue after reopening = state %q, want open", is.State)
	}
	handledAll(t, srv)
}

func TestLabels_add_and_remove_change_the_items_labels(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	t.Run("pull_request", func(t *testing.T) {
		if err := c.AddLabels(t.Context(), appItem(1), forge.KindPullRequest, []string{"ready"}); err != nil {
			t.Fatalf("AddLabels: %v", err)
		}
		if pr := readPR(t, c, 1); !slices.Equal(pr.Labels, []string{"bug", "ready"}) {
			t.Errorf("labels after adding = %v, want bug and ready", pr.Labels)
		}
		if err := c.RemoveLabels(t.Context(), appItem(1), forge.KindPullRequest, []string{"bug", "not-carried"}); err != nil {
			t.Fatalf("RemoveLabels: %v", err)
		}
		if pr := readPR(t, c, 1); !slices.Equal(pr.Labels, []string{"ready"}) {
			t.Errorf("labels after removing = %v, want ready alone", pr.Labels)
		}
	})
	t.Run("issue", func(t *testing.T) {
		if err := c.AddLabels(t.Context(), appItem(10), forge.KindIssue, []string{"ready"}); err != nil {
			t.Fatalf("AddLabels: %v", err)
		}
		if is := readIssue(t, c, 10); !slices.Equal(is.Labels, []string{"bug", "ready"}) {
			t.Errorf("labels after adding = %v, want bug and ready", is.Labels)
		}
	})
	t.Run("undefined_label", func(t *testing.T) {
		wantErr(t, c.AddLabels(t.Context(), appItem(10), forge.KindIssue, []string{"nope"}), forge.KindUpstream, forgeapi.CodeValidation)
		if is := readIssue(t, c, 10); slices.Contains(is.Labels, "nope") {
			t.Errorf("labels after a refused addition = %v, want no nope", is.Labels)
		}
	})
	t.Run("unknown_kind", func(t *testing.T) {
		sent := len(srv.Requests())
		wantErr(t, c.AddLabels(t.Context(), appItem(10), forge.Kind("run"), []string{"ready"}), forge.KindInvalid, "")
		if got := srv.Requests()[sent:]; len(got) != 0 {
			t.Errorf("requests = %q, want none", got)
		}
	})
	handledAll(t, srv)
}

func runState(t *testing.T, c *forgeconn.Client, id int64) forge.CheckState {
	t.Helper()
	l, err := c.RunsSince(t.Context(), appKey, t0.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("RunsSince: %v", err)
	}
	for i := range l.Rows {
		if l.Rows[i].ID == id {
			return l.Rows[i].State
		}
	}
	t.Fatalf("RunsSince holds no run %d", id)
	return ""
}

func TestRerunFailedChecks_reruns_the_failed_run_on_the_pinned_head(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	if err := c.RerunFailedChecks(t.Context(), appItem(1), passed); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	if got := runState(t, c, 102); got != forge.CheckPending {
		t.Errorf("run 102 after the re-run = %q, want pending", got)
	}
	if got := runState(t, c, 101); got != forge.CheckPassing {
		t.Errorf("run 101 = %q, want its pass kept", got)
	}
	t.Run("head_no_run_carries", func(t *testing.T) {
		wantErr(t, c.RerunFailedChecks(t.Context(), appItem(4), bare), forge.KindConflict, forgeapi.CodeStaleHead)
	})
	t.Run("head_with_nothing_failed", func(t *testing.T) {
		wantErr(t, c.RerunFailedChecks(t.Context(), appItem(2), failed), forge.KindConflict, forgeapi.CodeRunNotFailed)
	})
	handledAll(t, srv)
}

func TestRerunFailedRun_reruns_the_run(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	run := forge.Run{Repo: appKey, ID: 102, Number: 2}
	if err := c.RerunFailedRun(t.Context(), &run); err != nil {
		t.Fatalf("RerunFailedRun: %v", err)
	}
	if got := runState(t, c, 102); got != forge.CheckPending {
		t.Errorf("run 102 after the re-run = %q, want pending", got)
	}
	gone := forge.Run{Repo: appKey, ID: 999, Number: 9}
	wantErr(t, c.RerunFailedRun(t.Context(), &gone), forge.KindNotFound, forgeapi.CodeRepoOrPRNotVisible)
	handledAll(t, srv)
}

// writeLog records each attempt's request line and whether Mutates marks it.
type writeLog struct {
	marked []string
	mu     sync.Mutex
	all    int
}

func (l *writeLog) observe(next http.RoundTripper) http.RoundTripper {
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		l.mu.Lock()
		l.all++
		if forgeconn.Mutates(r) {
			l.marked = append(l.marked, r.Method+" "+r.URL.Path)
		}
		l.mu.Unlock()
		return next.RoundTrip(r)
	})
}

// since is the marked attempts, and the number of attempts, made after the
// point at returned mark and all.
func (l *writeLog) since(mark, all int) (marked []string, attempts int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.marked[mark:]...), l.all - all
}

func (l *writeLog) at() (mark, all int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.marked), l.all
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMutates_marks_each_write_and_never_a_read(t *testing.T) {
	t.Parallel()
	w := world()
	w.Repos[0].PRs[1].Draft, w.Repos[0].PRs[1].Conflicting, w.Repos[0].PRs[1].AutoMerge = false, false, false
	srv := forgeconntest.New(forgeconntest.GitHub, &w)
	t.Cleanup(srv.Close)
	log := &writeLog{}
	cfg := srv.Config(conn)
	cfg.AttemptObserver = log.observe
	c, err := forgeconn.Open(t.Context(), &cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(c.Close)
	ctx := t.Context()
	const (
		labels  = "POST /api/v3/repos/acme/app/issues/1/labels"
		graphql = "POST /api/graphql"
	)
	tests := []struct {
		name string
		step func() error
		want []string
		// reads is the fewest unmarked attempts the step makes beside its
		// writes.
		reads int
	}{
		{"search_pages", func() error { _, err := c.MyPRs(ctx, owner); return err }, nil, 1},
		{"add_two_labels", func() error { return c.AddLabels(ctx, appItem(1), forge.KindPullRequest, []string{"bug", "ready"}) }, []string{labels}, 1},
		{"remove_two_labels", func() error { return c.RemoveLabels(ctx, appItem(1), forge.KindPullRequest, []string{"bug", "ready"}) }, []string{
			"DELETE /api/v3/repos/acme/app/issues/1/labels/bug", "DELETE /api/v3/repos/acme/app/issues/1/labels/ready",
		}, 0},
		{"arm_auto_merge", func() error {
			_, err := c.Merge(ctx, appItem(2), forgeconn.MergeRequest{HeadSHA: failed, AutoMerge: true})
			return err
		}, []string{graphql}, 1},
		{"cancel_auto_merge", func() error { return c.CancelAutoMerge(ctx, appItem(2)) }, []string{graphql}, 1},
		{"merge", func() error {
			_, err := c.Merge(ctx, appItem(1), forgeconn.MergeRequest{HeadSHA: passed})
			return err
		}, []string{"PUT /api/v3/repos/acme/app/pulls/1/merge-async"}, 0},
		{"merge_status", func() error { _, err := c.MergeStatus(ctx, appItem(1)); return err }, nil, 1},
		{"close_issue", func() error { return c.CloseIssue(ctx, appItem(10)) }, []string{"PATCH /api/v3/repos/acme/app/issues/10"}, 0},
		{"rerun_checks", func() error { return c.RerunFailedChecks(ctx, appItem(1), passed) }, []string{
			"POST /api/v3/repos/acme/app/actions/runs/102/rerun-failed-jobs",
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mark, all := log.at()
			if err := tc.step(); err != nil {
				t.Fatalf("step: %v", err)
			}
			marked, attempts := log.since(mark, all)
			if !slices.Equal(marked, tc.want) {
				t.Errorf("marked = %q, want %q", marked, tc.want)
			}
			if reads := attempts - len(marked); reads < tc.reads {
				t.Errorf("unmarked attempts = %d, want at least %d", reads, tc.reads)
			}
		})
	}
	handledAll(t, srv)
}

func TestActionReads_pass_the_reserve_that_background_reads_stop_at(t *testing.T) {
	t.Parallel()
	srv, c := open(t, world())
	srv.SetBudget(forge.ReadReserve-50, time.Now().Add(time.Hour))
	if _, err := c.Labels(t.Context(), appKey); err != nil {
		t.Fatalf("Labels, which learns the REST pool: %v", err)
	}
	if _, err := c.ReadPR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ReadPR, which learns the GraphQL pool: %v", err)
	}
	t.Run("background_read", func(t *testing.T) {
		sent := len(srv.Requests())
		_, err := c.ReadPR(t.Context(), appItem(1))
		wantErr(t, err, forge.KindDeferred, "")
		if got := srv.Requests()[sent:]; len(got) != 0 {
			t.Errorf("requests = %q, want none sent", got)
		}
	})
	t.Run("action_read", func(t *testing.T) {
		pr, err := c.ReadPR(forgeconn.ActionReads(t.Context(), 10), appItem(1))
		if err != nil || pr.Key != appItem(1) {
			t.Errorf("ReadPR under ActionReads = %+v, %v, want it read", pr.Key, err)
		}
	})
	t.Run("write", func(t *testing.T) {
		if err := c.ClosePR(t.Context(), appItem(1)); err != nil {
			t.Errorf("ClosePR at the reserve = %v, want it sent", err)
		}
	})
	t.Run("read_back_after_the_write", func(t *testing.T) {
		pr, err := c.ReadPR(forgeconn.ActionReads(t.Context(), 10), appItem(1))
		if err != nil || pr.State != forge.ItemClosed {
			t.Errorf("ReadPR under ActionReads = state %q, %v, want closed", pr.State, err)
		}
	})
	handledAll(t, srv)
}
