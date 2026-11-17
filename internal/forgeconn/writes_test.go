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

// mergeable makes pull request #2, whose head has failing checks, a ready
// non-draft pull request with no conflict and nothing armed.
func mergeable(srv *forgeconntest.Server) {
	srv.EditPR("app", 2, false, func(it *forgeconntest.Item) { it.Draft, it.Conflicting, it.AutoMerge = false, false, false })
}

// armable makes pull request #2 one whose merge can be armed and not made
// now, and returns its head: GitHub, Gitea and Forgejo arm a merge over
// failing checks, GitLab only over a running pipeline.
func armable(t *testing.T, p forgeconntest.Product, srv *forgeconntest.Server) string {
	t.Helper()
	mergeable(srv)
	head := per(t, p, map[forgeconntest.Product]string{
		forgeconntest.GitHub: failed, forgeconntest.GitLab: running, forgeconntest.Gitea: failed, forgeconntest.Forgejo: failed,
	})
	srv.PushHead("app", 2, head)
	return head
}

// armed reports whether pr's merge is armed: as read, or on Gitea and
// Forgejo, whose answers carry no armed flag, as the fake holds it.
func armed(t *testing.T, p forgeconntest.Product, srv *forgeconntest.Server, pr *forge.PullRequest) bool {
	t.Helper()
	switch p {
	case forgeconntest.Gitea, forgeconntest.Forgejo:
		if pr.Head.AutoMergeArmed != forge.SupportUnknown {
			t.Errorf("#%d armed = %q, want unknown: the answer carries no armed flag", pr.Key.Number, pr.Head.AutoMergeArmed)
		}
		return srv.PR("app", pr.Key.Number).AutoMerge
	}
	if pr.Head.AutoMergeArmed == forge.SupportUnknown {
		t.Errorf("#%d armed = unknown, want it read", pr.Key.Number)
	}
	return pr.Head.AutoMergeArmed == forge.SupportYes
}

// mergeCase is one merge the forge or forgeapi refuses.
type mergeCase struct {
	setup  func(*forgeconntest.Server)
	req    forgeconn.MergeRequest
	name   string
	kind   forge.ErrorKind
	code   string
	number int
	status int
	sent   bool
}

