package forgeconn

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

// stub answers the forgeapi calls a case names; any other call panics on
// the nil embedded interface.
type stub struct {
	api
	caps    forgeapi.ConnectionCaps
	budget  forgeapi.BudgetState
	pr      forgeapi.PullRequest
	issue   forgeapi.Issue
	targets []forgeapi.LabelTarget
	merges  []forgeapi.MergeRequest
	outcome forgeapi.MergeOutcome
}

func (s *stub) AddLabels(_ context.Context, _ forgeapi.RepoRef, target forgeapi.LabelTarget, _ []string) error {
	s.targets = append(s.targets, target)
	return nil
}

func (s *stub) RemoveLabels(_ context.Context, _ forgeapi.RepoRef, target forgeapi.LabelTarget, _ []string) error {
	s.targets = append(s.targets, target)
	return nil
}

func (s *stub) MergePR(_ context.Context, _ forgeapi.RepoRef, _ forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	s.merges = append(s.merges, req)
	return s.outcome, nil
}

func (s *stub) ConnectionCaps(context.Context) (forgeapi.ConnectionCaps, error) { return s.caps, nil }

func (s *stub) Whoami(context.Context) (forgeapi.Account, error) {
	return forgeapi.Account{Login: "octo"}, nil
}
func (s *stub) BudgetState() forgeapi.BudgetState { return s.budget }
func (s *stub) ReadPR(context.Context, forgeapi.RepoRef, forgeapi.PRRef, ...forgeapi.ReadOption) (forgeapi.PullRequest, error) {
	return s.pr, nil
}

func (s *stub) ReadIssue(context.Context, forgeapi.RepoRef, forgeapi.IssueRef) (forgeapi.Issue, error) {
	return s.issue, nil
}

var appRef = func() forgeapi.RepoRef {
	r := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "acme/app", DisplayPath: "acme/app"}
	r.ID = r.Encode()
	return r
}()

func client(s *stub) *Client {
	return &Client{api: s, connection: "c1", product: forge.ProductGitHub, family: forgeapi.FamilyGitHub}
}

func TestMapErr_maps_every_kind_and_the_codes_that_name_their_own_remedy(t *testing.T) {
	tests := []struct {
		name string
		in   forgeapi.Error
		want forge.ErrorKind
	}{
		{"unauthorized", forgeapi.Error{Kind: forgeapi.KindUnauthorized, Status: 401}, forge.KindUnauthorized},
		{"forbidden", forgeapi.Error{Kind: forgeapi.KindForbidden, Status: 403}, forge.KindForbidden},
		{"scope_insufficient", forgeapi.Error{Kind: forgeapi.KindForbidden, Code: forgeapi.CodeScopeInsufficient, Status: 403, Needs: "contents=write"}, forge.KindForbidden},
		{"not_found", forgeapi.Error{Kind: forgeapi.KindNotFound, Status: 404}, forge.KindNotFound},
		{"owner_unresolved", forgeapi.Error{Kind: forgeapi.KindNotFound, Code: forgeapi.CodeOwnerUnresolved, Status: 200}, forge.KindNotFound},
		{"not_mergeable", forgeapi.Error{Kind: forgeapi.KindNotMergeable, Code: forgeapi.CodeNotMergeable, Status: 405}, forge.KindNotMergeable},
		{"conflict", forgeapi.Error{Kind: forgeapi.KindConflict, Code: forgeapi.CodeStaleHead, Status: 409}, forge.KindConflict},
		{"rate_limited", forgeapi.Error{Kind: forgeapi.KindRateLimited, Status: 429, RetryAfter: time.Minute}, forge.KindRateLimited},
		{"deferred", forgeapi.Error{Kind: forgeapi.KindRateLimited}, forge.KindDeferred},
		{"transient", forgeapi.Error{Kind: forgeapi.KindTransient, Code: forgeapi.CodeUnreachable}, forge.KindTransient},
		{"upstream", forgeapi.Error{Kind: forgeapi.KindUpstream, Status: 500}, forge.KindUpstream},
		{"reconnect_required", forgeapi.Error{Kind: forgeapi.KindUnauthorized, Code: forgeapi.CodeReconnectRequired, Status: 400}, forge.KindReconnectRequired},
		{"capability_unsupported", forgeapi.Error{Kind: forgeapi.KindNotFound, Code: forgeapi.CodeCapabilityUnsupported}, forge.KindUnsupported},
		{"run_undated", forgeapi.Error{Kind: forgeapi.KindUpstream, Code: forgeapi.CodeRunUndated, Status: 200}, forge.KindInvalid},
		{"local_refusal", forgeapi.Error{Code: forgeapi.CodeRepoRefInvalid}, forge.KindInvalid},
		{"unknown_answered", forgeapi.Error{Status: 418}, forge.KindUnknown},
		{"unknown_bare", forgeapi.Error{}, forge.KindUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Message = "upstream words"
			got, ok := errors.AsType[*forge.Error](mapErr(fmt.Errorf("wrapped: %w", &in)))
			if !ok {
				t.Fatalf("mapErr gave no *forge.Error")
			}
			want := forge.Error{Kind: tc.want, Code: in.Code, Needs: in.Needs, Message: "upstream words", Status: in.Status, RetryAfter: in.RetryAfter}
			if *got != want {
				t.Errorf("mapErr = %+v, want %+v", *got, want)
			}
		})
	}
}

