package forge

import "time"

// Support is a capability or fact the forge may not have answered: unknown
// is never read as no.
type Support string

// The Support members.
const (
	SupportUnknown Support = "unknown"
	SupportYes     Support = "yes"
	SupportNo      Support = "no"
)

// AccountKind is what kind of account authored an item.
type AccountKind string

// The AccountKind members; AccountUnknown is a forge that does not say.
const (
	AccountUnknown AccountKind = "unknown"
	AccountUser    AccountKind = "user"
	AccountBot     AccountKind = "bot"
)

// CheckState is the folded verdict of one commit's checks, or one run's.
type CheckState string

// The CheckState members.
const (
	CheckPassing CheckState = "passing"
	CheckFailing CheckState = "failing"
	CheckPending CheckState = "pending"
	CheckNeutral CheckState = "neutral"
	// CheckNone is a whole read that found no check on the commit.
	CheckNone CheckState = "none"
	// CheckUnknown is a fold that could not give a verdict.
	CheckUnknown CheckState = "unknown"
)

// MergeBlock is why the forge would refuse a merge, or MergeBlockNone.
type MergeBlock string

// The MergeBlock members.
const (
	MergeBlockUnknown       MergeBlock = "unknown"
	MergeBlockNone          MergeBlock = "none"
	MergeBlockDraft         MergeBlock = "draft"
	MergeBlockConflicts     MergeBlock = "conflicts"
	MergeBlockChecksFailing MergeBlock = "checks_failing"
	MergeBlockChecksRunning MergeBlock = "checks_running"
	MergeBlockBehind        MergeBlock = "behind"
	MergeBlockBlocked       MergeBlock = "blocked"
)

// QueueState is a merge queue's verdict on a pull request.
type QueueState string

// The QueueState members; QueueNone is a repository with no queue.
const (
	QueueUnknown        QueueState = "unknown"
	QueueNone           QueueState = "none"
	QueueQueued         QueueState = "queued"
	QueueAwaitingChecks QueueState = "awaiting_checks"
	QueueMergeable      QueueState = "mergeable"
	QueueUnmergeable    QueueState = "unmergeable"
	QueueLocked         QueueState = "locked"
)

// ItemState is whether a pull request or an issue is open.
type ItemState string

// The ItemState members; only a pull request is ever merged.
const (
	ItemUnknown ItemState = "unknown"
	ItemOpen    ItemState = "open"
	ItemClosed  ItemState = "closed"
	ItemMerged  ItemState = "merged"
)

// Repo is one repository of a connection. Path is the forge's display path
// (owner/name, or group/sub/project on GitLab).
type Repo struct {
	Key           RepoKey
	Path          string
	DefaultBranch string
	WebURL        string
	Archived      bool
	Fork          bool
	Private       bool
}

// PullRequest is one pull request (merge request on GitLab): its record,
// whose fields an edit moves UpdatedAt for, and Head, the readings that
// belong to one head commit.
type PullRequest struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	Key       ItemKey
	State     ItemState
	Title     string
	Body      string
	Author    string
	// AuthorKind is what kind of account Author is, as the forge says.
	AuthorKind   AccountKind
	SourceBranch string
	// HeadSHA is empty where the answer names no head commit.
	HeadSHA string
	WebURL  string
	Labels  []string
	Head    Head
	// Comments is CountUnknown where the answer does not carry it.
	Comments int
	Draft    bool
}

// Head is what one answer read about the pull request's head commit
// ForHead. Read false is not read: every other field is then meaningless
// and nothing may be judged on it.
type Head struct {
	ForHead        string
	Checks         CheckState
	Mergeable      Support
	MergeBlocked   MergeBlock
	AutoMergeArmed Support
	Queue          QueueState
	// The check counts are the checks the fold read in each state.
	ChecksPassing int
	ChecksFailing int
	ChecksPending int
	ChecksNeutral int
	ChecksUnknown int
	ChecksTotal   int
	// QueuePosition is CountUnknown when none was read.
	QueuePosition int
	// Approvals and ChangesRequested count reviewers whose latest
	// opinionated review approves or requests changes; CountUnknown where
	// the answer does not carry them.
	Approvals        int
	ChangesRequested int
	Read             bool
	// ChecksRefused is checks the forge refused to this credential while it
	// answered the rest of the head: Checks is then CheckUnknown with no
	// count, and a retry with the same credential answers the same.
	ChecksRefused bool
}

// Issue is one issue.
type Issue struct {
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Key        ItemKey
	State      ItemState
	Title      string
	Body       string
	Author     string
	AuthorKind AccountKind
	WebURL     string
	Labels     []string
	// Comments is CountUnknown where the answer does not carry it.
	Comments int
}

// Trigger is what started a run.
type Trigger string

// The Trigger members.
const (
	TriggerUnknown     Trigger = "unknown"
	TriggerPush        Trigger = "push"
	TriggerPullRequest Trigger = "pull_request"
	TriggerSchedule    Trigger = "schedule"
	TriggerManual      Trigger = "manual"
	TriggerOther       Trigger = "other"
)

// Run is one CI run of a repository.
type Run struct {
	CreatedAt time.Time
	// StartedAt is zero where the product sends no start time.
	StartedAt time.Time
	UpdatedAt time.Time
	Repo      RepoKey
	// Workflow is the workflow's display name, which can differ between its
	// runs, as a GitHub run-name title does.
	Workflow string
	// WorkflowPath is the path of the workflow definition the run ran; empty
	// where the forge names none (GitLab).
	WorkflowPath string
	Branch       string
	HeadSHA      string
	WebURL       string
	Trigger      Trigger
	// State is pending until the run ends.
	State CheckState
	// ID is the run's identity within its repository, stable across re-runs
	// where the product keeps one run for its attempts.
	ID     int64
	Number int64
}