func testMerge(t *testing.T, p forgeconntest.Product) {
	t.Run("merges_at_its_head", func(t *testing.T) {
		t.Parallel()
		srv, c := open(t, p, world())
		out, err := c.Merge(t.Context(), appItem(1), forgeconn.MergeRequest{HeadSHA: passed})
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}
		// GitHub merges in the background and its answer has no verdict.
		want := per(t, p, map[forgeconntest.Product]forge.MergeOutcome{
			forgeconntest.GitHub:  {State: forge.MergeOutcomeAccepted, Code: forgeapi.CodeAlreadyEnqueued, Queue: forge.QueueUnknown, QueuePosition: forge.CountUnknown},
			forgeconntest.GitLab:  {State: forge.MergeOutcomeMerged, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown},
			forgeconntest.Gitea:   {State: forge.MergeOutcomeMerged, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown},
			forgeconntest.Forgejo: {State: forge.MergeOutcomeMerged, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown},
		})
		if out != want {
			t.Errorf("Merge = %+v, want %+v", out, want)
		}
		s, err := c.MergeStatus(t.Context(), appItem(1))
		if err != nil || s.Merged != forge.SupportYes {
			t.Errorf("MergeStatus after the merge = %+v, %v, want merged", s, err)
		}
		if pr := readPR(t, c, 1); pr.State != forge.ItemMerged {
			t.Errorf("ReadPR after the merge = state %q, want merged", pr.State)
		}
		handledAll(t, srv)
	})
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		local := []mergeCase{
			{name: "strategy_unknown", number: 1, req: forgeconn.MergeRequest{HeadSHA: passed, Strategy: "fast-forward"}, kind: forge.KindInvalid, code: forgeapi.CodeStrategyNotAllowed},
			{name: "no_head", number: 1, req: forgeconn.MergeRequest{}, kind: forge.KindInvalid, code: forgeapi.CodeMissingSHA},
		}
		// GitHub refuses a moved head 400 and a merge it cannot make 405;
		// GitLab refuses one it can neither make nor arm 405, a draft or a
		// conflict it could arm 422, and a moved head 409, in that order;
		// Gitea and Forgejo refuse a merge they cannot make 405 and a moved
		// head 409, naming neither cause.
		gitea := []mergeCase{
			{name: "head_moved", number: 1, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindConflict, code: forgeapi.CodeNotMergeable, status: http.StatusConflict, sent: true},
			{name: "strategy_turned_off", number: 1, req: forgeconn.MergeRequest{HeadSHA: passed, Strategy: "rebase"}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusMethodNotAllowed, sent: true},
			{name: "checks_failing", number: 2, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusMethodNotAllowed, sent: true},
		}
		cases := append(local, per(t, p, map[forgeconntest.Product][]mergeCase{
			forgeconntest.Gitea:   gitea,
			forgeconntest.Forgejo: gitea,
			forgeconntest.GitHub: {
				{name: "head_moved", number: 1, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusBadRequest, sent: true},
				{name: "strategy_turned_off", number: 1, req: forgeconn.MergeRequest{HeadSHA: passed, Strategy: "rebase"}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusMethodNotAllowed, sent: true},
				{name: "checks_failing", number: 2, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusMethodNotAllowed, sent: true},
			},
			forgeconntest.GitLab: {
				{name: "head_moved", number: 1, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindConflict, code: forgeapi.CodeStaleHead, status: http.StatusConflict, sent: true},
				{name: "strategy_not_listed", number: 1, req: forgeconn.MergeRequest{HeadSHA: passed, Strategy: "rebase"}, kind: forge.KindInvalid, code: forgeapi.CodeStrategyNotAllowed},
				{name: "pipeline_failed", number: 2, req: forgeconn.MergeRequest{HeadSHA: failed}, kind: forge.KindNotMergeable, code: forgeapi.CodeNotMergeable, status: http.StatusMethodNotAllowed, sent: true},
				{
					name: "conflict_while_the_pipeline_runs", number: 2, req: forgeconn.MergeRequest{HeadSHA: running, AutoMerge: true},
					setup: func(srv *forgeconntest.Server) {
						srv.PushHead("app", 2, running)
						srv.EditPR("app", 2, false, func(it *forgeconntest.Item) { it.Conflicting = true })
					},
					kind: forge.KindNotMergeable, code: forgeapi.CodeBranchCannotMerge, status: http.StatusUnprocessableEntity, sent: true,
				},
			},
		})...)
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				srv, c := open(t, p, world())
				mergeable(srv)
				if tc.setup != nil {
					tc.setup(srv)
				}
				sent := len(srv.Requests())
				_, err := c.Merge(t.Context(), appItem(tc.number), tc.req)
				if fe := wantErr(t, err, tc.kind, tc.code); fe.Status != tc.status {
					t.Errorf("status = %d, want %d", fe.Status, tc.status)
				}
				if got := len(srv.Requests()) > sent; got != tc.sent {
					t.Errorf("a request was sent = %v, want %v", got, tc.sent)
				}
				if pr := readPR(t, c, tc.number); pr.State != forge.ItemOpen || armed(t, p, srv, &pr) {
					t.Errorf("ReadPR after the refusal = state %q, want open with nothing armed", pr.State)
				}
				handledAll(t, srv)
			})
		}
	})
	t.Run("auto_merge_arms_one_that_cannot_merge_now", func(t *testing.T) {
		t.Parallel()
		srv, c := open(t, p, world())
		head := armable(t, p, srv)
		out, err := c.Merge(t.Context(), appItem(2), forgeconn.MergeRequest{HeadSHA: head, AutoMerge: true})
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}
		if out.State != forge.MergeOutcomeEnqueued || out.Code != "" {
			t.Errorf("Merge = %+v, want enqueued with no code", out)
		}
		if pr := readPR(t, c, 2); pr.State != forge.ItemOpen || !armed(t, p, srv, &pr) {
			t.Errorf("ReadPR after arming = state %q, want open and armed", pr.State)
		}
		handledAll(t, srv)
	})
	t.Run("auto_merge_merges_one_that_can_merge_now", func(t *testing.T) {
		t.Parallel()
		srv, c := open(t, p, world())
		out, err := c.Merge(t.Context(), appItem(1), forgeconn.MergeRequest{HeadSHA: passed, Strategy: "squash", AutoMerge: true})
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}
		want := per(t, p, map[forgeconntest.Product]forge.MergeOutcomeState{
			forgeconntest.GitHub: forge.MergeOutcomeAccepted, forgeconntest.GitLab: forge.MergeOutcomeMerged,
			forgeconntest.Gitea: forge.MergeOutcomeMerged, forgeconntest.Forgejo: forge.MergeOutcomeMerged,
		})
		if out.State != want {
			t.Errorf("Merge = %+v, want %q", out, want)
		}
		if pr := readPR(t, c, 1); pr.State != forge.ItemMerged {
			t.Errorf("ReadPR = state %q, want merged", pr.State)
		}
		handledAll(t, srv)
	})
}

