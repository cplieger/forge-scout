package forgeconn

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forge-scout/internal/ghquota"
	"github.com/cplieger/forge-scout/internal/spend"
	"github.com/cplieger/forgeapi"
)

// openMetered opens a fake of product serving w with spent as the
// connection's spend meter.
func openMetered(t *testing.T, product forgeconntest.Product, w forgeconntest.World, spent *spend.Meter) (*Client, *forgeconntest.Server) {
	t.Helper()
	srv := forgeconntest.New(product, w)
	t.Cleanup(srv.Close)
	conn := srv.Connection("test")
	c, err := Open(t.Context(), &conn, slog.New(slog.NewTextHandler(io.Discard, nil)), ghquota.New(nil), spent)
	if err != nil {
		t.Fatalf("Open(%s fake) = %v; requests %v", product, err, srv.Requests())
	}
	t.Cleanup(c.Close)
	return c, srv
}

func TestWire_a_repeated_read_is_revalidated_and_billed_by_its_forge(t *testing.T) {
	for _, tc := range []struct {
		product forgeconntest.Product
		billed  int
		saved   int
	}{
		{product: forgeconntest.GitHub, billed: 0, saved: 1},
		{product: forgeconntest.GitLab, billed: 1, saved: 0},
	} {
		t.Run(string(tc.product), func(t *testing.T) {
			w := world()
			w.Validators = true
			spent := new(spend.Meter)
			c, srv := openMetered(t, tc.product, w, spent)
			d, err := c.Discover(t.Context(), []string{"acme"})
			if err != nil {
				t.Fatalf("Setup: Discover = %v", err)
			}
			api := d.ByOwner["acme"][slices.IndexFunc(d.ByOwner["acme"], func(r forge.Repo) bool { return r.Path == "acme/api" })]
			since := wireNow.Add(-72 * time.Hour)
			first, err := c.RunsSince(t.Context(), api, since)
			if err != nil {
				t.Fatalf("Setup: RunsSince = %v", err)
			}
			cacheBefore, spentBefore, notModified := c.ReadCache(), spent.Spent(), srv.NotModified()
			again, err := c.RunsSince(t.Context(), api, since)
			if err != nil {
				t.Fatalf("second RunsSince = %v", err)
			}
			if !slices.EqualFunc(first.Rows, again.Rows, func(a, b forge.Run) bool { return a.ID == b.ID && a.State == b.State }) {
				t.Errorf("second RunsSince rows = %+v, want the first's %+v served from the held answer", again.Rows, first.Rows)
			}
			if got := srv.NotModified() - notModified; got != 1 {
				t.Fatalf("the second listing was answered 304 %d time(s), want 1", got)
			}
			if got, want := c.ReadCache().Minus(cacheBefore), (forge.ReadCache{Hits: 1, Saved: tc.saved}); got != want {
				t.Errorf("ReadCache across the second listing = %+v, want %+v", got, want)
			}
			if got, want := spent.Spent().Minus(spentBefore), (spend.Spend{Sent: 1, REST: tc.billed}); got != want {
				t.Errorf("spend of the second listing = %+v, want %+v: the 304 is sent, and billed %d", got, want, tc.billed)
			}
		})
	}
}

func TestWire_a_run_listing_asks_one_request_all_day(t *testing.T) {
	w := world()
	w.Validators = true
	spent := new(spend.Meter)
	c, srv := openMetered(t, forgeconntest.GitHub, w, spent)
	d, err := c.Discover(t.Context(), []string{"acme"})
	if err != nil {
		t.Fatalf("Setup: Discover = %v", err)
	}
	api := d.ByOwner["acme"][slices.IndexFunc(d.ByOwner["acme"], func(r forge.Repo) bool { return r.Path == "acme/api" })]
	day := wireNow.Add(-72 * time.Hour).Truncate(24 * time.Hour)
	runRequests := func() []string {
		var out []string
		for _, r := range srv.Requests() {
			if strings.Contains(r, "/actions/runs") {
				out = append(out, r)
			}
		}
		return out
	}
	if _, err := c.RunsSince(t.Context(), api, day.Add(time.Minute)); err != nil {
		t.Fatalf("Setup: RunsSince = %v", err)
	}
	cacheBefore, spentBefore := c.ReadCache(), spent.Spent()
	if _, err := c.RunsSince(t.Context(), api, day.Add(23*time.Hour)); err != nil {
		t.Fatalf("second RunsSince = %v", err)
	}
	if got := runRequests(); len(got) != 2 || got[0] != got[1] {
		t.Errorf("run requests = %v, want one request sent twice: two bounds on one UTC day ask the same listing", got)
	}
	if got, want := c.ReadCache().Minus(cacheBefore), (forge.ReadCache{Hits: 1, Saved: 1}); got != want {
		t.Errorf("ReadCache across the later listing of the day = %+v, want %+v: it revalidates the first", got, want)
	}
	if got := spent.Spent().Minus(spentBefore); got.REST != 0 {
		t.Errorf("spend of the later listing of the day = %+v, want no billed request", got)
	}
}

func TestOpen_counts_every_request_it_sends(t *testing.T) {
	for _, tc := range []struct {
		product forgeconntest.Product
		// unbilled is the other families' detection reads the fake answers
		// 404 with no rate-limit header, which GitHub bills nothing for.
		unbilled int
	}{
		{product: forgeconntest.GitHub, unbilled: 1},
		{product: forgeconntest.GitLab},
		{product: forgeconntest.Forgejo},
	} {
		t.Run(string(tc.product), func(t *testing.T) {
			spent := new(spend.Meter)
			_, srv := openMetered(t, tc.product, world(), spent)
			reqs := srv.Requests()
			docs := 0
			for _, r := range reqs {
				if strings.HasPrefix(r, "POST graphql") {
					docs++
				}
			}
			want := spend.Spend{Sent: len(reqs), REST: len(reqs) - docs - tc.unbilled, Documents: docs}
			if got := spent.Spent(); got != want {
				t.Errorf("spend of the open = %+v, want %+v for its requests %v", got, want, reqs)
			}
		})
	}
}

