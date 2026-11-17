package forgeconn_test

import (
	"context"
	"errors"
	"maps"
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
	conn    = "c1"
	owner   = "acme"
	passed  = "1111111111111111111111111111111111111111"
	failed  = "2222222222222222222222222222222222222222"
	running = "3333333333333333333333333333333333333333"
	bare    = "4444444444444444444444444444444444444444"
)

var (
	t0      = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	appKey  = forge.RepoKey{Connection: conn, Repo: repoID("acme/app")}
	libKey  = forge.RepoKey{Connection: conn, Repo: repoID("acme/lib")}
	appItem = func(n int) forge.ItemKey { return forge.ItemKey{Connection: conn, Repo: appKey.Repo, Number: n} }
)

// products are the fakes every Client method is tested against.
var products = []forgeconntest.Product{forgeconntest.GitHub, forgeconntest.GitLab, forgeconntest.Gitea, forgeconntest.Forgejo}

// methods is each Client method's test, run once per product.
var methods = map[string]func(*testing.T, forgeconntest.Product){
	"Repos":             testRepos,
	"MyPRs":             testMyPRs,
	"MyIssues":          testMyIssues,
	"RepoPRs":           testRepoPRs,
	"RunsSince":         testRunsSince,
	"Labels":            testLabels,
	"Affordances":       testAffordances,
	"ReadPR":            testReadPR,
	"ReadIssue":         testReadIssue,
	"MergeStatus":       testMergeStatus,
	"Merge":             testMerge,
	"CancelAutoMerge":   testCancelAutoMerge,
	"ClosePR":           testClosePR,
	"ReopenPR":          testReopenPR,
	"CloseIssue":        testCloseIssue,
	"ReopenIssue":       testReopenIssue,
	"AddLabels":         testAddLabels,
	"RemoveLabels":      testRemoveLabels,
	"RerunFailedChecks": testRerunFailedChecks,
	"RerunFailedRun":    testRerunFailedRun,
}

// Each test opens its own fake and client: forgeapi keeps a one-second gap
// between writes on a connection, so the write tests run in parallel.
func TestClient_methods_against_each_product(t *testing.T) {
	t.Parallel()
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			for _, name := range slices.Sorted(maps.Keys(methods)) {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					methods[name](t, p)
				})
			}
		})
	}
}

// per is m's value for product p; a product with none fails the test, so a
// product added to products needs every expectation written for it.
func per[T any](t *testing.T, p forgeconntest.Product, m map[forgeconntest.Product]T) T {
	t.Helper()
	v, ok := m[p]
	if !ok {
		t.Fatalf("no expectation written for %s", p)
	}
	return v
}

func repoID(selector string) string { return forgeapi.RepoRef{Selector: selector}.Encode() }

// world is one owner with a repository holding pull requests in every check
// state, issues, runs and labels, and an archived private fork beside it.
// Each head's newest run agrees with its checks, since GitLab reads a head's
// state from its pipeline and GitHub from its checks.
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
					{ID: 102, Number: 2, Workflow: "ci", Branch: "main", HeadSHA: failed, Event: "schedule", Outcome: "failure", Created: t0, Started: t0, Updated: t0.Add(time.Minute)},
					{ID: 103, Number: 3, Workflow: "ci", Title: "Release", Branch: "main", HeadSHA: running, Event: "workflow_dispatch", Outcome: "pending", Created: t0.Add(time.Hour), Updated: t0.Add(time.Hour)},
				},
			},
			{Name: "lib", DefaultBranch: "trunk", Private: true, Archived: true, Fork: true},
		},
	}
}

