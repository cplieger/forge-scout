package main

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/collect"
	"github.com/cplieger/forge-scout/internal/config"
	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/slogx/capture"
)

// identityWorld's runs are given oldest first.
func identityWorld(runs ...forgeconntest.Run) forgeconntest.World {
	t0 := time.Now().UTC().Truncate(time.Second)
	for i := range runs {
		created := t0.Add(time.Duration(i-len(runs)) * time.Hour)
		runs[i].ID, runs[i].Number = int64(i+1), int64(i+1)
		runs[i].Branch, runs[i].Event = "main", "workflow_dispatch"
		runs[i].Created, runs[i].Started, runs[i].Updated = created, created.Add(time.Minute), created.Add(2*time.Minute)
	}
	return forgeconntest.World{
		Login: "me", Owner: "acme",
		Repos: []forgeconntest.Repo{{
			Name: "ci", DefaultBranch: "main", Runs: runs, Alerts: []forgeconntest.Alert{},
			Workflows: []forgeconntest.Workflow{{ID: 1, Name: "Promote", Path: ".github/workflows/promote.yaml", State: "active"}},
		}},
	}
}

func scanOnce(t *testing.T, product forgeconntest.Product, w forgeconntest.World, setup ...func(*forgeconntest.Server)) *capture.Recorder {
	t.Helper()
	rec := capture.Default(t)
	srv := forgeconntest.New(product, w)
	t.Cleanup(srv.Close)
	for _, f := range setup {
		f(srv)
	}
	cfg := config.Config{Lookback: 72 * time.Hour, ScanInterval: 15 * time.Minute, Connections: []config.Connection{srv.Connection("hub")}}
	c := newCollector(t.Context(), &cfg, slog.Default(), testStore(t))
	t.Cleanup(c.Close)
	if got := c.Scan(t.Context()); got != collect.Complete {
		t.Fatalf("scan = %s, want complete; messages %v; unhandled %v", got, rec.Messages(), srv.Unhandled())
	}
	return rec
}

func fields(rec *capture.Recorder, msg string) []map[string]string {
	var out []map[string]string
	for _, r := range rec.Records() {
		if r.Message != msg {
			continue
		}
		l := map[string]string{}
		r.Attrs(func(a slog.Attr) bool {
			l[a.Key] = a.Value.String()
			return true
		})
		out = append(out, l)
	}
	return out
}

func promote(title, outcome string) forgeconntest.Run {
	return forgeconntest.Run{Workflow: "Promote", Title: title, Path: ".github/workflows/promote.yaml", Outcome: outcome}
}

func TestScan_a_github_workflow_whose_runs_carry_their_own_titles_is_one_workflow(t *testing.T) {
	t.Run("a_failure_then_a_success_reads_green", func(t *testing.T) {
		rec := scanOnce(t, forgeconntest.GitHub, identityWorld(
			promote("Promote toolbelt (dry run)", "failure"),
			promote("Promote keyenc", "success"),
		))
		if got := fields(rec, "failing workflow"); len(got) != 0 {
			t.Errorf("failing workflow lines = %v, want none: the newest run of promote.yaml passed", got)
		}
		if got := completeCounts(rec.Records())["hub"]["failing_workflows"]; got != 0 {
			t.Errorf("scan complete failing_workflows = %d, want 0", got)
		}
		for _, l := range fields(rec, "ci run") {
			if l["workflow"] != "Promote" || l["workflow_path"] != ".github/workflows/promote.yaml" {
				t.Errorf("ci run %s = workflow %q, workflow_path %q, want the definition's name Promote and its path", l["run_id"], l["workflow"], l["workflow_path"])
			}
		}
	})
	t.Run("a_success_then_a_failure_reads_failing_under_the_definitions_name", func(t *testing.T) {
		rec := scanOnce(t, forgeconntest.GitHub, identityWorld(
			promote("Promote keyenc", "success"),
			promote("Promote toolbelt (dry run)", "failure"),
		))
		got := fields(rec, "failing workflow")
		if len(got) != 1 {
			t.Fatalf("failing workflow lines = %v, want one for promote.yaml", got)
		}
		if got[0]["workflow"] != "Promote" || got[0]["workflow_path"] != ".github/workflows/promote.yaml" || got[0]["consecutive_failures"] != "1" {
			t.Errorf("failing workflow = %v, want workflow Promote, workflow_path .github/workflows/promote.yaml and one failure", got[0])
		}
	})
	t.Run("titled_runs_are_timed_as_one_workflow", func(t *testing.T) {
		rec := scanOnce(t, forgeconntest.GitHub, identityWorld(
			promote("Promote a", "success"), promote("Promote b", "success"), promote("Promote c", "success"),
		))
		got := fields(rec, "slow workflow")
		if len(got) != 1 || got[0]["workflow"] != "Promote" || got[0]["workflow_path"] != ".github/workflows/promote.yaml" || got[0]["runs"] != "3" {
			t.Errorf("slow workflow lines = %v, want one for Promote over its 3 runs", got)
		}
	})
	t.Run("an_unlisted_definition_keeps_the_runs_own_name", func(t *testing.T) {
		w := identityWorld(promote("Promote keyenc", "failure"))
		w.Repos[0].Workflows = nil
		got := fields(scanOnce(t, forgeconntest.GitHub, w), "failing workflow")
		if len(got) != 1 || got[0]["workflow"] != "Promote keyenc" || got[0]["workflow_path"] != ".github/workflows/promote.yaml" {
			t.Errorf("failing workflow lines = %v, want one named as its run, keyed on its path", got)
		}
	})
}

