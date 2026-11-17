package forgeconn_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forgeapi/v3"
)

const (
	conn   = "c1"
	owner  = "acme"
	passed = "1111111111111111111111111111111111111111"
	failed = "2222222222222222222222222222222222222222"
	bare   = "4444444444444444444444444444444444444444"
)

var (
	t0      = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	appKey  = forge.RepoKey{Connection: conn, Repo: repoID("acme/app")}
	libKey  = forge.RepoKey{Connection: conn, Repo: repoID("acme/lib")}
	appItem = func(n int) forge.ItemKey { return forge.ItemKey{Connection: conn, Repo: appKey.Repo, Number: n} }
)

func repoID(selector string) string { return forgeapi.RepoRef{Selector: selector}.Encode() }

// world is one owner with a repository holding pull requests in every check
// state, issues, runs and labels, and an archived private fork beside it.
func world() forgeconntest.World {
	return forgeconntest.World{
		Login: "octo", Owner: owner,
		Repos: []forgeconntest.Repo{
			{
				Name: "app", DefaultBranch: "main", Labels: []string{"bug", "ready"},
				Checks: map[string][]string{passed: {"success", "success"}, failed: {"failure", "pending", "success"}},
				PRs: []forgeconntest.Item{
					{Number: 1, Title: "Add the thing", Body: "Body.", Author: "octo", HeadSHA: passed, Labels: []string{"bug"}, Comments: 3, Approvals: 2, ChangesRequested: 1, Created: t0, Updated: t0.Add(time.Hour)},
					{Number: 2, Title: "Draft", Author: "renovate", AuthorType: "Bot", HeadSHA: failed, Draft: true, AutoMerge: true, Conflicting: true, Created: t0, Updated: t0},
					{Number: 3, Title: "Merged", Author: "octo", HeadSHA: passed, State: "merged", Created: t0, Updated: t0},
					{Number: 4, Title: "No CI", Author: "octo", HeadSHA: bare, Created: t0, Updated: t0},
				},
				Issues: []forgeconntest.Item{
					{Number: 10, Title: "Broken", Body: "It broke.", Author: "someone", Labels: []string{"bug"}, Comments: 2, Created: t0, Updated: t0},
					{Number: 11, Title: "Fixed", Author: "someone", State: "closed", Created: t0, Updated: t0},
				},
				Runs: []forgeconntest.Run{
					{ID: 101, Number: 1, Workflow: "ci", Branch: "main", HeadSHA: passed, Event: "push", Outcome: "success", Created: t0.Add(-48 * time.Hour), Updated: t0.Add(-48 * time.Hour)},
					{ID: 102, Number: 2, Workflow: "ci", Branch: "main", HeadSHA: passed, Event: "schedule", Outcome: "failure", Created: t0, Started: t0, Updated: t0.Add(time.Minute)},
					{ID: 103, Number: 3, Workflow: "ci", Title: "Release", Branch: "main", HeadSHA: failed, Event: "workflow_dispatch", Outcome: "pending", Created: t0.Add(time.Hour), Updated: t0.Add(time.Hour)},
				},
			},
			{Name: "lib", DefaultBranch: "trunk", Private: true, Archived: true, Fork: true},
		},
	}
}