func open(t *testing.T, p forgeconntest.Product, w forgeconntest.World) (*forgeconntest.Server, *forgeconn.Client) {
	t.Helper()
	srv := forgeconntest.New(p, &w)
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

// webURL is the page of pull request, issue or run n of acme/app on product
// p's fake.
func webURL(t *testing.T, p forgeconntest.Product, srv *forgeconntest.Server, kind string, n int) string {
	t.Helper()
	paths := per(t, p, map[forgeconntest.Product]map[string]string{
		forgeconntest.GitHub:  {"pr": "/acme/app/pull/", "issue": "/acme/app/issues/", "run": "/acme/app/actions/runs/"},
		forgeconntest.GitLab:  {"pr": "/acme/app/-/merge_requests/", "issue": "/acme/app/-/issues/", "run": "/acme/app/-/pipelines/"},
		forgeconntest.Gitea:   {"pr": "/acme/app/pulls/", "issue": "/acme/app/issues/", "run": "/acme/app/actions/runs/"},
		forgeconntest.Forgejo: {"pr": "/acme/app/pulls/", "issue": "/acme/app/issues/", "run": "/acme/app/actions/runs/"},
	})
	return srv.URL + paths[kind] + strconv.Itoa(n)
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

// heads compares the head-keyed half of each row with want, in order.
func heads(t *testing.T, rows []forge.PullRequest, want []forge.Head) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d", len(rows), len(want))
	}
	for i := range rows {
		if rows[i].Head != want[i] {
			t.Errorf("#%d head = %+v, want %+v", rows[i].Key.Number, rows[i].Head, want[i])
		}
	}
}

// The head halves of #1, #2 and #4 of world as each product reads them on a
// repository's list. GitHub, Gitea and Forgejo fold the head's checks with
// their counts, so a head with none reads as none; GitLab answers its head
// pipeline's one status and no count, so a head with no pipeline reads as
// unknown, and it reads a draft as blocked before a conflict. Gitea and
// Forgejo answer a mergeable flag with no reason and no armed merge.
var (
	ghHeads = []forge.Head{
		{
			ForHead: passed, Checks: forge.CheckPassing, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockNone,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, ChecksPassing: 2, ChecksTotal: 2,
			QueuePosition: forge.CountUnknown, Approvals: 2, ChangesRequested: 1, Read: true,
		},
		{
			ForHead: failed, Checks: forge.CheckFailing, Mergeable: forge.SupportNo, MergeBlocked: forge.MergeBlockConflicts,
			AutoMergeArmed: forge.SupportYes, Queue: forge.QueueNone, ChecksPassing: 1, ChecksFailing: 1, ChecksPending: 1, ChecksTotal: 3,
			QueuePosition: forge.CountUnknown, Read: true,
		},
		{
			ForHead: bare, Checks: forge.CheckNone, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockNone,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Read: true,
		},
	}
	glHeads = []forge.Head{
		{
			ForHead: passed, Checks: forge.CheckPassing, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockNone,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown,
			Approvals: 2, ChangesRequested: 1, Read: true,
		},
		{
			ForHead: failed, Checks: forge.CheckFailing, Mergeable: forge.SupportNo, MergeBlocked: forge.MergeBlockDraft,
			AutoMergeArmed: forge.SupportYes, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Read: true,
		},
		{
			ForHead: bare, Checks: forge.CheckUnknown, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockNone,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Read: true,
		},
	}
	gtHeads = []forge.Head{
		{
			ForHead: passed, Checks: forge.CheckPassing, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockUnknown,
			AutoMergeArmed: forge.SupportUnknown, Queue: forge.QueueNone, ChecksPassing: 2, ChecksTotal: 2,
			QueuePosition: forge.CountUnknown, Approvals: 2, ChangesRequested: 1, Read: true,
		},
		{
			ForHead: failed, Checks: forge.CheckFailing, Mergeable: forge.SupportNo, MergeBlocked: forge.MergeBlockUnknown,
			AutoMergeArmed: forge.SupportUnknown, Queue: forge.QueueNone, ChecksPassing: 1, ChecksFailing: 1, ChecksPending: 1, ChecksTotal: 3,
			QueuePosition: forge.CountUnknown, Read: true,
		},
		{
			ForHead: bare, Checks: forge.CheckNone, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockUnknown,
			AutoMergeArmed: forge.SupportUnknown, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Read: true,
		},
	}
)