func TestMapErr_passes_nil_and_the_context_error_through(t *testing.T) {
	if err := mapErr(nil); err != nil {
		t.Errorf("mapErr(nil) = %v", err)
	}
	if err := mapErr(context.Canceled); err != context.Canceled { //nolint:errorlint // the very value must come back
		t.Errorf("mapErr(context.Canceled) = %v, want it unchanged", err)
	}
}

func TestCutOf_names_each_page_reason(t *testing.T) {
	tests := []struct {
		name      string
		reason    forgeapi.PartialReason
		remaining int
		want      forge.CutReason
	}{
		{"deferred_with_budget_left", forgeapi.PartialRateLimited, 200, forge.CutDeferred},
		{"rate_limited_with_none", forgeapi.PartialRateLimited, 0, forge.CutRateLimited},
		{"rate_limited_unknown_budget", forgeapi.PartialRateLimited, forgeapi.BudgetRemainingUnknown, forge.CutRateLimited},
		{"own_pacing", forgeapi.PartialBudget, 200, forge.CutDeferred},
		{"graphql_partial", forgeapi.PartialGraphQLPartial, 200, forge.CutGraphQLPartial},
		{"pagination_cap", forgeapi.PartialPaginationCap, 200, forge.CutPaginationCap},
		{"result_window", forgeapi.PartialResultWindow, 200, forge.CutResultWindow},
		{"forbidden", forgeapi.PartialForbidden, 200, forge.CutUnknown},
		{"unknown", forgeapi.PartialUnknown, 200, forge.CutUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := client(&stub{budget: forgeapi.BudgetState{Remaining: tc.remaining}})
			if got := c.cutOf(tc.reason); got != tc.want {
				t.Errorf("cutOf(%v) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestListing_a_row_with_no_repository_is_dropped_and_cuts_a_whole_listing(t *testing.T) {
	c := client(&stub{})
	rows := []forgeapi.Issue{{Repo: appRef, Ref: forgeapi.IssueRef{Number: 1}}, {Ref: forgeapi.IssueRef{Number: 2}}}
	for _, tc := range []struct {
		name    string
		end     forge.End
		cut     forge.CutReason
		wantCut forge.CutReason
	}{
		{"whole", forge.EndWhole, "", forge.CutGraphQLPartial},
		{"already_cut", forge.EndCut, forge.CutCeiling, forge.CutCeiling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := walked[forgeapi.Issue]{rows: rows, end: tc.end, cut: tc.cut}
			l := listing(&w, c.issueOf)
			if len(l.Rows) != 1 || l.Rows[0].Key.Number != 1 {
				t.Errorf("rows = %+v, want #1 alone", l.Rows)
			}
			if l.End != forge.EndCut || l.Cut != tc.wantCut {
				t.Errorf("listing ended %q cut %q, want cut %q", l.End, l.Cut, tc.wantCut)
			}
		})
	}
}

func TestHeadOf_reads_the_half_unless_a_marker_or_the_list_says_otherwise(t *testing.T) {
	passing := forgeapi.ActionState{Checks: forgeapi.CheckPassing, ChecksPassing: 1, ChecksTotal: 1}
	tests := []struct {
		name     string
		partial  *forgeapi.Partial
		action   forgeapi.ActionState
		headRead bool
		read     bool
		refused  bool
		checks   forge.CheckState
	}{
		{"whole", nil, passing, true, true, false, forge.CheckPassing},
		{"cut_names", &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap}, passing, true, true, false, forge.CheckPassing},
		{"refused_rollup", &forgeapi.Partial{Reason: forgeapi.PartialForbidden}, forgeapi.ActionState{}, true, true, true, forge.CheckUnknown},
		{"no_check", nil, forgeapi.ActionState{}, true, true, false, forge.CheckNone},
		{"graphql_partial", &forgeapi.Partial{Reason: forgeapi.PartialGraphQLPartial}, passing, true, false, false, ""},
		{"fold_deferred", &forgeapi.Partial{Reason: forgeapi.PartialRateLimited}, passing, true, false, false, ""},
		{"fold_unfinished", &forgeapi.Partial{Reason: forgeapi.PartialBudget}, passing, true, false, false, ""},
		{"list_without_head", nil, passing, false, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := headOf(&forgeapi.PullRequest{HeadSHA: "abc", Partial: tc.partial, Action: tc.action}, tc.headRead)
			if h.Read != tc.read || h.ChecksRefused != tc.refused || h.Checks != tc.checks {
				t.Errorf("head = %+v, want read %v, refused %v, checks %q", h, tc.read, tc.refused, tc.checks)
			}
			if tc.read && h.ForHead != "abc" {
				t.Errorf("ForHead = %q, want the answer's head abc", h.ForHead)
			}
		})
	}
}

func TestNewClient_an_instance_naming_no_product_is_upstream(t *testing.T) {
	_, err := newClient(t.Context(), &stub{}, forgeapi.FamilyGitHub, "c1")
	if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindUpstream {
		t.Errorf("newClient = %v, want upstream", err)
	}
}

func TestNewClient_reads_the_rerun_capability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps map[forgeapi.Capability]forgeapi.Support
		want forge.Support
	}{
		{"yes", map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: forgeapi.SupportYes}, forge.SupportYes},
		{"no", map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: forgeapi.SupportNo}, forge.SupportNo},
		{"unread", nil, forge.SupportUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := newClient(t.Context(), &stub{caps: forgeapi.ConnectionCaps{Product: forgeapi.ProductForgejo, Caps: tc.caps}}, forgeapi.FamilyGitea, "c1")
			if err != nil {
				t.Fatalf("newClient: %v", err)
			}
			if c.CanRerun() != tc.want || c.Product() != forge.ProductForgejo || c.Login() != "octo" {
				t.Errorf("CanRerun %q product %q login %q, want %q on forgejo for octo", c.CanRerun(), c.Product(), c.Login(), tc.want)
			}
		})
	}
}