// Label is one label a repository defines.
type Label struct {
	Name string
	// Color is #rrggbb in lower case, empty where the forge named no hex
	// colour.
	Color string
}

// Affordances is what a repository lets the connection's account do.
type Affordances struct {
	DefaultBranch string
	CanPush       Support
	// MergeStrategies are the merge methods the repository allows, in the
	// forge's spelling, which a merge passes back unchanged.
	MergeStrategies []string
}

// MergeOutcomeState is what the forge did with a merge request.
type MergeOutcomeState string

// The MergeOutcomeState members.
const (
	MergeOutcomeUnknown  MergeOutcomeState = "unknown"
	MergeOutcomeMerged   MergeOutcomeState = "merged"
	MergeOutcomeEnqueued MergeOutcomeState = "enqueued"
	// MergeOutcomeAccepted is a merge the forge will make once the pull
	// request's requirements pass.
	MergeOutcomeAccepted MergeOutcomeState = "accepted"
	MergeOutcomeInFlight MergeOutcomeState = "in_flight"
	MergeOutcomeRefused  MergeOutcomeState = "refused"
)

// MergeOutcome is the forge's answer to a merge.
type MergeOutcome struct {
	State MergeOutcomeState
	// Code names why a refused merge was refused, empty otherwise.
	Code  string
	Queue QueueState
	// QueuePosition is CountUnknown when none was read.
	QueuePosition int
}

// MergeStatus is whether a pull request has merged, read after a merge.
type MergeStatus struct {
	Merged Support
	Queue  QueueState
	WebURL string
}

// Alert is one open code-scanning alert (GitHub only).
type Alert struct {
	CreatedAt time.Time
	Repo      RepoKey
	Rule      string
	// SecuritySeverity is low, medium, high or critical, unknown for a value
	// GitHub does not document, or empty where the rule sets none.
	SecuritySeverity string
	// ProblemSeverity is none, note, warning or error, unknown for a value
	// GitHub does not document, or empty where the rule sets none.
	ProblemSeverity string
	Tool            string
	URL             string
	// Path and StartLine place the alert's most recent instance in the
	// code; empty and 0 where GitHub sent no location.
	Path      string
	StartLine int
	Number    int64
}

// WorkflowState is a GitHub workflow definition's state.
type WorkflowState string

// The WorkflowState members, the five GitHub documents.
const (
	WorkflowActive             WorkflowState = "active"
	WorkflowDeleted            WorkflowState = "deleted"
	WorkflowDisabledFork       WorkflowState = "disabled_fork"
	WorkflowDisabledInactivity WorkflowState = "disabled_inactivity"
	WorkflowDisabledManually   WorkflowState = "disabled_manually"
)

// Workflow is one workflow definition (GitHub only).
type Workflow struct {
	Repo  RepoKey
	Name  string
	Path  string
	State WorkflowState
	URL   string
}

// Disabled reports a workflow GitHub disabled for inactivity or a person
// disabled by hand; one disabled because its repository is a fork is not.
func (w *Workflow) Disabled() bool {
	return w.State == WorkflowDisabledInactivity || w.State == WorkflowDisabledManually
}

// DisabledByInactivity reports a workflow GitHub turned off itself.
func (w *Workflow) DisabledByInactivity() bool { return w.State == WorkflowDisabledInactivity }

// Budget is one request pool's last reported figures.
type Budget struct {
	Reset time.Time
	// Remaining is CountUnknown where the forge reports none.
	Remaining int
	LastCost  int
	Cache     ReadCache
}

// Known reports whether the forge reported a remaining figure.
func (b Budget) Known() bool { return b.Remaining >= 0 }

// ReadCache is what a client's read cache did: Hits are reads the forge
// confirmed unchanged and a held answer served, Misses every other cacheable
// read the forge answered in full, and Saved the hits the forge billed
// nothing for.
type ReadCache struct {
	Hits, Misses, Saved int
}

// End is how a listing ended. The zero value is no End: a listing that set
// none is read as failed, never as whole.
type End string

// The End members.
const (
	// EndWhole is a listing read to its end.
	EndWhole End = "whole"
	// EndCut is a listing cut short: rows past the cut may be missing, so
	// nothing absent from it may be deleted.
	EndCut End = "cut"
	// EndNone is a signal the repository does not have, such as no
	// code-scanning analyses.
	EndNone End = "none"
)

// CutReason is why a listing was cut short.
type CutReason string

// The CutReason members.
const (
	// CutRateLimited is a page the forge's spent budget stopped.
	CutRateLimited CutReason = "rate_limited"
	// CutDeferred is a page held back unsent, at the read reserve or by the
	// forge library's own pacing.
	CutDeferred CutReason = "deferred"
	// CutResultWindow is a search that stops serving rows before its own
	// stated total.
	CutResultWindow CutReason = "result_window"
	// CutPaginationCap is a page cap the forge library reached with no
	// continuation left to follow.
	CutPaginationCap CutReason = "pagination_cap"
	// CutCeiling is forge-scout's own page ceiling for the read.
	CutCeiling CutReason = "ceiling"
	// CutGraphQLPartial is a page answered with data beside errors, or with
	// a row whose repository it left out.
	CutGraphQLPartial CutReason = "graphql_partial"
	// CutUnknown is a page the forge library marked partial for a reason it
	// documents on no page.
	CutUnknown CutReason = "unknown"
)

// Listing is a read's rows and how it ended. Cut is set only when End is
// EndCut. Indexed reports rows answered from a search index, which can lag
// the record.
type Listing[T any] struct {
	End     End
	Cut     CutReason
	Rows    []T
	Indexed bool
}
