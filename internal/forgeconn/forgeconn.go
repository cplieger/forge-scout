// Package forgeconn is forge-scout's one path to forgeapi: it opens a
// connection and makes every forge call forge-scout needs, translating
// forgeapi's rows, partial markers and errors into internal/forge. No other
// package imports forgeapi for a forge call or holds its row types.
package forgeconn

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/families"
)

// Config is one connection to open. Connection is forge-scout's id for it,
// which every key the client answers carries. Web is the instance's address
// and API its API root, empty to derive it from Web. The two observers wrap
// every attempt (each retry included) and every request; nil observes
// nothing.
type Config struct {
	Credential       forgeapi.CredentialSource
	Logger           *slog.Logger
	AttemptObserver  func(http.RoundTripper) http.RoundTripper
	RequestObserver  func(http.RoundTripper) http.RoundTripper
	Connection       string
	Web              string
	API              string
	PrivateAddresses bool
	PlaintextHTTP    bool
}

// api is the forgeapi client methods the adapter calls.
type api interface {
	ConnectionCaps(ctx context.Context) (forgeapi.ConnectionCaps, error)
	Whoami(ctx context.Context) (forgeapi.Account, error)
	BudgetState() forgeapi.BudgetState
	ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error)
	ListMyPRs(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error)
	ListMyIssues(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error)
	ListPRs(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error)
	ListRuns(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error)
	ListLabels(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Label], error)
	RepoAffordances(ctx context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error)
	ReadPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, opts ...forgeapi.ReadOption) (forgeapi.PullRequest, error)
	ReadIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error)
	MergeStatus(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.MergeStatus, error)
	MergePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error)
	CancelAutoMerge(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) error
	ClosePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error)
	ReopenPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error)
	CloseIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error)
	ReopenIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error)
	AddLabels(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error
	RemoveLabels(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error
	RerunFailedChecks(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, headSHA string) error
	RerunFailedRun(ctx context.Context, repo forgeapi.RepoRef, run forgeapi.RunRef) error
	Close()
}

// Client is one open connection. Its methods are safe for concurrent use.
type Client struct {
	api        api
	connection string
	login      string
	product    forge.Product
	rerun      forge.Support
	family     forgeapi.Family
}

// Open detects the connection's forge, reads its capabilities and identifies
// its account. Writes are on, and a background read stops at
// forge.ReadReserve of any pool while an action's writes pass it. A failure
// is a *forge.Error, or the context's error.
func Open(ctx context.Context, cfg *Config) (*Client, error) {
	opts := []forgeapi.Option{
		forgeapi.WithCredentialSource(cfg.Credential),
		forgeapi.WithMutations(true),
		forgeapi.WithMutationReserve(forge.ReadReserve),
		forgeapi.WithReadConcurrency(2),
		forgeapi.WithListPages(1),
		forgeapi.WithPrivateAddresses(cfg.PrivateAddresses),
		forgeapi.WithPlaintextHTTP(cfg.PlaintextHTTP),
	}
	if cfg.Logger != nil {
		opts = append(opts, forgeapi.WithLogger(cfg.Logger))
	}
	if cfg.AttemptObserver != nil {
		opts = append(opts, forgeapi.WithAttemptObserver(cfg.AttemptObserver))
	}
	if cfg.RequestObserver != nil {
		opts = append(opts, forgeapi.WithRequestObserver(cfg.RequestObserver))
	}
	core, family, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: cfg.Web, APIBaseURL: cfg.API}, opts...)
	if err != nil {
		return nil, mapErr(err)
	}
	c, err := newClient(ctx, core, family, cfg.Connection)
	if err != nil {
		core.Close()
		return nil, err
	}
	return c, nil
}

// newClient reads the capabilities and the account of an opened connection.
func newClient(ctx context.Context, a api, family forgeapi.Family, connection string) (*Client, error) {
	caps, err := a.ConnectionCaps(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	product, ok := productOf(caps.Product)
	if !ok {
		return nil, &forge.Error{Kind: forge.KindUpstream, Message: "the instance named no forge product"}
	}
	account, err := a.Whoami(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return &Client{
		api: a, connection: connection, login: account.Login, product: product,
		rerun: supportOf(caps.Caps[forgeapi.CapRerunChecks]), family: family,
	}, nil
}

// Product is the forge product the connection answered as.
func (c *Client) Product() forge.Product { return c.product }

// Login is the account the credential belongs to.
func (c *Client) Login() string { return c.login }

// CanRerun is whether the instance offers re-running failed checks, as
// detection read it at open.
func (c *Client) CanRerun() forge.Support { return c.rerun }

// Budget is the request pool the last response reported, with what the read
// cache did since the connection opened.
func (c *Client) Budget() forge.Budget {
	state := c.api.BudgetState()
	return budgetOf(&state)
}

// Close releases the connection's idle network connections.
func (c *Client) Close() { c.api.Close() }

// ref rebuilds the forgeapi reference of a repository id this connection
// answered.
func (c *Client) ref(repo string) (forgeapi.RepoRef, error) {
	ref, err := forgeapi.DecodeRepoRef(repo, c.family)
	if err != nil {
		return forgeapi.RepoRef{}, mapErr(err)
	}
	return ref, nil
}
