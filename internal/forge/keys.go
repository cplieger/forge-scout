package forge

// RepoKey names one repository of one connection. Repo is the forge
// library's stable repository id, never the display path, so a rename or a
// transfer keeps the key.
type RepoKey struct {
	Connection string
	Repo       string
}

// ItemKey names one pull request or one issue of a repository. Pull requests
// and issues are kept apart by Kind, since GitLab numbers them separately.
type ItemKey struct {
	Connection string
	Repo       string
	Number     int
}

// RepoKey is the key of the repository k belongs to.
func (k ItemKey) RepoKey() RepoKey { return RepoKey{Connection: k.Connection, Repo: k.Repo} }

// Kind is what an item is. Its value is the kind label a metric carries.
type Kind string

// The Kind members.
const (
	KindPullRequest Kind = "pull_request"
	KindIssue       Kind = "issue"
)
