package forgeconn

import (
	"strings"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

func (c *Client) repoKey(ref *forgeapi.RepoRef) (forge.RepoKey, bool) {
	if ref.ID == "" {
		return forge.RepoKey{}, false
	}
	return forge.RepoKey{Connection: c.connection, Repo: ref.ID}, true
}

func (c *Client) repoOf(r *forgeapi.Repository) (forge.Repo, bool) {
	key, ok := c.repoKey(&r.Ref)
	if !ok {
		return forge.Repo{}, false
	}
	return forge.Repo{
		Key: key, Path: r.Ref.DisplayPath, DefaultBranch: r.Affordances.DefaultBranch, WebURL: r.WebURL,
		Archived: r.Archived, Fork: r.Fork, Private: r.Private,
	}, true
}

// prOf maps one pull request. headRead says whether the answer reads the
// head-keyed half at all, which a cross-repository list on some products does
// not (the product's ListMyPRs contract in forgeapi).
func (c *Client) prOf(r *forgeapi.PullRequest, headRead bool) (forge.PullRequest, bool) {
	key, ok := c.repoKey(&r.Repo)
	if !ok {
		return forge.PullRequest{}, false
	}
	return forge.PullRequest{
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Key:   forge.ItemKey{Connection: key.Connection, Repo: key.Repo, Number: r.Ref.Number},
		State: prStateOf(r.State), Title: r.Title, Body: r.Body, Author: r.Author, AuthorKind: accountKind(r.AuthorKind),
		SourceBranch: r.SourceBranch, HeadSHA: r.HeadSHA, WebURL: r.WebURL, Labels: labelNames(r.Labels),
		Head: headOf(r, headRead, c.product), Comments: r.Comments, Draft: r.Draft,
	}, true
}

// headOf is the head-keyed half of r. A row marker other than a refused
// rollup or a cut list inside the row (check names, reviewers: forgeapi keeps
// the verdict exact and answers a cut count CountUnknown) leaves the half not
// read, since the fields it covers may be missing. forgeapi answers both a
// head with no check and checks it did not read as CheckUnknown; only an
// answer that counts the head's checks tells them apart, and GitLab's one
// pipeline status counts none.
func headOf(r *forgeapi.PullRequest, headRead bool, product forge.Product) forge.Head {
	refused := false
	if p := r.Partial; p != nil {
		switch p.Reason {
		case forgeapi.PartialForbidden:
			refused = true
		case forgeapi.PartialPaginationCap:
		default:
			return forge.Head{}
		}
	}
	if !headRead {
		return forge.Head{}
	}
	a := &r.Action
	h := forge.Head{
		ForHead: r.HeadSHA, Checks: checkStateOf(a.Checks), Mergeable: supportOf(a.Mergeable),
		MergeBlocked: mergeBlockOf(a.MergeBlocked), AutoMergeArmed: supportOf(a.AutoMergeArmed),
		Queue:         queueOf(a.QueueState),
		ChecksPassing: a.ChecksPassing, ChecksFailing: a.ChecksFailing, ChecksPending: a.ChecksPending,
		ChecksNeutral: a.ChecksNeutral, ChecksUnknown: a.ChecksUnknown, ChecksTotal: a.ChecksTotal,
		QueuePosition: a.QueuePosition, Approvals: a.Approvals, ChangesRequested: a.ChangesRequested,
		Read: true, ChecksRefused: refused,
	}
	if !refused && product != forge.ProductGitLab && a.Checks == forgeapi.CheckUnknown && a.ChecksTotal == 0 {
		h.Checks = forge.CheckNone
	}
	return h
}

func (c *Client) issueOf(r *forgeapi.Issue) (forge.Issue, bool) {
	key, ok := c.repoKey(&r.Repo)
	if !ok {
		return forge.Issue{}, false
	}
	return forge.Issue{
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Key:   forge.ItemKey{Connection: key.Connection, Repo: key.Repo, Number: r.Ref.Number},
		State: issueStateOf(r.State), Title: r.Title, Body: r.Body, Author: r.Author, AuthorKind: accountKind(r.AuthorKind),
		WebURL: r.WebURL, Labels: labelNames(r.Labels), Comments: r.Comments,
	}, true
}

func (c *Client) runOf(r *forgeapi.Run) (forge.Run, bool) {
	key, ok := c.repoKey(&r.Repo)
	if !ok {
		return forge.Run{}, false
	}
	return forge.Run{
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt, Repo: key,
		Workflow: r.Name, WorkflowPath: r.WorkflowPath, Branch: r.Branch, HeadSHA: r.HeadSHA, WebURL: r.WebURL,
		Trigger: triggerOf(r.Trigger), State: checkStateOf(r.State), ID: r.Ref.ID, Number: r.Ref.Number,
	}, true
}

func labelOf(l *forgeapi.Label) (forge.Label, bool) {
	return forge.Label{Name: l.Name, Color: colorOf(l.Color)}, true
}

// colorOf is a forge's label colour in forge.Label's spelling. GitHub, Gitea
// and Forgejo send the six digits alone; GitLab sends a leading # and three
// or six digits, or a CSS colour name
// (https://docs.gitlab.com/api/labels/#create-a-new-label).
func colorOf(raw string) string {
	hex := strings.ToLower(strings.TrimPrefix(raw, "#"))
	if strings.Trim(hex, "0123456789abcdef") != "" {
		return ""
	}
	switch len(hex) {
	case 3:
		return "#" + string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	case 6:
		return "#" + hex
	}
	return ""
}

func affordancesOf(a *forgeapi.RepoAffordances) forge.Affordances {
	return forge.Affordances{DefaultBranch: a.DefaultBranch, CanPush: supportOf(a.CanPush), MergeStrategies: a.MergeStrategies}
}

func budgetOf(b *forgeapi.BudgetState) forge.Budget {
	out := forge.Budget{
		Reset: b.Reset, Remaining: b.Remaining, LastCost: b.LastCost,
		Cache: forge.ReadCache{Hits: b.ReadCache.Hits, Misses: b.ReadCache.Misses, Saved: b.ReadCache.Saved},
	}
	if b.Remaining == forgeapi.BudgetRemainingUnknown {
		out.Remaining = forge.CountUnknown
	}
	return out
}

func labelNames(ls []forgeapi.Label) []string {
	out := make([]string, 0, len(ls))
	for i := range ls {
		out = append(out, ls[i].Name)
	}
	return out
}

func productOf(p forgeapi.Product) (forge.Product, bool) {
	switch p {
	case forgeapi.ProductGitHub:
		return forge.ProductGitHub, true
	case forgeapi.ProductGitLab:
		return forge.ProductGitLab, true
	case forgeapi.ProductGitea:
		return forge.ProductGitea, true
	case forgeapi.ProductForgejo:
		return forge.ProductForgejo, true
	}
	return "", false
}

func supportOf(s forgeapi.Support) forge.Support {
	switch s {
	case forgeapi.SupportYes:
		return forge.SupportYes
	case forgeapi.SupportNo:
		return forge.SupportNo
	}
	return forge.SupportUnknown
}

func accountKind(k forgeapi.AccountKind) forge.AccountKind {
	switch k {
	case forgeapi.AccountKindUser:
		return forge.AccountUser
	case forgeapi.AccountKindBot:
		return forge.AccountBot
	}
	return forge.AccountUnknown
}

func checkStateOf(s forgeapi.CheckState) forge.CheckState {
	switch s {
	case forgeapi.CheckPassing:
		return forge.CheckPassing
	case forgeapi.CheckFailing:
		return forge.CheckFailing
	case forgeapi.CheckPending:
		return forge.CheckPending
	case forgeapi.CheckNeutral:
		return forge.CheckNeutral
	}
	return forge.CheckUnknown
}

func mergeBlockOf(m forgeapi.MergeBlockReason) forge.MergeBlock {
	switch m {
	case forgeapi.MergeBlockNone:
		return forge.MergeBlockNone
	case forgeapi.MergeBlockDraft:
		return forge.MergeBlockDraft
	case forgeapi.MergeBlockConflicts:
		return forge.MergeBlockConflicts
	case forgeapi.MergeBlockChecksFailing:
		return forge.MergeBlockChecksFailing
	case forgeapi.MergeBlockChecksRunning:
		return forge.MergeBlockChecksRunning
	case forgeapi.MergeBlockBehind:
		return forge.MergeBlockBehind
	case forgeapi.MergeBlockBlocked:
		return forge.MergeBlockBlocked
	}
	return forge.MergeBlockUnknown
}

func queueOf(q forgeapi.QueueState) forge.QueueState {
	switch q {
	case forgeapi.QueueNone:
		return forge.QueueNone
	case forgeapi.QueueQueued:
		return forge.QueueQueued
	case forgeapi.QueueAwaitingChecks:
		return forge.QueueAwaitingChecks
	case forgeapi.QueueMergeable:
		return forge.QueueMergeable
	case forgeapi.QueueUnmergeable:
		return forge.QueueUnmergeable
	case forgeapi.QueueLocked:
		return forge.QueueLocked
	}
	return forge.QueueUnknown
}

func prStateOf(s forgeapi.PRState) forge.ItemState {
	switch s {
	case forgeapi.PRStateOpen:
		return forge.ItemOpen
	case forgeapi.PRStateClosed:
		return forge.ItemClosed
	case forgeapi.PRStateMerged:
		return forge.ItemMerged
	}
	return forge.ItemUnknown
}

func issueStateOf(s forgeapi.IssueState) forge.ItemState {
	switch s {
	case forgeapi.IssueStateOpen:
		return forge.ItemOpen
	case forgeapi.IssueStateClosed:
		return forge.ItemClosed
	}
	return forge.ItemUnknown
}

func triggerOf(t forgeapi.RunTrigger) forge.Trigger {
	switch t {
	case forgeapi.TriggerPush:
		return forge.TriggerPush
	case forgeapi.TriggerPullRequest:
		return forge.TriggerPullRequest
	case forgeapi.TriggerSchedule:
		return forge.TriggerSchedule
	case forgeapi.TriggerManual:
		return forge.TriggerManual
	case forgeapi.TriggerOther:
		return forge.TriggerOther
	}
	return forge.TriggerUnknown
}