// unreviewed is h with no review counted.
func unreviewed(h forge.Head) forge.Head {
	h.Approvals, h.ChangesRequested = 0, 0
	return h
}

func TestOpen_names_the_product_account_and_capabilities(t *testing.T) {
	t.Parallel()
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			srv, c := open(t, p, world())
			product := per(t, p, map[forgeconntest.Product]forge.Product{
				forgeconntest.GitHub: forge.ProductGitHub, forgeconntest.GitLab: forge.ProductGitLab,
				forgeconntest.Gitea: forge.ProductGitea, forgeconntest.Forgejo: forge.ProductForgejo,
			})
			if c.Product() != product || c.Login() != "octo" {
				t.Errorf("Product %q, Login %q, want %q and octo", c.Product(), c.Login(), product)
			}
			// GitHub enables its runner per repository, so no connection read
			// answers it; GitLab carries the retry on every version, and Gitea
			// and Forgejo 17 declare a re-run route in their API document.
			rerun := per(t, p, map[forgeconntest.Product]forge.Support{
				forgeconntest.GitHub: forge.SupportUnknown, forgeconntest.GitLab: forge.SupportYes,
				forgeconntest.Gitea: forge.SupportYes, forgeconntest.Forgejo: forge.SupportYes,
			})
			if c.CanRerun() != rerun {
				t.Errorf("CanRerun = %q, want %q", c.CanRerun(), rerun)
			}
			// Gitea sends no rate-limit header at all.
			reports := per(t, p, map[forgeconntest.Product]bool{
				forgeconntest.GitHub: true, forgeconntest.GitLab: true, forgeconntest.Gitea: false, forgeconntest.Forgejo: true,
			})
			b := c.Budget()
			if reports && (b.Remaining != 4900 || b.Reset.IsZero()) {
				t.Errorf("Budget = %+v, want the 4900 the fake reports and its reset", b)
			}
			if !reports && (b.Remaining != forge.CountUnknown || !b.Reset.IsZero()) {
				t.Errorf("Budget = %+v, want no remaining count and no reset", b)
			}
			handledAll(t, srv)
		})
	}
}

func TestOpen_a_rejected_token_is_unauthorized(t *testing.T) {
	t.Parallel()
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			w := world()
			w.RejectToken = true
			srv := forgeconntest.New(p, &w)
			t.Cleanup(srv.Close)
			cfg := srv.Config(conn)
			_, err := forgeconn.Open(t.Context(), &cfg)
			fe, ok := errors.AsType[*forge.Error](err)
			if !ok || fe.Kind != forge.KindUnauthorized || fe.Status != http.StatusUnauthorized {
				t.Errorf("Open = %v, want unauthorized with status 401", err)
			}
		})
	}
}

func TestOpen_a_cancelled_context_is_the_context_error(t *testing.T) {
	t.Parallel()
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			srv := forgeconntest.New(p, new(world()))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			cfg := srv.Config(conn)
			if _, err := forgeconn.Open(ctx, &cfg); !errors.Is(err, context.Canceled) {
				t.Errorf("Open = %v, want context.Canceled", err)
			}
		})
	}
}

func testRepos(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
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
	t.Run("unknown_owner", func(t *testing.T) {
		_, err := c.Repos(t.Context(), "nobody")
		wantErr(t, err, forge.KindNotFound, forgeapi.CodeOwnerUnresolved)
	})
	handledAll(t, srv)
}

// indexed is whether each product lists an owner's items from a search
// index.
var indexed = map[forgeconntest.Product]bool{
	forgeconntest.GitHub: true, forgeconntest.GitLab: false, forgeconntest.Gitea: false, forgeconntest.Forgejo: false,
}