func open(t *testing.T, w forgeconntest.World) (*forgeconntest.Server, *forgeconn.Client) {
	t.Helper()
	srv := forgeconntest.New(forgeconntest.GitHub, &w)
	t.Cleanup(srv.Close)
	cfg := srv.Config(conn)
	c, err := forgeconn.Open(t.Context(), &cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(c.Close)
	return srv, c
}

func handledAll(t *testing.T, srv *forgeconntest.Server) {
	t.Helper()
	if u := srv.Unhandled(); len(u) != 0 {
		t.Errorf("the fake did not serve %q", u)
	}
}

func numbers[T any](rows []T, key func(*T) int) []int {
	out := make([]int, 0, len(rows))
	for i := range rows {
		out = append(out, key(&rows[i]))
	}
	return out
}

func prNumber(p *forge.PullRequest) int { return p.Key.Number }

func whole[T any](t *testing.T, l *forge.Listing[T]) {
	t.Helper()
	if l.End != forge.EndWhole || l.Cut != "" {
		t.Errorf("listing ended %q cut %q, want whole", l.End, l.Cut)
	}
}

func TestOpen_names_the_product_account_and_capabilities(t *testing.T) {
	srv, c := open(t, world())
	if c.Product() != forge.ProductGitHub || c.Login() != "octo" {
		t.Errorf("Product %q, Login %q, want github and octo", c.Product(), c.Login())
	}
	if c.CanRerun() != forge.SupportUnknown {
		t.Errorf("CanRerun = %q, want unknown: GitHub enables its runner per repository, so no connection read answers it", c.CanRerun())
	}
	if b := c.Budget(); b.Remaining != 4900 || b.Reset.IsZero() {
		t.Errorf("Budget = %+v, want the 4900 the fake reports and its reset", b)
	}
	handledAll(t, srv)
}

func TestOpen_a_rejected_token_is_unauthorized(t *testing.T) {
	w := world()
	w.RejectToken = true
	srv := forgeconntest.New(forgeconntest.GitHub, &w)
	t.Cleanup(srv.Close)
	cfg := srv.Config(conn)
	_, err := forgeconn.Open(t.Context(), &cfg)
	fe, ok := errors.AsType[*forge.Error](err)
	if !ok || fe.Kind != forge.KindUnauthorized || fe.Status != http.StatusUnauthorized {
		t.Errorf("Open = %v, want unauthorized with status 401", err)
	}
}

func TestOpen_a_cancelled_context_is_the_context_error(t *testing.T) {
	srv := forgeconntest.New(forgeconntest.GitHub, new(world()))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := srv.Config(conn)
	if _, err := forgeconn.Open(ctx, &cfg); !errors.Is(err, context.Canceled) {
		t.Errorf("Open = %v, want context.Canceled", err)
	}
}

func TestRepos_lists_the_owners_repositories(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.Repos(t.Context(), owner)
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	whole(t, &l)
	if len(l.Rows) != 2 {
		t.Fatalf("Repos rows = %+v, want app and lib", l.Rows)
	}
	app, lib := l.Rows[0], l.Rows[1]
	if app.Key != appKey || app.Path != "acme/app" || app.DefaultBranch != "main" || app.WebURL != srv.URL+"/acme/app" {
		t.Errorf("app = %+v, want key %+v, path acme/app, branch main", app, appKey)
	}
	if app.Archived || app.Fork || app.Private {
		t.Errorf("app = %+v, want neither archived, fork nor private", app)
	}
	if lib.Key != libKey || !lib.Archived || !lib.Fork || !lib.Private || lib.DefaultBranch != "trunk" {
		t.Errorf("lib = %+v, want an archived private fork on trunk", lib)
	}
	handledAll(t, srv)
}

func TestRepos_an_unknown_owner_is_owner_unresolved(t *testing.T) {
	_, c := open(t, world())
	_, err := c.Repos(t.Context(), "nobody")
	fe, ok := errors.AsType[*forge.Error](err)
	if !ok || fe.Kind != forge.KindNotFound || fe.Code != forgeapi.CodeOwnerUnresolved {
		t.Errorf("Repos = %v, want not_found with the code owner_unresolved", err)
	}
}

func TestMyPRs_reads_each_open_pull_request_with_its_head(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.MyPRs(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyPRs: %v", err)
	}
	whole(t, &l)
	if !l.Indexed {
		t.Errorf("MyPRs Indexed = false, want true for a search list")
	}
	if got := numbers(l.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
		t.Fatalf("MyPRs numbers = %v, want the open 1, 2 and 4", got)
	}
	one, two, four := l.Rows[0], l.Rows[1], l.Rows[2]
	t.Run("record", func(t *testing.T) {
		if one.Key != appItem(1) || one.State != forge.ItemOpen || one.Title != "Add the thing" || one.Body != "Body." {
			t.Errorf("#1 = %+v, want the open record of app #1", one)
		}
		if one.Author != "octo" || one.SourceBranch != "feature" || one.HeadSHA != passed || one.Comments != 3 {
			t.Errorf("#1 author %q branch %q head %q comments %d", one.Author, one.SourceBranch, one.HeadSHA, one.Comments)
		}
		if !slices.Equal(one.Labels, []string{"bug"}) || !one.CreatedAt.Equal(t0) || !one.UpdatedAt.Equal(t0.Add(time.Hour)) {
			t.Errorf("#1 labels %v created %v updated %v", one.Labels, one.CreatedAt, one.UpdatedAt)
		}
		if one.WebURL != srv.URL+"/acme/app/pull/1" {
			t.Errorf("#1 WebURL = %q", one.WebURL)
		}
		if two.AuthorKind != forge.AccountBot || !two.Draft {
			t.Errorf("#2 author kind %q draft %v, want a bot's draft", two.AuthorKind, two.Draft)
		}
	})
	t.Run("passing_head", func(t *testing.T) {
		h := one.Head
		if !h.Read || h.ForHead != passed || h.Checks != forge.CheckPassing || h.ChecksPassing != 2 || h.ChecksTotal != 2 {
			t.Errorf("#1 head = %+v, want two passing checks read for its head", h)
		}
		if h.Mergeable != forge.SupportYes || h.MergeBlocked != forge.MergeBlockNone || h.AutoMergeArmed != forge.SupportNo {
			t.Errorf("#1 merge state = %+v", h)
		}
		if h.Approvals != 2 || h.ChangesRequested != 1 || h.Queue != forge.QueueNone || h.QueuePosition != forge.CountUnknown {
			t.Errorf("#1 reviews and queue = %+v", h)
		}
	})
	t.Run("failing_head", func(t *testing.T) {
		h := two.Head
		if !h.Read || h.Checks != forge.CheckFailing || h.ChecksFailing != 1 || h.ChecksPending != 1 || h.ChecksPassing != 1 || h.ChecksTotal != 3 {
			t.Errorf("#2 head = %+v, want one failing, one pending and one passing check", h)
		}
		if h.Mergeable != forge.SupportNo || h.MergeBlocked != forge.MergeBlockConflicts || h.AutoMergeArmed != forge.SupportYes {
			t.Errorf("#2 merge state = %+v, want conflicts with auto-merge armed", h)
		}
	})
	t.Run("no_check", func(t *testing.T) {
		if h := four.Head; !h.Read || h.Checks != forge.CheckNone || h.ChecksTotal != 0 {
			t.Errorf("#4 head = %+v, want a read head with no check", h)
		}
	})
	handledAll(t, srv)
}

func TestMyPRs_a_refused_rollup_reads_the_head_with_checks_refused(t *testing.T) {
	w := world()
	w.Repos[0].PRs[0].ChecksForbidden = true
	_, c := open(t, w)
	l, err := c.MyPRs(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyPRs: %v", err)
	}
	whole(t, &l)
	h := l.Rows[0].Head
	if !h.Read || !h.ChecksRefused || h.Checks != forge.CheckUnknown || h.ChecksTotal != 0 {
		t.Errorf("#1 head = %+v, want read with checks refused and no count", h)
	}
	if l.Rows[1].Head.ChecksRefused {
		t.Errorf("#2 head = %+v, want its checks answered", l.Rows[1].Head)
	}
}

func TestMyIssues_reads_each_open_issue(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.MyIssues(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyIssues: %v", err)
	}
	whole(t, &l)
	if !l.Indexed || len(l.Rows) != 1 {
		t.Fatalf("MyIssues = %+v, want the one open issue from the search index", l)
	}
	is := l.Rows[0]
	if is.Key != appItem(10) || is.State != forge.ItemOpen || is.Title != "Broken" || is.Body != "It broke." || is.Comments != 2 {
		t.Errorf("issue = %+v, want app #10", is)
	}
	if is.Author != "someone" || !slices.Equal(is.Labels, []string{"bug"}) || is.WebURL != srv.URL+"/acme/app/issues/10" {
		t.Errorf("issue author %q labels %v url %q", is.Author, is.Labels, is.WebURL)
	}
	handledAll(t, srv)
}

func TestRepoPRs_reads_the_repositorys_open_pull_requests(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.RepoPRs(t.Context(), appKey)
	if err != nil {
		t.Fatalf("RepoPRs: %v", err)
	}
	whole(t, &l)
	if l.Indexed {
		t.Errorf("RepoPRs Indexed = true, want false for a repository list")
	}
	if got := numbers(l.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
		t.Fatalf("RepoPRs numbers = %v, want 1, 2 and 4", got)
	}
	if h := l.Rows[1].Head; !h.Read || h.Checks != forge.CheckFailing || h.ChecksTotal != 3 {
		t.Errorf("#2 head = %+v, want the exact fold of three checks", h)
	}
	handledAll(t, srv)
}

func TestRepoPRs_an_id_forge_scout_never_read_is_invalid(t *testing.T) {
	srv, c := open(t, world())
	sent := len(srv.Requests())
	_, err := c.RepoPRs(t.Context(), forge.RepoKey{Connection: conn, Repo: "acme/app"})
	fe, ok := errors.AsType[*forge.Error](err)
	if !ok || fe.Kind != forge.KindInvalid || fe.Code != forgeapi.CodeRepoRefInvalid {
		t.Errorf("RepoPRs = %v, want invalid with the code repo_ref_invalid", err)
	}
	if got := srv.Requests()[sent:]; len(got) != 0 {
		t.Errorf("requests = %q, want none sent", got)
	}
}

func TestRunsSince_reads_the_runs_created_since(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.RunsSince(t.Context(), appKey, t0)
	if err != nil {
		t.Fatalf("RunsSince: %v", err)
	}
	whole(t, &l)
	if len(l.Rows) != 2 {
		t.Fatalf("RunsSince rows = %+v, want runs 103 and 102", l.Rows)
	}
	pending, failing := l.Rows[0], l.Rows[1]
	if pending.ID != 103 || pending.State != forge.CheckPending || pending.Workflow != "Release" || pending.Trigger != forge.TriggerManual {
		t.Errorf("run 103 = %+v, want a pending manual run titled Release", pending)
	}
	if failing.ID != 102 || failing.Number != 2 || failing.State != forge.CheckFailing || failing.Trigger != forge.TriggerSchedule {
		t.Errorf("run 102 = %+v, want a failed scheduled run", failing)
	}
	if failing.Repo != appKey || failing.WorkflowPath != ".github/workflows/ci.yaml" || failing.Branch != "main" || failing.HeadSHA != passed {
		t.Errorf("run 102 repo %+v path %q branch %q head %q", failing.Repo, failing.WorkflowPath, failing.Branch, failing.HeadSHA)
	}
	if !failing.CreatedAt.Equal(t0) || !failing.StartedAt.Equal(t0) || !failing.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("run 102 times %v %v %v", failing.CreatedAt, failing.StartedAt, failing.UpdatedAt)
	}
	if failing.WebURL != srv.URL+"/acme/app/actions/runs/102" {
		t.Errorf("run 102 WebURL = %q", failing.WebURL)
	}
	handledAll(t, srv)
}

