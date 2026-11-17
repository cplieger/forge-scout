package forgeconn

import (
	"context"
	"strconv"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

// Repos lists owner's repositories. An owner the instance does not resolve
// is a not_found error with the code owner_unresolved.
func (c *Client) Repos(ctx context.Context, owner string) (forge.Listing[forge.Repo], error) {
	w, err := walk(ctx, c, maxRepoPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Repository], error) {
		return c.api.ListRepos(ctx, forgeapi.WithOwner(owner), forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.Repo]{}, err
	}
	return listing(&w, c.repoOf), nil
}

// MyPRs lists the open pull requests under owner across its repositories,
// with merge state and reviews. On GitLab, Gitea and Forgejo the rows carry
// no head-keyed half: Head is not read.
func (c *Client) MyPRs(ctx context.Context, owner string) (forge.Listing[forge.PullRequest], error) {
	w, err := walk(ctx, c, maxSnapshotPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
		return c.api.ListMyPRs(ctx, forgeapi.WithOwner(owner), forgeapi.WithMergeState(true), forgeapi.WithReviews(true), forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.PullRequest]{}, err
	}
	headRead := c.product == forge.ProductGitHub
	return listing(&w, func(r *forgeapi.PullRequest) (forge.PullRequest, bool) { return c.prOf(r, headRead) }), nil
}

// MyIssues lists the open issues under owner across its repositories.
func (c *Client) MyIssues(ctx context.Context, owner string) (forge.Listing[forge.Issue], error) {
	w, err := walk(ctx, c, maxSnapshotPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
		return c.api.ListMyIssues(ctx, forgeapi.WithOwner(owner), forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.Issue]{}, err
	}
	return listing(&w, c.issueOf), nil
}

// RepoPRs lists one repository's open pull requests, each with its head's
// checks, merge state and reviews.
func (c *Client) RepoPRs(ctx context.Context, repo forge.RepoKey) (forge.Listing[forge.PullRequest], error) {
	ref, err := c.ref(repo.Repo)
	if err != nil {
		return forge.Listing[forge.PullRequest]{}, err
	}
	w, err := walk(ctx, c, maxRepoItemPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
		return c.api.ListPRs(ctx, ref, forgeapi.WithState(forgeapi.ListStateOpen), forgeapi.WithMergeState(true), forgeapi.WithReviews(true), forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.PullRequest]{}, err
	}
	return listing(&w, func(r *forgeapi.PullRequest) (forge.PullRequest, bool) { return c.prOf(r, true) }), nil
}

// RunsSince lists repo's runs created at or after since. A product that
// sends a run with no creation time fails it as invalid with the code
// run_undated.
func (c *Client) RunsSince(ctx context.Context, repo forge.RepoKey, since time.Time) (forge.Listing[forge.Run], error) {
	ref, err := c.ref(repo.Repo)
	if err != nil {
		return forge.Listing[forge.Run]{}, err
	}
	w, err := walk(ctx, c, maxRunPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error) {
		return c.api.ListRuns(ctx, ref, forgeapi.WithSince(since), forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.Run]{}, err
	}
	return listing(&w, c.runOf), nil
}

// Labels lists the labels repo defines.
func (c *Client) Labels(ctx context.Context, repo forge.RepoKey) (forge.Listing[forge.Label], error) {
	ref, err := c.ref(repo.Repo)
	if err != nil {
		return forge.Listing[forge.Label]{}, err
	}
	w, err := walk(ctx, c, maxLabelPages, func(ctx context.Context, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Label], error) {
		return c.api.ListLabels(ctx, ref, forgeapi.WithAfter(after))
	})
	if err != nil {
		return forge.Listing[forge.Label]{}, err
	}
	return listing(&w, labelOf), nil
}

// Affordances reads what repo lets the connection's account do.
func (c *Client) Affordances(ctx context.Context, repo forge.RepoKey) (forge.Affordances, error) {
	ref, err := c.ref(repo.Repo)
	if err != nil {
		return forge.Affordances{}, err
	}
	a, err := c.api.RepoAffordances(ctx, ref)
	if err != nil {
		return forge.Affordances{}, mapErr(err)
	}
	return affordancesOf(&a), nil
}

// ReadPR reads one pull request with its head's whole check fold, merge
// state and reviews.
func (c *Client) ReadPR(ctx context.Context, key forge.ItemKey) (forge.PullRequest, error) {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return forge.PullRequest{}, err
	}
	r, err := c.api.ReadPR(ctx, ref, forgeapi.PRRef{Number: key.Number}, forgeapi.WithReviews(true))
	if err != nil {
		return forge.PullRequest{}, mapErr(err)
	}
	pr, ok := c.prOf(&r, true)
	if !ok {
		return forge.PullRequest{}, unkeyed(key)
	}
	return pr, nil
}

// ReadIssue reads one issue. An issue transferred to another repository is
// answered keyed by the repository it is in now.
func (c *Client) ReadIssue(ctx context.Context, key forge.ItemKey) (forge.Issue, error) {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return forge.Issue{}, err
	}
	r, err := c.api.ReadIssue(ctx, ref, forgeapi.IssueRef{Number: key.Number})
	if err != nil {
		return forge.Issue{}, mapErr(err)
	}
	issue, ok := c.issueOf(&r)
	if !ok {
		return forge.Issue{}, unkeyed(key)
	}
	return issue, nil
}

// MergeStatus reads whether a pull request has merged, after a merge.
func (c *Client) MergeStatus(ctx context.Context, key forge.ItemKey) (forge.MergeStatus, error) {
	ref, err := c.ref(key.Repo)
	if err != nil {
		return forge.MergeStatus{}, err
	}
	s, err := c.api.MergeStatus(ctx, ref, forgeapi.PRRef{Number: key.Number})
	if err != nil {
		return forge.MergeStatus{}, mapErr(err)
	}
	return forge.MergeStatus{Merged: supportOf(s.Merged), Queue: queueOf(s.Queue), WebURL: s.WebURL}, nil
}

// unkeyed is a single read whose answer names no repository.
func unkeyed(key forge.ItemKey) *forge.Error {
	return &forge.Error{Kind: forge.KindUpstream, Message: "the forge answered #" + strconv.Itoa(key.Number) + " with no repository"}
}
