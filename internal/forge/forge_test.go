package forge

import (
	"testing"
	"time"
)

func TestError_names_kind_code_status_and_message(t *testing.T) {
	tests := []struct {
		name string
		err  Error
		want string
	}{
		{name: "kind_only", err: Error{Kind: KindDeferred}, want: "forge deferred"},
		{
			name: "everything",
			err:  Error{Kind: KindForbidden, Code: "no_merge_permission", Status: 403, Message: "token lacks contents: write", RetryAfter: time.Minute},
			want: "forge forbidden (no_merge_permission) status 403: token lacks contents: write",
		},
		{name: "status_and_message", err: Error{Kind: KindRateLimited, Status: 429, Message: "slow down"}, want: "forge rate_limited status 429: slow down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestItemKey_RepoKey_drops_the_number(t *testing.T) {
	k := ItemKey{Connection: "c1", Repo: "v1.R_1", Number: 7}
	if got := k.RepoKey(); got != (RepoKey{Connection: "c1", Repo: "v1.R_1"}) {
		t.Errorf("RepoKey() = %+v, want connection c1 and repository v1.R_1", got)
	}
}

func TestGitHubDotcom(t *testing.T) {
	for host, want := range map[string]bool{
		"github.com": true, "GitHub.com": true, "www.github.com": true, "api.github.com": true,
		"ghe.example.test": false, "github.com.example.test": false, "": false,
	} {
		if got := GitHubDotcom(host); got != want {
			t.Errorf("GitHubDotcom(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestWorkflow_disabled(t *testing.T) {
	for _, tc := range []struct {
		state              WorkflowState
		disabled, inactive bool
	}{
		{WorkflowActive, false, false},
		{WorkflowDeleted, false, false},
		{WorkflowDisabledFork, false, false},
		{WorkflowDisabledInactivity, true, true},
		{WorkflowDisabledManually, true, false},
	} {
		w := Workflow{State: tc.state}
		if w.Disabled() != tc.disabled || w.DisabledByInactivity() != tc.inactive {
			t.Errorf("Workflow in %s: Disabled %v, DisabledByInactivity %v, want %v, %v", tc.state, w.Disabled(), w.DisabledByInactivity(), tc.disabled, tc.inactive)
		}
	}
}