func TestLabels_lists_the_repositorys_labels(t *testing.T) {
	srv, c := open(t, world())
	l, err := c.Labels(t.Context(), appKey)
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	whole(t, &l)
	if !slices.Equal(l.Rows, []forge.Label{{Name: "bug", Color: "ededed"}, {Name: "ready", Color: "ededed"}}) {
		t.Errorf("Labels = %+v, want bug and ready", l.Rows)
	}
	handledAll(t, srv)
}

func TestAffordances_reads_what_the_repository_allows(t *testing.T) {
	srv, c := open(t, world())
	a, err := c.Affordances(t.Context(), appKey)
	if err != nil {
		t.Fatalf("Affordances: %v", err)
	}
	if a.DefaultBranch != "main" || a.CanPush != forge.SupportYes || !slices.Equal(a.MergeStrategies, []string{"merge", "squash"}) {
		t.Errorf("Affordances = %+v, want main, push and the merge and squash strategies", a)
	}
	handledAll(t, srv)
}

func TestReadPR_reads_one_pull_request_in_any_state(t *testing.T) {
	srv, c := open(t, world())
	pr, err := c.ReadPR(t.Context(), appItem(3))
	if err != nil {
		t.Fatalf("ReadPR: %v", err)
	}
	if pr.Key != appItem(3) || pr.State != forge.ItemMerged || pr.Title != "Merged" {
		t.Errorf("ReadPR = %+v, want merged app #3", pr)
	}
	if h := pr.Head; !h.Read || h.Checks != forge.CheckPassing || h.ChecksTotal != 2 || h.Approvals != 0 {
		t.Errorf("#3 head = %+v, want the passing fold with reviews read", h)
	}
	_, err = c.ReadPR(t.Context(), appItem(99))
	if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindNotFound {
		t.Errorf("ReadPR of a missing number = %v, want not_found", err)
	}
	handledAll(t, srv)
}

