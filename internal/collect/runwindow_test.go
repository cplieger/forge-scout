package collect

import (
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
)

// The run window is exactly lookback long: the listing, its cover, the
// coverage-gap check and the store's prune all start lookback before the
// scan, to the second, wherever in an hour or a day the scan starts.

func TestScan_lists_runs_from_exactly_lookback_before_the_scan(t *testing.T) {
	ep := githubEndpoint("gh")
	h := newHarness(t, ep)
	for _, at := range []time.Duration{0, 17*time.Minute + 42*time.Second, 62 * time.Minute, 13*time.Hour + 5*time.Second} {
		*h.clock = now.Add(at)
		h.scan(t)
		if got, want := ep.conn.since["o/r"], h.clock.Add(-72*time.Hour); !got.Equal(want) {
			t.Errorf("scan at %s listed runs since %s, want %s, lookback before the scan", stamp(*h.clock), stamp(got), stamp(want))
		}
	}
}

func TestScan_a_listing_older_than_lookback_is_a_coverage_gap_to_the_second(t *testing.T) {
	for _, tc := range []struct {
		name    string
		after   time.Duration
		wantGap int
	}{
		{name: "listed_lookback_ago", after: 72 * time.Hour},
		{name: "listed_half_an_hour_more_ago", after: 72*time.Hour + 30*time.Minute, wantGap: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := githubEndpoint("gh")
			ep.conn.runs = map[string]listing{"o/r": {Runs: []forge.Run{run(1, forge.RunPassing, now.Add(-time.Hour))}}}
			h := newHarness(t, ep)
			h.scan(t)
			*h.clock = now.Add(tc.after)
			h.scan(t)
			if got := lines(h.rec, "runs coverage gap"); len(got) != tc.wantGap {
				t.Errorf("runs coverage gap lines %v after a scan %s later, want %d: the runs created between the window's start and the last listing went unlisted",
					got, tc.after, tc.wantGap)
			}
		})
	}
}

func TestScan_a_listing_covers_from_exactly_lookback_before_the_scan(t *testing.T) {
	ep := githubEndpoint("gh")
	ep.conn.runs = map[string]listing{"o/r": {Runs: []forge.Run{run(1, forge.RunPassing, now.Add(-2*time.Hour))}}}
	h := newHarness(t, ep)
	h.scan(t)
	*h.clock = now.Add(time.Hour)
	ep.conn.runs["o/r"] = listing{Runs: []forge.Run{run(2, forge.RunFailing, now.Add(30*time.Minute))}}
	h.scan(t)
	*h.clock = now.Add(73*time.Hour + 30*time.Minute)
	ep.conn.runs["o/r"] = listing{Runs: []forge.Run{run(3, forge.RunFailing, now.Add(73*time.Hour))}}
	h.scan(t)
	l := lineFor(t, h.rec, "failing workflow", "gh")
	if l["run_id"] != "3" || l["consecutive_failures"] != "1" || l["failing_since_clipped"] != "true" {
		t.Errorf("failing workflow = %v, want run 3, a streak of 1 and clipped: the listing covers from 72h before its scan, half an hour after the last listing, so run 2 may not be the run before it",
			l)
	}
}

func TestScan_the_store_forgets_a_delivered_run_once_it_is_lookback_old(t *testing.T) {
	ep := githubEndpoint("gh")
	aging, fresh := run(1, forge.RunPassing, now.Add(-72*time.Hour+10*time.Minute)), run(2, forge.RunPassing, now.Add(-time.Hour))
	ep.conn.runs = map[string]listing{"o/r": {Runs: []forge.Run{aging, fresh}}}
	h := newHarness(t, ep)
	h.scan(t)
	if got := lineFor(t, h.rec, "scan complete", "gh")["tracked"]; got != "2" {
		t.Fatalf("Setup: tracked after the first scan = %s, want 2", got)
	}
	*h.clock = now.Add(30 * time.Minute)
	h.scan(t)
	if got := lineFor(t, h.rec, "scan complete", "gh")["tracked"]; got != "1" {
		t.Errorf("tracked half an hour later = %s, want 1: run 1 was created 20 minutes before the window's start", got)
	}
}
