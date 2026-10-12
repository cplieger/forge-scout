package githubrest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzDecodeCodeAlerts drives the code-scanning page decode with untrusted
// bytes: a decoded page is an array of at most one page's rows, each mapped to
// repo's code-scanning alert with each severity one of GitHub's values, empty
// or unknown, and null is never a page.
func FuzzDecodeCodeAlerts(f *testing.F) {
	f.Add(`[{"number":3,"rule":{"id":"x","security_severity_level":"high"},"tool":{"name":"CodeQL"}}]`)
	f.Add(`[{"number":4,"rule":{"id":"dead","severity":"error","security_severity_level":null},"tool":{"name":"deadset"}}]`)
	f.Add(`[{"number":5,"rule":{"severity":"Warning","security_severity_level":"extreme"}}]`)
	f.Add(`[{"number":6,"rule":{"severity":7}}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(``)
	f.Add(`[{"number":-1}]`)
	f.Add(`{"not":"an array"}`)
	f.Add("[" + strings.Repeat(`{"number":1},`, perPage) + `{"number":2}]`)
	security := map[string]bool{"": true, "low": true, "medium": true, "high": true, "critical": true, "unknown": true}
	problem := map[string]bool{"": true, "none": true, "note": true, "warning": true, "error": true, "unknown": true}
	f.Fuzz(func(t *testing.T, data string) {
		alerts, err := decodeAlerts("o/r", []byte(data))
		if err != nil {
			return
		}
		if bytes.Equal(bytes.TrimSpace([]byte(data)), []byte("null")) {
			t.Fatalf("decodeAlerts(null) = %d alerts, nil error, want a decode failure", len(alerts))
		}
		if len(alerts) > perPage {
			t.Fatalf("decodeAlerts = %d alerts, nil error, want a page of at most %d rows", len(alerts), perPage)
		}
		for _, a := range alerts {
			if a.Repo != "o/r" || a.Source != "code_scanning" {
				t.Fatalf("decodeAlerts(%q) row = %+v, want repo o/r and source code_scanning", data, a)
			}
			if !security[a.SecuritySeverity] || !problem[a.ProblemSeverity] {
				t.Fatalf("decodeAlerts(%q) severities %q, %q, want GitHub's values, empty or unknown", data, a.SecuritySeverity, a.ProblemSeverity)
			}
		}
		var oracle []struct {
			Rule struct {
				Severity              *string `json:"severity"`
				SecuritySeverityLevel *string `json:"security_severity_level"`
			} `json:"rule"`
		}
		if json.Unmarshal([]byte(data), &oracle) != nil || len(oracle) != len(alerts) {
			return
		}
		for i, o := range oracle {
			if want := oracleSeverity(o.Rule.SecuritySeverityLevel, security); alerts[i].SecuritySeverity != want {
				t.Fatalf("decodeAlerts(%q) row %d security severity %q, want %q", data, i, alerts[i].SecuritySeverity, want)
			}
			if want := oracleSeverity(o.Rule.Severity, problem); alerts[i].ProblemSeverity != want {
				t.Fatalf("decodeAlerts(%q) row %d problem severity %q, want %q", data, i, alerts[i].ProblemSeverity, want)
			}
		}
	})
}

// oracleSeverity is what a decoded severity should be: absent or null is
// empty, a known value in any case is itself, and anything else is unknown.
func oracleSeverity(raw *string, known map[string]bool) string {
	switch {
	case raw == nil || *raw == "":
		return ""
	case known[strings.ToLower(*raw)] && strings.ToLower(*raw) != "unknown":
		return strings.ToLower(*raw)
	}
	return "unknown"
}

// FuzzDecodeWorkflows drives the workflows page decode on the same terms:
// every row is kept as one of repo's definitions in its own state, never more
// rows than a page asks for, a page without a workflows array is never an
// empty page, and a page read whole names only GitHub's five states.
func FuzzDecodeWorkflows(f *testing.F) {
	f.Add(`{"total_count":1,"workflows":[{"name":"CI","path":".github/workflows/ci.yml","state":"disabled_inactivity"}]}`)
	f.Add(`{"workflows":[{"name":"CI","state":"active"},{"name":"Old","state":"disabled_manually"}]}`)
	f.Add(`{"workflows":[{"state":"deleted"},{"state":"disabled_fork"}]}`)
	f.Add(`{"workflows":[{"name":"nightly","state":"disabled_repository"}]}`)
	f.Add(`{"workflows":[{"name":"nameless"}]}`)
	f.Add(`{"workflows":null}`)
	f.Add(`{}`)
	f.Add(``)
	f.Add(`{"workflows":[{"state":7}]}`)
	f.Add(`[1,2]`)
	f.Add(`{"workflows":[` + strings.Repeat(`{"state":"disabled_manually"},`, perPage) + `{"state":"active"}]}`)
	known := map[string]bool{"active": true, "deleted": true, "disabled_fork": true, "disabled_inactivity": true, "disabled_manually": true}
	f.Fuzz(func(t *testing.T, data string) {
		defs, err := decodeWorkflows("o/r", []byte(data))
		if err != nil {
			return
		}
		if len(defs) > perPage {
			t.Fatalf("decodeWorkflows(%q) = %d rows, want at most %d", data, len(defs), perPage)
		}
		for _, w := range defs {
			if w.Repo != "o/r" || !known[w.State] {
				t.Fatalf("decodeWorkflows(%q) kept %+v, want o/r's definitions in a documented state", data, w)
			}
		}
		var oracle struct {
			Workflows []struct {
				State string `json:"state"`
			} `json:"workflows"`
		}
		if json.Unmarshal([]byte(data), &oracle) != nil || len(oracle.Workflows) != len(defs) {
			return
		}
		for i, w := range oracle.Workflows {
			if w.State != defs[i].State {
				t.Fatalf("decodeWorkflows(%q) row %d state %q, want %q", data, i, defs[i].State, w.State)
			}
		}
	})
}