func TestReads_a_single_read_naming_no_repository_is_upstream(t *testing.T) {
	c := client(&stub{})
	key := forge.ItemKey{Connection: "c1", Repo: appRef.ID, Number: 1}
	_, prErr := c.ReadPR(t.Context(), key)
	_, issueErr := c.ReadIssue(t.Context(), key)
	for name, err := range map[string]error{"ReadPR": prErr, "ReadIssue": issueErr} {
		if fe, ok := errors.AsType[*forge.Error](err); !ok || fe.Kind != forge.KindUpstream {
			t.Errorf("%s = %v, want upstream", name, err)
		}
	}
}

func TestBudget_an_unreported_remaining_is_count_unknown(t *testing.T) {
	reset := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	c := client(&stub{budget: forgeapi.BudgetState{
		Remaining: forgeapi.BudgetRemainingUnknown, Reset: reset, LastCost: 2,
		ReadCache: forgeapi.ReadCacheState{Hits: 3, Misses: 4, Saved: 5, Bytes: 6},
	}})
	want := forge.Budget{Remaining: forge.CountUnknown, Reset: reset, LastCost: 2, Cache: forge.ReadCache{Hits: 3, Misses: 4, Saved: 5}}
	if got := c.Budget(); got != want {
		t.Errorf("Budget = %+v, want %+v", got, want)
	}
}