func TestReadIssue_reads_one_issue_in_any_state(t *testing.T) {
	srv, c := open(t, world())
	is, err := c.ReadIssue(t.Context(), appItem(11))
	if err != nil {
		t.Fatalf("ReadIssue: %v", err)
	}
	if is.Key != appItem(11) || is.State != forge.ItemClosed || is.Title != "Fixed" || is.Comments != 0 {
		t.Errorf("ReadIssue = %+v, want closed app #11", is)
	}
	_, err = c.ReadIssue(t.Context(), appItem(1))
	if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindNotFound {
		t.Errorf("ReadIssue of a pull request = %v, want not_found", err)
	}
	handledAll(t, srv)
}

func TestMergeStatus_reads_whether_the_pull_request_merged(t *testing.T) {
	srv, c := open(t, world())
	for _, tc := range []struct {
		name   string
		number int
		want   forge.Support
	}{{"merged", 3, forge.SupportYes}, {"open", 1, forge.SupportNo}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := c.MergeStatus(t.Context(), appItem(tc.number))
			if err != nil {
				t.Fatalf("MergeStatus: %v", err)
			}
			if s.Merged != tc.want || s.Queue != forge.QueueNone || s.WebURL != srv.URL+"/acme/app/pull/"+strconv.Itoa(tc.number) {
				t.Errorf("MergeStatus = %+v, want merged %q", s, tc.want)
			}
		})
	}
	handledAll(t, srv)
}