func TestWire_a_document_is_charged_the_points_its_forge_reports(t *testing.T) {
	spent := new(spend.Meter)
	c, srv := openMetered(t, forgeconntest.GitHub, world(), spent)
	srv.SetGraphQLCost(4)
	before := spent.Spent()
	if _, err := c.OpenPRs(t.Context(), "acme"); err != nil {
		t.Fatalf("OpenPRs = %v", err)
	}
	if got, want := spent.Spent().Minus(before), (spend.Spend{Sent: 1, Documents: 1, Points: 4}); got != want {
		t.Errorf("spend of one search document = %+v, want %+v", got, want)
	}
}

func TestWire_a_deferred_read_carries_the_budget_that_held_it(t *testing.T) {
	c, srv := openMetered(t, forgeconntest.GitLab, world(), new(spend.Meter))
	reset := wireNow.Add(2 * time.Hour)
	srv.SetBudget(forge.ReadReserve-10, reset)
	if _, err := c.Discover(t.Context(), []string{"acme"}); err != nil {
		t.Fatalf("Setup: Discover = %v", err)
	}
	_, err := c.OpenPRs(t.Context(), "acme")
	bs, ok := errors.AsType[*forge.BudgetStopError](err)
	if !errors.Is(err, forge.ErrReadDeferred) || !ok || bs.Budget.Remaining != forge.ReadReserve-10 || !bs.Budget.Reset.Equal(reset) {
		t.Errorf("OpenPRs under the reserve = %v, want forge.ErrReadDeferred carrying the budget %d and reset %s that held it", err, forge.ReadReserve-10, reset)
	}
}

func TestReads_a_rate_limit_forgeapi_answers_carries_its_budget(t *testing.T) {
	reset := wireNow.Add(time.Hour)
	for name, fail := range map[string]*forgeapi.Error{
		"refused":  {Kind: forgeapi.KindRateLimited, Status: http.StatusTooManyRequests},
		"deferred": {Kind: forgeapi.KindRateLimited},
	} {
		t.Run(name, func(t *testing.T) {
			c := client(t, &fakeAPI{
				product: forgeapi.ProductGitLab, budget: forgeapi.BudgetState{Remaining: 3, Reset: reset},
				runs: func(...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
					return forgeapi.Page[forgeapi.Run]{}, fail
				},
			})
			_, err := c.RunsSince(t.Context(), forge.Repo{Path: "o/r"}.WithHandle(forgeapi.RepoRef{}), wireNow)
			bs, ok := errors.AsType[*forge.BudgetStopError](err)
			if !ok || bs.Budget.Remaining != 3 || !bs.Budget.Reset.Equal(reset) {
				t.Errorf("RunsSince on %s = %v, want it carrying the budget forgeapi reports, 3 until %s", name, err, reset)
			}
		})
	}
}

// documents sends n GraphQL documents through spent, as forgeapi's attempt
// observer sees them.
func documents(t *testing.T, spent *spend.Meter, n int) {
	t.Helper()
	answer := spent.Observe(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	}))
	for range n {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.github.test/graphql", http.NoBody)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		resp, err := answer.RoundTrip(req)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		resp.Body.Close()
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPoints_a_read_of_several_documents_charges_each_earlier_one_a_point(t *testing.T) {
	spent := new(spend.Meter)
	f := &fakeAPI{
		product: forgeapi.ProductGitHub, budget: forgeapi.BudgetState{Remaining: 4000, LastCost: 4},
		myPRs: func(...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
			documents(t, spent, 2)
			return prPage(7, nil), nil
		},
	}
	c, err := newClient(t.Context(), f, forgeapi.FamilyGitHub, []string{"o"}, spent, func(forge.Product) {})
	if err != nil {
		t.Fatalf("Setup: newClient = %v", err)
	}
	if _, err := c.OpenPRs(t.Context(), "o"); err != nil {
		t.Fatalf("OpenPRs = %v", err)
	}
	if got := spent.Spent().Points; got != 5 {
		t.Errorf("Points of a read that sent two documents, the last priced 4 = %d, want 5: the earlier one charged 1", got)
	}
}

func TestPoints_a_checks_read_charges_its_document(t *testing.T) {
	spent := new(spend.Meter)
	f := &fakeAPI{
		product: forgeapi.ProductGitHub, budget: forgeapi.BudgetState{Remaining: 4000, LastCost: 4},
		status: func(string) (forgeapi.CommitChecks, error) {
			documents(t, spent, 1)
			return forgeapi.CommitChecks{State: forgeapi.CheckPassing, Passing: 1, Total: 1}, nil
		},
	}
	c, err := newClient(t.Context(), f, forgeapi.FamilyGitHub, []string{"o"}, spent, func(forge.Product) {})
	if err != nil {
		t.Fatalf("Setup: newClient = %v", err)
	}
	pr := forge.PullRequest{HeadSHA: "abc"}
	pr.SetRepoHandle(forgeapi.RepoRef{})
	if _, err := c.CommitChecks(t.Context(), &pr); err != nil {
		t.Fatalf("CommitChecks = %v", err)
	}
	if got := spent.Spent().Points; got != 4 {
		t.Errorf("Points of one checks document priced 4 = %d, want 4", got)
	}
}