func TestScan_github_workflow_files_whose_names_hold_an_at_stay_apart(t *testing.T) {
	nightly, beta := ".github/workflows/release@nightly.yml", ".github/workflows/release@beta.yml"
	w := identityWorld(
		forgeconntest.Run{Workflow: "Nightly", Path: nightly + "@main", Outcome: "failure"},
		forgeconntest.Run{Workflow: "Beta", Path: beta + "@main", Outcome: "success"},
	)
	w.Repos[0].Workflows = []forgeconntest.Workflow{
		{ID: 1, Name: "Nightly", Path: nightly, State: "active"},
		{ID: 2, Name: "Beta", Path: beta, State: "active"},
	}
	got := fields(scanOnce(t, forgeconntest.GitHub, w), "failing workflow")
	if len(got) != 1 || got[0]["workflow"] != "Nightly" || got[0]["workflow_path"] != nightly {
		t.Errorf("failing workflow lines = %v, want Nightly alone at %s: a later pass of another file is not its", got, nightly)
	}
}

func TestScan_a_gitea_family_workflow_is_its_file(t *testing.T) {
	for _, product := range []forgeconntest.Product{forgeconntest.Gitea, forgeconntest.Forgejo} {
		t.Run(string(product), func(t *testing.T) {
			rec := scanOnce(t, product, identityWorld(
				forgeconntest.Run{Workflow: "ci", Outcome: "failure"},
				forgeconntest.Run{Workflow: "ci", Outcome: "success"},
			))
			if got := fields(rec, "failing workflow"); len(got) != 0 {
				t.Errorf("failing workflow lines = %v, want none: the newest run of ci.yml passed", got)
			}
			for _, l := range fields(rec, "ci run") {
				if l["workflow"] != "ci" || l["workflow_path"] != "ci.yml" {
					t.Errorf("ci run %s = workflow %q, workflow_path %q, want ci and its file ci.yml", l["run_id"], l["workflow"], l["workflow_path"])
				}
			}
		})
	}
}

func TestScan_a_gitlab_pipeline_is_its_name(t *testing.T) {
	rec := scanOnce(t, forgeconntest.GitLab, identityWorld(
		forgeconntest.Run{Workflow: "nightly", Outcome: "failure"},
		forgeconntest.Run{Workflow: "push", Outcome: "success"},
	))
	got := fields(rec, "failing workflow")
	if len(got) != 1 || got[0]["workflow"] != "nightly" {
		t.Fatalf("failing workflow lines = %v, want nightly alone: a pipeline names no definition, so its name is its identity", got)
	}
	if _, ok := got[0]["workflow_path"]; ok {
		t.Errorf("failing workflow = %v, want no workflow_path: GitLab names no definition", got[0])
	}
}

func TestScan_a_gitlab_project_listing_throttle_under_the_reserve_does_not_stop_the_scan(t *testing.T) {
	rec := scanOnce(t, forgeconntest.GitLab, identityWorld(forgeconntest.Run{Workflow: "push", Outcome: "success"}),
		func(s *forgeconntest.Server) { s.SetProjectsThrottle(100) })
	if got := fields(rec, "scan stopped"); len(got) != 0 {
		t.Errorf("scan stopped lines = %v, want none: the project listings' throttle is not the pool the other reads draw on", got)
	}
}

func TestScan_gitlab_project_listings_spend_the_api_limit_their_answers_do_not_name(t *testing.T) {
	rec := capture.Default(t)
	srv := forgeconntest.New(forgeconntest.GitLab, identityWorld(forgeconntest.Run{Workflow: "push", Outcome: "success"}))
	t.Cleanup(srv.Close)
	srv.SetProjectsThrottle(100)
	srv.SetBudget(forge.ReadReserve+1, time.Now().Add(time.Minute))
	cfg := config.Config{Lookback: 72 * time.Hour, ScanInterval: 15 * time.Minute, Connections: []config.Connection{srv.Connection("hub")}}
	c := newCollector(t.Context(), &cfg, slog.Default(), testStore(t))
	t.Cleanup(c.Close)
	if got := c.Scan(t.Context()); got == collect.Complete {
		t.Fatalf("scan = %s, want it stopped: one project listing took the API limit from %d to the reserve", got, forge.ReadReserve+1)
	}
	got := fields(rec, "scan stopped")
	if len(got) != 1 || got[0]["reason"] != "read_reserve" || got[0]["budget_remaining"] != strconv.Itoa(forge.ReadReserve) {
		t.Errorf("scan stopped lines = %v, want one for read_reserve at budget_remaining %d", got, forge.ReadReserve)
	}
}