func testMyPRs(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	l, err := c.MyPRs(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyPRs: %v", err)
	}
	whole(t, &l)
	if want := per(t, p, indexed); l.Indexed != want {
		t.Errorf("MyPRs Indexed = %v, want %v: GitHub lists from its search index, the others from the database", l.Indexed, want)
	}
	if got := numbers(l.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
		t.Fatalf("MyPRs numbers = %v, want the open 1, 2 and 4", got)
	}
	one, two := l.Rows[0], l.Rows[1]
	t.Run("record", func(t *testing.T) {
		if one.Key != appItem(1) || one.State != forge.ItemOpen || one.Title != "Add the thing" || one.Body != "Body." {
			t.Errorf("#1 = %+v, want the open record of app #1", one)
		}
		// The issue search Gitea and Forgejo list from carries no head.
		head := per(t, p, map[forgeconntest.Product][2]string{
			forgeconntest.GitHub: {"feature", passed}, forgeconntest.GitLab: {"feature", passed},
			forgeconntest.Gitea: {}, forgeconntest.Forgejo: {},
		})
		if one.Author != "octo" || one.SourceBranch != head[0] || one.HeadSHA != head[1] || one.Comments != 3 {
			t.Errorf("#1 author %q branch %q head %q comments %d, want octo on %q at %q with 3", one.Author, one.SourceBranch, one.HeadSHA, one.Comments, head[0], head[1])
		}
		if !slices.Equal(one.Labels, []string{"bug"}) || !one.CreatedAt.Equal(t0) || !one.UpdatedAt.Equal(t0.Add(time.Hour)) {
			t.Errorf("#1 labels %v created %v updated %v", one.Labels, one.CreatedAt, one.UpdatedAt)
		}
		if want := webURL(t, p, srv, "pr", 1); one.WebURL != want {
			t.Errorf("#1 WebURL = %q, want %q", one.WebURL, want)
		}
		// GitLab's REST rows and Forgejo's accounts name no author kind.
		kind := per(t, p, map[forgeconntest.Product]forge.AccountKind{
			forgeconntest.GitHub: forge.AccountBot, forgeconntest.GitLab: forge.AccountUnknown,
			forgeconntest.Gitea: forge.AccountBot, forgeconntest.Forgejo: forge.AccountUnknown,
		})
		if two.AuthorKind != kind || !two.Draft {
			t.Errorf("#2 author kind %q draft %v, want a draft by %q", two.AuthorKind, two.Draft, kind)
		}
	})
	t.Run("head", func(t *testing.T) {
		heads(t, l.Rows, per(t, p, map[forgeconntest.Product][]forge.Head{
			forgeconntest.GitHub:  ghHeads,
			forgeconntest.GitLab:  {{}, {}, {}},
			forgeconntest.Gitea:   {{}, {}, {}},
			forgeconntest.Forgejo: {{}, {}, {}},
		}))
	})
	handledAll(t, srv)
}

func testMyIssues(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	l, err := c.MyIssues(t.Context(), owner)
	if err != nil {
		t.Fatalf("MyIssues: %v", err)
	}
	whole(t, &l)
	if want := per(t, p, indexed); l.Indexed != want || len(l.Rows) != 1 {
		t.Fatalf("MyIssues = %+v, want the one open issue, indexed %v", l, want)
	}
	is := l.Rows[0]
	if is.Key != appItem(10) || is.State != forge.ItemOpen || is.Title != "Broken" || is.Body != "It broke." || is.Comments != 2 {
		t.Errorf("issue = %+v, want app #10", is)
	}
	if is.Author != "someone" || !slices.Equal(is.Labels, []string{"bug"}) || is.WebURL != webURL(t, p, srv, "issue", 10) {
		t.Errorf("issue author %q labels %v url %q", is.Author, is.Labels, is.WebURL)
	}
	handledAll(t, srv)
}

func testRepoPRs(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
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
	if l.Rows[0].Title != "Add the thing" || l.Rows[0].WebURL != webURL(t, p, srv, "pr", 1) {
		t.Errorf("#1 = %+v, want app #1's record", l.Rows[0])
	}
	heads(t, l.Rows, per(t, p, map[forgeconntest.Product][]forge.Head{
		forgeconntest.GitHub: ghHeads, forgeconntest.GitLab: glHeads, forgeconntest.Gitea: gtHeads, forgeconntest.Forgejo: gtHeads,
	}))
	handledAll(t, srv)
}