func TestLabels_address_the_item_of_the_kind_named(t *testing.T) {
	s := &stub{}
	c := client(s)
	key := forge.ItemKey{Connection: "c1", Repo: appRef.ID, Number: 7}
	if err := c.AddLabels(t.Context(), key, forge.KindPullRequest, []string{"a"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if err := c.RemoveLabels(t.Context(), key, forge.KindIssue, []string{"a"}); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	want := []forgeapi.LabelTarget{{PR: forgeapi.PRRef{Number: 7}}, {Issue: forgeapi.IssueRef{Number: 7}}}
	if len(s.targets) != 2 || s.targets[0] != want[0] || s.targets[1] != want[1] {
		t.Errorf("targets = %+v, want %+v", s.targets, want)
	}
}

func TestMerge_sends_the_strategy_or_the_repository_default_and_never_deletes_the_branch(t *testing.T) {
	s := &stub{}
	c := client(s)
	key := forge.ItemKey{Connection: "c1", Repo: appRef.ID, Number: 7}
	for _, req := range []MergeRequest{{HeadSHA: "abc"}, {Strategy: "squash", HeadSHA: "abc", AutoMerge: true}} {
		if _, err := c.Merge(t.Context(), key, req); err != nil {
			t.Fatalf("Merge: %v", err)
		}
	}
	want := []forgeapi.MergeRequest{
		{Intent: forgeapi.IntentDefault, HeadSHA: "abc"},
		{Intent: forgeapi.IntentDefault, Strategy: "squash", HeadSHA: "abc", AutoMerge: true},
	}
	if len(s.merges) != 2 || s.merges[0] != want[0] || s.merges[1] != want[1] {
		t.Errorf("merge requests = %+v, want %+v", s.merges, want)
	}
}

func TestMerge_maps_each_outcome(t *testing.T) {
	tests := []struct {
		in   forgeapi.MergeOutcomeState
		want forge.MergeOutcomeState
	}{
		{forgeapi.MergeOutcomeMerged, forge.MergeOutcomeMerged},
		{forgeapi.MergeOutcomeEnqueued, forge.MergeOutcomeEnqueued},
		{forgeapi.MergeOutcomeAccepted, forge.MergeOutcomeAccepted},
		{forgeapi.MergeOutcomeInFlight, forge.MergeOutcomeInFlight},
		{forgeapi.MergeOutcomeRefused, forge.MergeOutcomeRefused},
		{forgeapi.MergeOutcomeUnknown, forge.MergeOutcomeUnknown},
	}
	for _, tc := range tests {
		t.Run(string(tc.want), func(t *testing.T) {
			s := &stub{outcome: forgeapi.MergeOutcome{State: tc.in, Code: "code", QueueState: forgeapi.QueueQueued, QueuePosition: 3}}
			out, err := client(s).Merge(t.Context(), forge.ItemKey{Connection: "c1", Repo: appRef.ID, Number: 7}, MergeRequest{HeadSHA: "abc"})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			want := forge.MergeOutcome{State: tc.want, Code: "code", Queue: forge.QueueQueued, QueuePosition: 3}
			if out != want {
				t.Errorf("Merge = %+v, want %+v", out, want)
			}
		})
	}
}