func testCancelAutoMerge(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	head := armable(t, p, srv)
	if _, err := c.Merge(t.Context(), appItem(2), forgeconn.MergeRequest{HeadSHA: head, AutoMerge: true}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := c.CancelAutoMerge(t.Context(), appItem(2)); err != nil {
		t.Fatalf("CancelAutoMerge: %v", err)
	}
	if pr := readPR(t, c, 2); armed(t, p, srv, &pr) || pr.State != forge.ItemOpen {
		t.Errorf("ReadPR after the cancel = state %q, want open with nothing armed", pr.State)
	}
	if err := c.CancelAutoMerge(t.Context(), appItem(2)); err != nil {
		t.Errorf("CancelAutoMerge with nothing armed = %v, want success", err)
	}
	handledAll(t, srv)
}

func testClosePR(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	if err := c.ClosePR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemClosed || !pr.UpdatedAt.After(t0.Add(time.Hour)) {
		t.Errorf("ReadPR after closing = state %q updated %v, want closed with a newer updated time", pr.State, pr.UpdatedAt)
	}
	handledAll(t, srv)
}

func testReopenPR(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	if err := c.ClosePR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if err := c.ReopenPR(t.Context(), appItem(1)); err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}
	if pr := readPR(t, c, 1); pr.State != forge.ItemOpen {
		t.Errorf("ReadPR after reopening = state %q, want open", pr.State)
	}
	t.Run("merged", func(t *testing.T) {
		// GitHub refuses the transition 422, Gitea and Forgejo 412; GitLab's
		// update ignores an event the state cannot take and answers 200.
		err := c.ReopenPR(t.Context(), appItem(3))
		switch p {
		case forgeconntest.Gitea, forgeconntest.Forgejo:
			if fe := wantErr(t, err, forge.KindUpstream, ""); fe.Status != http.StatusPreconditionFailed {
				t.Errorf("ReopenPR of a merged pull request = status %d, want 412", fe.Status)
			}
		case forgeconntest.GitHub:
			if fe := wantErr(t, err, forge.KindUpstream, forgeapi.CodeValidation); fe.Status != http.StatusUnprocessableEntity {
				t.Errorf("ReopenPR of a merged pull request = status %d, want 422", fe.Status)
			}
		case forgeconntest.GitLab:
			if err != nil {
				t.Errorf("ReopenPR of a merged merge request = %v, want the ignored event's success", err)
			}
		default:
			t.Fatalf("no expectation written for %s", p)
		}
		if pr := readPR(t, c, 3); pr.State != forge.ItemMerged {
			t.Errorf("ReadPR after reopening a merged pull request = state %q, want merged", pr.State)
		}
	})
	handledAll(t, srv)
}

func testCloseIssue(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	if err := c.CloseIssue(t.Context(), appItem(10)); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if is := readIssue(t, c, 10); is.State != forge.ItemClosed || !is.UpdatedAt.After(t0) {
		t.Errorf("ReadIssue after closing = state %q updated %v, want closed with a newer updated time", is.State, is.UpdatedAt)
	}
	handledAll(t, srv)
}

func testReopenIssue(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	if err := c.ReopenIssue(t.Context(), appItem(11)); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	if is := readIssue(t, c, 11); is.State != forge.ItemOpen {
		t.Errorf("ReadIssue after reopening = state %q, want open", is.State)
	}
	handledAll(t, srv)
}

