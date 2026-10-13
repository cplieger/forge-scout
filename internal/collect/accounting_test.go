package collect

import (
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/github-scout/internal/forge"
)

// requestKeys are the request fields one scan line carries, read whole.
var requestKeys = []string{
	"requests_sent", "requests_rest", "requests_graphql", "points_graphql",
	"requests_open", "requests_open_graphql", "requests_discovery", "requests_discovery_graphql",
	"requests_prs", "requests_prs_graphql", "requests_issues", "requests_issues_graphql",
	"requests_runs", "requests_runs_graphql", "requests_checks", "requests_checks_graphql",
	"requests_code_scanning", "requests_code_scanning_graphql", "requests_workflows", "requests_workflows_graphql",
}

func requestFields(l map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range requestKeys {
		out[k] = l[k]
	}
	return out
}

func checkFields(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s %s = %q, want %q", what, k, got[k], v)
		}
	}
}

func TestScan_logs_what_each_read_family_sent_on_each_surface(t *testing.T) {
	ep := readStateEndpoint()
	ep.conn.points = map[string]int{"discover": 1, "prs": 3, "issues": 2, "checks": 1}
	h := newHarness(t, ep)
	h.scan(t)
	first := map[string]string{
		"requests_sent": "8", "requests_rest": "4", "requests_graphql": "4", "points_graphql": "7",
		"requests_open": "1", "requests_open_graphql": "0",
		"requests_discovery": "0", "requests_discovery_graphql": "1",
		"requests_prs": "0", "requests_prs_graphql": "1",
		"requests_issues": "0", "requests_issues_graphql": "1",
		"requests_runs": "1", "requests_runs_graphql": "0",
		"requests_checks": "0", "requests_checks_graphql": "1",
		"requests_code_scanning": "1", "requests_code_scanning_graphql": "0",
		"requests_workflows": "1", "requests_workflows_graphql": "0",
	}
	checkFields(t, "first scan complete", requestFields(lineFor(t, h.rec, "scan complete", "gh")), first)
	h.scan(t)
	second := map[string]string{"requests_sent": "7", "requests_rest": "3", "requests_open": "0", "requests_runs": "1", "points_graphql": "7"}
	checkFields(t, "second scan complete, on the connection the first opened", requestFields(lineFor(t, h.rec, "scan complete", "gh")), second)
}

func TestScan_a_stopped_scan_logs_the_requests_it_sent(t *testing.T) {
	ep := readStateEndpoint()
	fail(ep, "issues o", forge.ErrReadDeferred)
	h := newHarness(t, ep)
	h.scan(t)
	s := lineFor(t, h.rec, "scan stopped", "gh")
	checkFields(t, "scan stopped", requestFields(s), map[string]string{
		"requests_open": "1", "requests_discovery_graphql": "1", "requests_prs_graphql": "1", "requests_runs": "0",
	})
}

func TestScan_a_forge_without_graphql_logs_its_rest_requests(t *testing.T) {
	ep := giteaEndpoint("gt")
	h := newHarness(t, ep)
	h.scan(t)
	checkFields(t, "scan complete", requestFields(lineFor(t, h.rec, "scan complete", "gt")), map[string]string{
		"requests_sent": "5", "requests_rest": "5", "requests_graphql": "0", "points_graphql": "0",
		"requests_open": "1", "requests_discovery": "1", "requests_prs": "1", "requests_issues": "1", "requests_runs": "1",
		"requests_checks": "0", "requests_code_scanning": "0", "requests_workflows": "0",
	})
}

func TestScan_logs_what_the_read_cache_did_this_scan(t *testing.T) {
	ep := readStateEndpoint()
	ep.conn.cache = forge.ReadCache{Hits: 3, Misses: 4, Saved: 3}
	ep.gh.cache = forge.ReadCache{Hits: 10, Misses: 20, Saved: 10}
	h := newHarness(t, ep)
	h.scan(t)
	cacheFields := func() map[string]string {
		c := lineFor(t, h.rec, "scan complete", "gh")
		return map[string]string{"cache_hits": c["cache_hits"], "cache_misses": c["cache_misses"], "cache_saved": c["cache_saved"]}
	}
	checkFields(t, "first scan complete, both clients' caches", cacheFields(), map[string]string{"cache_hits": "13", "cache_misses": "24", "cache_saved": "13"})
	ep.conn.cache = forge.ReadCache{Hits: 7, Misses: 6, Saved: 7}
	ep.gh.cache = forge.ReadCache{Hits: 15, Misses: 21, Saved: 15}
	h.scan(t)
	checkFields(t, "second scan complete, both clients' caches", cacheFields(), map[string]string{"cache_hits": "9", "cache_misses": "3", "cache_saved": "9"})
}

func TestScan_reuses_each_connections_open_across_scans(t *testing.T) {
	gh, gt := readStateEndpoint(), giteaEndpoint("gt")
	h := newHarness(t, gh, gt)
	for range 3 {
		h.scan(t)
	}
	for _, ep := range []*endpoint{gh, gt} {
		if ep.opens != 1 || ep.conn.closed {
			t.Errorf("connection %s over three scans: opened %d times, closed %t; want one open kept, so its read cache can answer the next scan",
				ep.cfg.Name, ep.opens, ep.conn.closed)
		}
	}
}

func TestScan_a_stop_reports_the_budget_of_the_pool_that_stopped_it(t *testing.T) {
	graphQL := forge.Budget{Remaining: 120, Reset: now.Add(2 * time.Hour)}
	rest := forge.Budget{Remaining: 240, Reset: now.Add(time.Hour)}
	for _, tc := range []struct {
		name, call string
		budget     forge.Budget
		err        error
	}{
		{name: "graphql_reserve", call: "prs o", budget: graphQL, err: forge.ErrReadDeferred},
		{name: "graphql_refusal", call: "issues o", budget: graphQL, err: forge.ErrRateLimited},
		{name: "rest_reserve", call: "alerts o/r", budget: rest, err: forge.ErrReadDeferred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := readStateEndpoint()
			fail(ep, tc.call, &forge.BudgetStopError{Err: tc.err, Budget: tc.budget})
			h := newHarness(t, ep)
			h.scan(t)
			s := lineFor(t, h.rec, "scan stopped", "gh")
			if s["budget_remaining"] != strconv.Itoa(tc.budget.Remaining) || s["budget_reset"] != stamp(tc.budget.Reset) {
				t.Errorf("scan stopped budget = %s %s, want the stopping pool's %d %s, not the pool the scan's other reads report",
					s["budget_remaining"], s["budget_reset"], tc.budget.Remaining, stamp(tc.budget.Reset))
			}
		})
	}
}

func TestScan_a_reopened_connection_logs_its_new_clients_cache_counts(t *testing.T) {
	ep := readStateEndpoint()
	ep.conn.cache = forge.ReadCache{Hits: 5, Misses: 5, Saved: 5}
	h := newHarness(t, ep)
	h.scan(t)
	h.c.Close()
	ep.conn.cache = forge.ReadCache{Hits: 1, Misses: 2, Saved: 1}
	h.scan(t)
	if ep.opens != 2 {
		t.Fatalf("Setup: connection opened %d times, want a reopen after Close", ep.opens)
	}
	c := lineFor(t, h.rec, "scan complete", "gh")
	got := map[string]string{"cache_hits": c["cache_hits"], "cache_misses": c["cache_misses"], "cache_saved": c["cache_saved"]}
	checkFields(t, "scan complete after a reopen", got, map[string]string{"cache_hits": "1", "cache_misses": "2", "cache_saved": "1"})
}