// sameRun compares two runs, their times by instant.
func sameRun(got, want forge.Run) bool {
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.StartedAt.Equal(want.StartedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		return false
	}
	got.CreatedAt, got.StartedAt, got.UpdatedAt = want.CreatedAt, want.StartedAt, want.UpdatedAt
	return got == want
}

func testRunsSince(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	l, err := c.RunsSince(t.Context(), appKey, t0)
	if err != nil {
		t.Fatalf("RunsSince: %v", err)
	}
	whole(t, &l)
	pending := forge.Run{
		CreatedAt: t0.Add(time.Hour), UpdatedAt: t0.Add(time.Hour), Repo: appKey, Workflow: "Release", Branch: "main", HeadSHA: running,
		WebURL: webURL(t, p, srv, "run", 103), Trigger: forge.TriggerManual, State: forge.CheckPending, ID: 103, Number: 3,
	}
	failing := forge.Run{
		CreatedAt: t0, UpdatedAt: t0.Add(time.Minute), Repo: appKey, Branch: "main", HeadSHA: failed,
		WebURL: webURL(t, p, srv, "run", 102), Trigger: forge.TriggerSchedule, State: forge.CheckFailing, ID: 102, Number: 2,
	}
	// GitHub names a run by its workflow and dates its start; a GitLab
	// pipeline has a name only where one is configured, and no start time
	// on the list; Gitea and Forgejo name a run by its workflow file alone.
	switch p {
	case forgeconntest.GitHub:
		pending.WorkflowPath = ".github/workflows/ci.yaml"
		failing.Workflow, failing.WorkflowPath, failing.StartedAt = "ci", ".github/workflows/ci.yaml", t0
	case forgeconntest.Gitea, forgeconntest.Forgejo:
		pending.Workflow, pending.WorkflowPath = "ci", "ci.yml"
		failing.Workflow, failing.WorkflowPath, failing.StartedAt = "ci", "ci.yml", t0
	case forgeconntest.GitLab:
	default:
		t.Fatalf("no expectation written for %s", p)
	}
	if len(l.Rows) != 2 || !sameRun(l.Rows[0], pending) || !sameRun(l.Rows[1], failing) {
		t.Errorf("RunsSince rows = %+v, want %+v and %+v", l.Rows, pending, failing)
	}
	handledAll(t, srv)
}

func testLabels(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	l, err := c.Labels(t.Context(), appKey)
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	whole(t, &l)
	if !slices.Equal(l.Rows, []forge.Label{{Name: "bug", Color: "#ededed"}, {Name: "ready", Color: "#ededed"}}) {
		t.Errorf("Labels = %+v, want bug and ready in #ededed", l.Rows)
	}
	handledAll(t, srv)
}

func testAffordances(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	a, err := c.Affordances(t.Context(), appKey)
	if err != nil {
		t.Fatalf("Affordances: %v", err)
	}
	if a.DefaultBranch != "main" || a.CanPush != forge.SupportYes || !slices.Equal(a.MergeStrategies, []string{"merge", "squash"}) {
		t.Errorf("Affordances = %+v, want main, push and the merge and squash strategies", a)
	}
	handledAll(t, srv)
}