func testAddLabels(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	t.Run("pull_request", func(t *testing.T) {
		if err := c.AddLabels(t.Context(), appItem(1), forge.KindPullRequest, []string{"ready"}); err != nil {
			t.Fatalf("AddLabels: %v", err)
		}
		if pr := readPR(t, c, 1); !slices.Equal(pr.Labels, []string{"bug", "ready"}) {
			t.Errorf("labels after adding = %v, want bug and ready", pr.Labels)
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
		wantErr(t, c.AddLabels(t.Context(), appItem(11), forge.KindIssue, []string{"nope"}), forge.KindUpstream, forgeapi.CodeValidation)
		if is := readIssue(t, c, 11); slices.Contains(is.Labels, "nope") {
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

func testRemoveLabels(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	t.Run("pull_request", func(t *testing.T) {
		if err := c.RemoveLabels(t.Context(), appItem(1), forge.KindPullRequest, []string{"bug", "not-carried"}); err != nil {
			t.Fatalf("RemoveLabels: %v", err)
		}
		if pr := readPR(t, c, 1); len(pr.Labels) != 0 {
			t.Errorf("labels after removing = %v, want none", pr.Labels)
		}
	})
	t.Run("issue", func(t *testing.T) {
		if err := c.RemoveLabels(t.Context(), appItem(10), forge.KindIssue, []string{"bug"}); err != nil {
			t.Fatalf("RemoveLabels: %v", err)
		}
		if is := readIssue(t, c, 10); len(is.Labels) != 0 {
			t.Errorf("labels after removing = %v, want none", is.Labels)
		}
	})
	t.Run("unknown_kind", func(t *testing.T) {
		sent := len(srv.Requests())
		wantErr(t, c.RemoveLabels(t.Context(), appItem(10), forge.Kind("run"), []string{"bug"}), forge.KindInvalid, "")
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

func testRerunFailedChecks(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	if err := c.RerunFailedChecks(t.Context(), appItem(2), failed); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	if got := runState(t, c, 102); got != forge.CheckPending {
		t.Errorf("run 102 after the re-run = %q, want pending", got)
	}
	if got := runState(t, c, 101); got != forge.CheckPassing {
		t.Errorf("run 101 = %q, want its pass kept", got)
	}
	t.Run("head_with_nothing_failed", func(t *testing.T) {
		wantErr(t, c.RerunFailedChecks(t.Context(), appItem(1), passed), forge.KindConflict, forgeapi.CodeRunNotFailed)
	})
	// GitHub, Gitea and Forgejo resolve a run on the pin without reading the
	// pull request, so a pin no run carries is stale; GitLab reads the merge
	// request's head and its pipeline.
	onPin := map[forgeconntest.Product]string{
		forgeconntest.GitHub: forgeapi.CodeStaleHead, forgeconntest.GitLab: forgeapi.CodeRunNotFailed,
		forgeconntest.Gitea: forgeapi.CodeStaleHead, forgeconntest.Forgejo: forgeapi.CodeStaleHead,
	}
	t.Run("head_no_run_carries", func(t *testing.T) {
		code := per(t, p, onPin)
		wantErr(t, c.RerunFailedChecks(t.Context(), appItem(4), bare), forge.KindConflict, code)
	})
	t.Run("pin_not_the_head", func(t *testing.T) {
		code := per(t, p, map[forgeconntest.Product]string{
			forgeconntest.GitHub: forgeapi.CodeRunNotFailed, forgeconntest.GitLab: forgeapi.CodeStaleHead,
			forgeconntest.Gitea: forgeapi.CodeRunNotFailed, forgeconntest.Forgejo: forgeapi.CodeRunNotFailed,
		})
		wantErr(t, c.RerunFailedChecks(t.Context(), appItem(4), passed), forge.KindConflict, code)
	})
	handledAll(t, srv)
}

func testRerunFailedRun(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
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
		{"rerun_checks", func() error { return c.RerunFailedChecks(ctx, appItem(2), failed) }, []string{
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
	srv, c := open(t, forgeconntest.GitHub, world())
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
