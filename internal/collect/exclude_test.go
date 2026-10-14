package collect

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/config"
	"github.com/cplieger/forge-scout/internal/forge"
)

// excludeEndpoint carries two rules: one author on every item, and one
// label on issues alone. Of its pull requests #1 and #3 match the author
// rule and #2 carries the label, which leaves a pull request alone; of its
// issues #4 matches the label rule and #5 none.
func excludeEndpoint() *endpoint {
	ep := githubEndpoint("gh")
	ep.cfg.Exclude = []config.Exclude{
		{Path: "exclude[1]", Author: "tribble-trouble"},
		{Path: "connections[1].exclude[1]", Kind: config.KindIssue, Label: "dependencies"},
	}
	old := now.Add(-100 * 24 * time.Hour)
	ep.conn.prs = map[string][]forge.PullRequest{"o": {
		{Repo: "o/r", Number: 1, Author: "tribble-trouble", HeadSHA: "h1", Labels: []string{"dependencies"}, CreatedAt: old, UpdatedAt: old},
		{Repo: "o/r", Number: 2, Author: "stranger", HeadSHA: "h2", Labels: []string{"dependencies"}, CreatedAt: now, UpdatedAt: now},
		{Repo: "o/r", Number: 3, Author: "Tribble-Trouble", HeadSHA: "h3", Draft: true, CreatedAt: old, UpdatedAt: old},
	}}
	ep.conn.checks = map[string]forge.CheckResult{"h1": {State: "failing"}, "h2": {State: "passing"}, "h3": {State: "failing"}}
	ep.conn.issues = map[string][]forge.Issue{"o": {
		{Repo: "o/r", Number: 4, Author: "stranger", Labels: []string{"Dependencies"}, CreatedAt: old, UpdatedAt: old},
		{Repo: "o/r", Number: 5, Author: "stranger", CreatedAt: now, UpdatedAt: now},
	}}
	return ep
}

func TestScan_exclude_rules_leave_items_out_of_every_row_and_count(t *testing.T) {
	h := newHarness(t, excludeEndpoint())
	if got := h.scan(t); got != Complete {
		t.Fatalf("Scan with exclude rules = %s, want complete: an excluded item is not a missing one; messages %v", got, h.rec.Messages())
	}
	prs, issues := lines(h.rec, "open pull request"), lines(h.rec, "open issue")
	if len(prs) != 1 || prs[0]["number"] != "2" || prs[0]["rank"] != "1" {
		t.Errorf("open pull request lines = %v, want #2 alone, ranked 1", prs)
	}
	if len(issues) != 1 || issues[0]["number"] != "5" || issues[0]["rank"] != "1" {
		t.Errorf("open issue lines = %v, want #5 alone, ranked 1", issues)
	}
	c := lineFor(t, h.rec, "scan complete", "gh")
	want := map[string]string{
		"open_prs": "1", "excluded_prs": "2", "open_issues": "1", "excluded_issues": "1",
		"from_others_prs": "1", "from_others_issues": "1", "failing_checks_prs": "0", "checks_unread": "0",
		"open_age_lt7d_prs": "1", "open_age_90d_plus_prs": "0", "idle_30d_prs": "0",
		"open_age_lt7d_issues": "1", "open_age_90d_plus_issues": "0", "idle_30d_issues": "0",
		"open_prs_read": "complete", "open_issues_read": "complete", "pr_checks_read": "complete",
	}
	for k, v := range want {
		if c[k] != v {
			t.Errorf("scan complete %s = %q, want %q", k, c[k], v)
		}
	}
}

// TestScan_an_author_rule_takes_a_bot_by_either_login holds the rule a REST login
// names to the same App's GraphQL rows, which carry its login without the suffix.
func TestScan_an_author_rule_takes_a_bot_by_either_login(t *testing.T) {
	ep := githubEndpoint("gh")
	ep.cfg.Exclude = []config.Exclude{{Path: "exclude[1]", Author: "tribble-trouble[bot]"}}
	ep.conn.prs = map[string][]forge.PullRequest{"o": {
		{Repo: "o/r", Number: 1, Author: "tribble-trouble", AuthorKind: forge.AccountBot, UpdatedAt: now},
		{Repo: "o/r", Number: 2, Author: "tribble-trouble", AuthorKind: forge.AccountUser, UpdatedAt: now},
	}}
	h := newHarness(t, ep)
	h.scan(t)
	if prs := lines(h.rec, "open pull request"); len(prs) != 1 || prs[0]["number"] != "2" {
		t.Errorf("open pull request lines = %v, want #2 alone: #1 is the App the rule names, #2 a person of that login", prs)
	}
}

func TestScan_exclude_rules_send_no_checks_read_for_an_excluded_pull_request(t *testing.T) {
	ep := excludeEndpoint()
	h := newHarness(t, ep)
	h.scan(t)
	var checks []string
	for _, call := range ep.conn.calls {
		if strings.HasPrefix(call, "checks ") {
			checks = append(checks, call)
		}
	}
	if !slices.Equal(checks, []string{"checks h2"}) {
		t.Errorf("checks reads = %v, want only the kept pull request's, checks h2", checks)
	}
}

func TestScan_exclude_rules_name_the_rule_that_left_an_item_out(t *testing.T) {
	h := newHarness(t, excludeEndpoint())
	h.scan(t)
	got := map[string]string{}
	for _, l := range lines(h.rec, "excluded item") {
		if l["level"] != "DEBUG" {
			t.Errorf("excluded item line %v, want DEBUG", l)
		}
		got[l["kind"]+" "+l["number"]] = l["rule"]
	}
	want := map[string]string{"pull request 1": "exclude[1]", "pull request 3": "exclude[1]", "issue 4": "connections[1].exclude[1]"}
	if len(got) != len(want) {
		t.Errorf("excluded item lines = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("excluded item %s rule = %q, want %q", k, got[k], v)
		}
	}
}