func TestReads_a_walk_past_its_ceiling_is_cut_at_the_ceiling(t *testing.T) {
	w := world()
	w.PageSize = 1
	for _, name := range []string{"r3", "r4", "r5", "r6", "r7", "r8", "r9", "r10", "r11"} {
		w.Repos = append(w.Repos, forgeconntest.Repo{Name: name, DefaultBranch: "main"})
	}
	srv, c := open(t, w)
	l, err := c.Repos(t.Context(), owner)
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	if l.End != forge.EndCut || l.Cut != forge.CutCeiling || len(l.Rows) != 10 {
		t.Errorf("Repos ended %q cut %q with %d rows, want cut at the ceiling of 10 pages", l.End, l.Cut, len(l.Rows))
	}
	handledAll(t, srv)
}

func TestReads_a_walk_follows_every_page_under_the_ceiling(t *testing.T) {
	w := world()
	w.PageSize = 1
	srv, c := open(t, w)
	prs, err := c.MyPRs(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyPRs: %v", err)
	}
	whole(t, &prs)
	if got := numbers(prs.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
		t.Errorf("MyPRs over pages of one = %v, want 1, 2 and 4", got)
	}
	labels, err := c.Labels(t.Context(), appKey)
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	whole(t, &labels)
	if len(labels.Rows) != 2 {
		t.Errorf("Labels over pages of one = %+v, want both", labels.Rows)
	}
	handledAll(t, srv)
}

