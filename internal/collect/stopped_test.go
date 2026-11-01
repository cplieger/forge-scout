package collect

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
)

// stopKeys are the keys a scan stopped line may carry beside its _read
// fields: what the stop was and what its own requests cost, never a count or
// a row of the scan before it.
var stopKeys = append([]string{
	"level", "forge", "connection", "scan_id", "reason", "phase", "limit", "hint", "unsupported_signals",
	"degraded", "failed_signals", "budget_remaining", "budget_reset", "scan_interval_s",
	"cache_hits", "cache_misses", "cache_saved",
}, requestKeys...)

func TestScan_a_stop_after_a_complete_scan_logs_no_snapshot_and_stores_none(t *testing.T) {
	gh, gt := githubEndpoint("gh"), giteaEndpoint("gt")
	gh.conn.prs = map[string][]forge.PullRequest{"o": {{Repo: "o/r", Number: 1, Ref: "#1", Title: "t", Author: "x", HeadSHA: "h1", CreatedAt: now, UpdatedAt: now}}}
	gh.conn.checks = map[string]forge.CheckResult{"h1": {State: forge.CheckFailing}}
	gh.conn.issues = map[string][]forge.Issue{"o": {{Repo: "o/r", Number: 2, Ref: "#2", Title: "t", Author: "me", CreatedAt: now, UpdatedAt: now}}}
	gh.conn.runs = map[string]listing{"o/r": {Runs: []forge.Run{run(1, forge.RunFailing, now.Add(-time.Hour))}}}
	gh.gh.alerts = map[string][]forge.Alert{"o/r": {{Repo: "o/r", Number: 1, Rule: "r", SecuritySeverity: "high", Tool: "CodeQL", Source: "code_scanning", CreatedAt: now}}}
	gh.gh.workflows = map[string][]forge.Workflow{"o/r": {{Repo: "o/r", Name: "CI", Path: ciPath, State: "active"}, {Repo: "o/r", Name: "old", State: "disabled_inactivity"}}}
	h := newHarness(t, gh, gt)
	h.scan(t)
	for _, msg := range []string{"open pull request", "open issue", "failing workflow", "disabled workflow", "security alert", "top security alert", dayMessage, "scan complete"} {
		lineFor(t, h.rec, msg, "gh")
	}

	*h.clock = now.Add(15 * time.Minute)
	gh.conn.errs = map[string]error{"runs o/r": forge.ErrReadDeferred}
	h.scan(t)
	stopped := lineFor(t, h.rec, "scan stopped", "gh")
	if stopped["reason"] != stopReadReserve || stopped["open_prs_read"] != "complete" {
		t.Fatalf("Setup: scan stopped = %v, want a budget stop after a whole pull request read", stopped)
	}
	lineFor(t, h.rec, "scan complete", "gt")
	noLineBesideTheStop(t, h, "gh")
	for k, v := range stopped {
		if !strings.HasSuffix(k, "_read") && !slices.Contains(stopKeys, k) {
			t.Errorf("scan stopped carries %s=%q, want no count or field of the complete scan before it", k, v)
		}
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatalf("read /data: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"state.json", "state.lock"}; !slices.Equal(names, want) {
		t.Errorf("/data holds %v after a complete scan and a stop, want only the run state and its lock %v", names, want)
	}
}

// noLineBesideTheStop fails for any line of the newest scan that carries
// connection's scan_id other than its scan stopped.
func noLineBesideTheStop(t *testing.T, h *harness, connection string) {
	t.Helper()
	for _, msg := range h.rec.Messages() {
		if msg == "scan stopped" {
			continue
		}
		for _, l := range lines(h.rec, msg) {
			if l["connection"] == connection && l["scan_id"] != "" {
				t.Errorf("the stop logged %q for %s with scan_id %s, want no snapshot line, kept copy or summary beside scan stopped: %v", msg, connection, l["scan_id"], l)
			}
		}
	}
}

func TestScan_a_stop_after_an_alert_read_logs_no_security_tool_line(t *testing.T) {
	gh := githubEndpoint("gh")
	gh.conn.repos = map[string][]forge.Repo{"o": {{Path: "o/r", DefaultBranch: "main"}, {Path: "o/s", DefaultBranch: "main"}}}
	gh.gh.alerts = map[string][]forge.Alert{"o/r": {{Repo: "o/r", Number: 1, Rule: "r", SecuritySeverity: "high", Tool: "CodeQL", Source: "code_scanning", CreatedAt: now}}}
	gh.gh.errs = map[string]error{"workflows o/s": forge.ErrReadDeferred}
	h := newHarness(t, gh)
	h.scan(t)
	if stopped := lineFor(t, h.rec, "scan stopped", "gh"); stopped["reason"] != stopReadReserve || stopped["phase"] != "workflows" || stopped["security_alerts_read"] == "unread" {
		t.Fatalf("Setup: scan stopped = %v, want a budget stop in the workflows read after the alerts read", stopped)
	}
	noLineBesideTheStop(t, h, "gh")
}