func testReadPR(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	pr, err := c.ReadPR(t.Context(), appItem(3))
	if err != nil {
		t.Fatalf("ReadPR: %v", err)
	}
	if pr.Key != appItem(3) || pr.State != forge.ItemMerged || pr.Title != "Merged" || pr.WebURL != webURL(t, p, srv, "pr", 3) {
		t.Errorf("ReadPR = %+v, want merged app #3", pr)
	}
	// GitLab reads a merge request that is no longer open as blocked.
	want := per(t, p, map[forgeconntest.Product]forge.Head{
		forgeconntest.GitHub: {
			ForHead: passed, Checks: forge.CheckPassing, Mergeable: forge.SupportYes, MergeBlocked: forge.MergeBlockNone,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, ChecksPassing: 2, ChecksTotal: 2, QueuePosition: forge.CountUnknown, Read: true,
		},
		forgeconntest.GitLab: {
			ForHead: passed, Checks: forge.CheckPassing, Mergeable: forge.SupportNo, MergeBlocked: forge.MergeBlockBlocked,
			AutoMergeArmed: forge.SupportNo, Queue: forge.QueueNone, QueuePosition: forge.CountUnknown, Read: true,
		},
		forgeconntest.Gitea:   unreviewed(gtHeads[0]),
		forgeconntest.Forgejo: unreviewed(gtHeads[0]),
	})
	if pr.Head != want {
		t.Errorf("#3 head = %+v, want %+v", pr.Head, want)
	}
	t.Run("missing_number", func(t *testing.T) {
		_, err := c.ReadPR(t.Context(), appItem(99))
		if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindNotFound {
			t.Errorf("ReadPR of a missing number = %v, want not_found", err)
		}
	})
	handledAll(t, srv)
}

func testReadIssue(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
	is, err := c.ReadIssue(t.Context(), appItem(11))
	if err != nil {
		t.Fatalf("ReadIssue: %v", err)
	}
	if is.Key != appItem(11) || is.State != forge.ItemClosed || is.Title != "Fixed" || is.Comments != 0 || is.WebURL != webURL(t, p, srv, "issue", 11) {
		t.Errorf("ReadIssue = %+v, want closed app #11", is)
	}
	t.Run("a_pull_requests_number", func(t *testing.T) {
		_, err := c.ReadIssue(t.Context(), appItem(1))
		if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindNotFound {
			t.Errorf("ReadIssue of a pull request = %v, want not_found", err)
		}
	})
	handledAll(t, srv)
}

func testMergeStatus(t *testing.T, p forgeconntest.Product) {
	srv, c := open(t, p, world())
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
			if s.Merged != tc.want || s.Queue != forge.QueueNone || s.WebURL != webURL(t, p, srv, "pr", tc.number) {
				t.Errorf("MergeStatus = %+v, want merged %q", s, tc.want)
			}
		})
	}
	handledAll(t, srv)
}

func TestMyPRs_a_refused_rollup_reads_the_head_with_checks_refused(t *testing.T) {
	t.Parallel()
	w := world()
	w.Repos[0].PRs[0].ChecksForbidden = true
	_, c := open(t, forgeconntest.GitHub, w)
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

func TestRepoPRs_an_id_forge_scout_never_read_is_invalid(t *testing.T) {
	t.Parallel()
	srv, c := open(t, forgeconntest.GitHub, world())
	sent := len(srv.Requests())
	_, err := c.RepoPRs(t.Context(), forge.RepoKey{Connection: conn, Repo: "acme/app"})
	wantErr(t, err, forge.KindInvalid, forgeapi.CodeRepoRefInvalid)
	if got := srv.Requests()[sent:]; len(got) != 0 {
		t.Errorf("requests = %q, want none sent", got)
	}
}

func TestReads_a_walk_past_its_ceiling_is_cut_at_the_ceiling(t *testing.T) {
	t.Parallel()
	w := world()
	w.PageSize = 1
	for _, name := range []string{"r3", "r4", "r5", "r6", "r7", "r8", "r9", "r10", "r11"} {
		w.Repos = append(w.Repos, forgeconntest.Repo{Name: name, DefaultBranch: "main"})
	}
	srv, c := open(t, forgeconntest.GitHub, w)
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
	t.Parallel()
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			w := world()
			w.PageSize = 1
			srv, c := open(t, p, w)
			mine, err := c.MyPRs(t.Context(), owner)
			if err != nil {
				t.Fatalf("MyPRs: %v", err)
			}
			whole(t, &mine)
			if got := numbers(mine.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
				t.Errorf("MyPRs over pages of one = %v, want 1, 2 and 4", got)
			}
			repo, err := c.RepoPRs(t.Context(), appKey)
			if err != nil {
				t.Fatalf("RepoPRs: %v", err)
			}
			whole(t, &repo)
			if got := numbers(repo.Rows, prNumber); !slices.Equal(got, []int{1, 2, 4}) {
				t.Errorf("RepoPRs over pages of one = %v, want 1, 2 and 4", got)
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
		})
	}
}