func TestReads_a_graphql_partial_page_is_cut(t *testing.T) {
	w := world()
	w.GraphQLErrors = true
	_, c := open(t, w)
	l, err := c.MyPRs(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyPRs: %v", err)
	}
	if l.End != forge.EndCut || l.Cut != forge.CutGraphQLPartial || len(l.Rows) != 3 {
		t.Errorf("MyPRs ended %q cut %q with %d rows, want the rows kept, cut graphql_partial", l.End, l.Cut, len(l.Rows))
	}
}

func TestReads_a_read_at_the_reserve_is_cut_and_sends_nothing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining int
		want      forge.CutReason
	}{{"budget_left", forge.ReadReserve - 50, forge.CutDeferred}, {"budget_spent", 0, forge.CutRateLimited}} {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := open(t, world())
			srv.SetBudget(tc.remaining, time.Now().Add(time.Hour))
			if _, err := c.Labels(t.Context(), appKey); err != nil {
				t.Fatalf("Labels before the reserve: %v", err)
			}
			sent := len(srv.Requests())
			l, err := c.Labels(t.Context(), appKey)
			if err != nil {
				t.Fatalf("Labels at the reserve: %v", err)
			}
			if l.End != forge.EndCut || l.Cut != tc.want || len(l.Rows) != 0 {
				t.Errorf("Labels ended %q cut %q with %d rows, want cut %q", l.End, l.Cut, len(l.Rows), tc.want)
			}
			if n := len(srv.Requests()); n != sent {
				t.Errorf("requests after the reserve = %q, want none sent", srv.Requests()[sent:])
			}
		})
	}
}

func TestReads_an_injected_rate_limit_is_rate_limited_with_its_wait(t *testing.T) {
	srv, c := open(t, world())
	srv.Answer("GET /api/v3/repos/acme/app/labels", http.StatusForbidden,
		http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"4000"}}, "You have exceeded a secondary rate limit.")
	_, err := c.Labels(t.Context(), appKey)
	fe, ok := errors.AsType[*forge.Error](err)
	if !ok || fe.Kind != forge.KindRateLimited || fe.Status != http.StatusForbidden || fe.RetryAfter != time.Minute {
		t.Fatalf("Labels = %v, want rate_limited at 403 with a one-minute wait", err)
	}
	if l, err := c.Labels(t.Context(), appKey); err != nil || len(l.Rows) != 2 {
		t.Errorf("Labels after the one injected answer = %+v, %v, want the labels", l, err)
	}
}

func TestFake_edits_move_the_record_as_the_forge_does(t *testing.T) {
	srv, c := open(t, world())
	srv.EditPR("app", 1, false, func(it *forgeconntest.Item) { it.Title = "Retitled" })
	srv.PushHead("app", 2, bare)
	srv.SetChecks("app", bare, "pending")
	l, err := c.RepoPRs(t.Context(), appKey)
	if err != nil {
		t.Fatalf("RepoPRs: %v", err)
	}
	one, two := l.Rows[0], l.Rows[1]
	if one.Title != "Retitled" || !one.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("#1 title %q updated %v, want retitled with its updated time kept", one.Title, one.UpdatedAt)
	}
	if two.HeadSHA != bare || !two.UpdatedAt.After(t0) || two.Head.ForHead != bare || two.Head.Checks != forge.CheckPending {
		t.Errorf("#2 = %+v, want the pushed head, a newer updated time and its pending check", two)
	}
	srv.EditIssue("app", 10, true, func(it *forgeconntest.Item) { it.State = "closed" })
	is, err := c.ReadIssue(t.Context(), appItem(10))
	if err != nil || is.State != forge.ItemClosed || !is.UpdatedAt.After(t0) {
		t.Errorf("ReadIssue after closing = %+v, %v, want closed with a newer updated time", is, err)
	}
}
