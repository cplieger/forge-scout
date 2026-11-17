package forgeconn

import (
	"context"
	"net/http"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

// MergeRequest is one merge, pinned to the head commit the caller judged.
type MergeRequest struct {
	// Strategy is one of the repository's Affordances.MergeStrategies; empty
	// merges with the repository's own default.
	Strategy string
	// HeadSHA is required: the forge refuses the merge when the head moved.
	HeadSHA string
	// AutoMerge has the forge merge once the pull request's requirements
	// pass, where it cannot merge now and the forge offers it.
	AutoMerge bool
}

// The write methods answer only whether the forge accepted the write: its
// own answer is not the new state, which a re-read decides. Each is one
// action's write, sent past the read reserve; a failure is a *forge.Error, or
// the context's error.

// Merge merges a pull request, or arms its merge when AutoMerge is set and
// the pull request cannot merge now.
func (c *Client) Merge(ctx context.Context, key forge.ItemKey, req MergeRequest) (forge.MergeOutcome, error) {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return forge.MergeOutcome{}, err
	}
	out, err := c.api.MergePR(ctx, ref, forgeapi.PRRef{Number: key.Number}, forgeapi.MergeRequest{
		Intent: forgeapi.IntentDefault, Strategy: req.Strategy, HeadSHA: req.HeadSHA, AutoMerge: req.AutoMerge,
	})
	if err != nil {
		return forge.MergeOutcome{}, mapErr(err)
	}
	return forge.MergeOutcome{
		State: mergeOutcomeOf(out.State), Code: out.Code, Queue: queueOf(out.QueueState), QueuePosition: out.QueuePosition,
	}, nil
}

// CancelAutoMerge disarms a pull request's armed merge. A pull request with
// nothing armed is a success.
func (c *Client) CancelAutoMerge(ctx context.Context, key forge.ItemKey) error {
	return c.onPR(ctx, key, c.api.CancelAutoMerge)
}

// ClosePR closes a pull request.
func (c *Client) ClosePR(ctx context.Context, key forge.ItemKey) error {
	return c.onPR(ctx, key, func(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) error {
		_, err := c.api.ClosePR(ctx, repo, pr)
		return err
	})
}

// ReopenPR reopens a closed pull request.
func (c *Client) ReopenPR(ctx context.Context, key forge.ItemKey) error {
	return c.onPR(ctx, key, func(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) error {
		_, err := c.api.ReopenPR(ctx, repo, pr)
		return err
	})
}

// CloseIssue closes an issue.
func (c *Client) CloseIssue(ctx context.Context, key forge.ItemKey) error {
	return c.onIssue(ctx, key, func(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) error {
		_, err := c.api.CloseIssue(ctx, repo, issue)
		return err
	})
}

// ReopenIssue reopens a closed issue.
func (c *Client) ReopenIssue(ctx context.Context, key forge.ItemKey) error {
	return c.onIssue(ctx, key, func(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) error {
		_, err := c.api.ReopenIssue(ctx, repo, issue)
		return err
	})
}

// AddLabels puts the named labels, each one the repository defines, on the
// pull request or issue key of kind.
func (c *Client) AddLabels(ctx context.Context, key forge.ItemKey, kind forge.Kind, names []string) error {
	return c.onLabels(ctx, key, kind, func(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget) error {
		return c.api.AddLabels(ctx, repo, target, names)
	})
}

// RemoveLabels takes the named labels off the pull request or issue key of
// kind. A name the item does not carry is a success.
func (c *Client) RemoveLabels(ctx context.Context, key forge.ItemKey, kind forge.Kind, names []string) error {
	return c.onLabels(ctx, key, kind, func(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget) error {
		return c.api.RemoveLabels(ctx, repo, target, names)
	})
}

// RerunFailedChecks re-runs the failed checks of a pull request's head
// commit head, empty for the head it carries now. A pinned head no run
// carries is a conflict with the code stale_head; any other head with no
// failed or cancelled run, one with the code run_not_failed.
func (c *Client) RerunFailedChecks(ctx context.Context, key forge.ItemKey, head string) error {
	return c.onPR(ctx, key, func(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) error {
		return c.api.RerunFailedChecks(ctx, repo, pr, head)
	})
}

// RerunFailedRun re-runs the failed jobs of run (on Forgejo the whole run).
func (c *Client) RerunFailedRun(ctx context.Context, run *forge.Run) error {
	ref, err := c.ref(run.Repo.Repo)
	if err != nil {
		return err
	}
	return mapErr(c.api.RerunFailedRun(ctx, ref, forgeapi.RunRef{ID: run.ID, Number: run.Number}))
}

func (c *Client) onPR(ctx context.Context, key forge.ItemKey, write func(context.Context, forgeapi.RepoRef, forgeapi.PRRef) error) error {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return err
	}
	return mapErr(write(ctx, ref, forgeapi.PRRef{Number: key.Number}))
}

func (c *Client) onIssue(ctx context.Context, key forge.ItemKey, write func(context.Context, forgeapi.RepoRef, forgeapi.IssueRef) error) error {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return err
	}
	return mapErr(write(ctx, ref, forgeapi.IssueRef{Number: key.Number}))
}

func (c *Client) onLabels(ctx context.Context, key forge.ItemKey, kind forge.Kind, write func(context.Context, forgeapi.RepoRef, forgeapi.LabelTarget) error) error {
	var target forgeapi.LabelTarget
	switch kind {
	case forge.KindPullRequest:
		target.PR = forgeapi.PRRef{Number: key.Number}
	case forge.KindIssue:
		target.Issue = forgeapi.IssueRef{Number: key.Number}
	default:
		return &forge.Error{Kind: forge.KindInvalid, Message: "a label change addresses a pull request or an issue, not " + string(kind)}
	}
	ref, err := c.ref(key.Repo)
	if err != nil {
		return err
	}
	return mapErr(write(ctx, ref, target))
}

// ActionReads marks ctx for the reads one action makes around its write: up
// to n of them, clamped to 1..20, pass the read reserve that background reads
// stop at.
func ActionReads(ctx context.Context, n int) context.Context { return forgeapi.ActionReads(ctx, n) }

// Mutates reports whether r, as a Config observer sees it, is a request that
// changes forge state: a write, never a read a write makes around it.
func Mutates(r *http.Request) bool { return forgeapi.Mutates(r) }

func mergeOutcomeOf(s forgeapi.MergeOutcomeState) forge.MergeOutcomeState {
	switch s {
	case forgeapi.MergeOutcomeMerged:
		return forge.MergeOutcomeMerged
	case forgeapi.MergeOutcomeEnqueued:
		return forge.MergeOutcomeEnqueued
	case forgeapi.MergeOutcomeAccepted:
		return forge.MergeOutcomeAccepted
	case forgeapi.MergeOutcomeInFlight:
		return forge.MergeOutcomeInFlight
	case forgeapi.MergeOutcomeRefused:
		return forge.MergeOutcomeRefused
	}
	return forge.MergeOutcomeUnknown
}