func TestReads_a_graphql_partial_page_is_cut(t *testing.T) {
	t.Parallel()
	// GitHub lists the owner's pull requests by GraphQL search, GitLab a
	// repository's by its list document; Gitea and Forgejo read no GraphQL.
	read := map[forgeconntest.Product]func(*forgeconn.Client, context.Context) (forge.Listing[forge.PullRequest], error){
		forgeconntest.GitHub: func(c *forgeconn.Client, ctx context.Context) (forge.Listing[forge.PullRequest], error) {
			return c.MyPRs(ctx, owner)
		},
		forgeconntest.GitLab: func(c *forgeconn.Client, ctx context.Context) (forge.Listing[forge.PullRequest], error) {
			return c.RepoPRs(ctx, appKey)
		},
	}
	for _, p := range slices.Sorted(maps.Keys(read)) {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			w := world()
			w.GraphQLErrors = true
			_, c := open(t, p, w)
			l, err := per(t, p, read)(c, t.Context())
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if l.End != forge.EndCut || l.Cut != forge.CutGraphQLPartial || len(l.Rows) != 3 {
				t.Errorf("list ended %q cut %q with %d rows, want the rows kept, cut graphql_partial", l.End, l.Cut, len(l.Rows))
			}
		})
	}
}

func TestReads_a_read_at_the_reserve_is_cut_and_sends_nothing(t *testing.T) {
	t.Parallel()
	// Gitea reports no budget, so no read stops at a reserve there.
	budgeted := slices.DeleteFunc(slices.Clone(products), func(p forgeconntest.Product) bool { return p == forgeconntest.Gitea })
	for _, p := range budgeted {
		for _, tc := range []struct {
			name      string
			remaining int
			want      forge.CutReason
		}{{"budget_left", forge.ReadReserve - 50, forge.CutDeferred}, {"budget_spent", 0, forge.CutRateLimited}} {
			t.Run(string(p)+"_"+tc.name, func(t *testing.T) {
				t.Parallel()
				srv, c := open(t, p, world())
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
}

func TestReads_an_injected_rate_limit_is_rate_limited_with_its_wait(t *testing.T) {
	t.Parallel()
	// GitHub answers a secondary limit 403 with budget left; GitLab, Gitea
	// and Forgejo answer their throttle 429.
	type limit struct {
		route  string
		status int
		header http.Header
	}
	for _, p := range products {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			srv, c := open(t, p, world())
			l := per(t, p, map[forgeconntest.Product]limit{
				forgeconntest.GitHub:  {"GET /api/v3/repos/acme/app/labels", http.StatusForbidden, http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"4000"}}},
				forgeconntest.GitLab:  {"GET /api/v4/projects/acme%2Fapp/labels", http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}},
				forgeconntest.Gitea:   {"GET /api/v1/repos/acme/app/labels", http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}},
				forgeconntest.Forgejo: {"GET /api/v1/repos/acme/app/labels", http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}},
			})
			srv.Answer(l.route, l.status, l.header, "You have exceeded a secondary rate limit.")
			_, err := c.Labels(t.Context(), appKey)
			fe, ok := errors.AsType[*forge.Error](err)
			if !ok || fe.Kind != forge.KindRateLimited || fe.Status != l.status || fe.RetryAfter != time.Minute {
				t.Fatalf("Labels = %v, want rate_limited at %d with a one-minute wait", err, l.status)
			}
			if got, err := c.Labels(t.Context(), appKey); err != nil || len(got.Rows) != 2 {
				t.Errorf("Labels after the one injected answer = %+v, %v, want the labels", got, err)
			}
		})
	}
}

func TestFake_edits_move_the_record_as_the_forge_does(t *testing.T) {
	t.Parallel()
	srv, c := open(t, forgeconntest.GitHub, world())
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
