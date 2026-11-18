// Package store is forge-scout's product file, forge-scout.db: its
// namespaces, the place of every table in it, the key codecs, and the open
// policy on one swappable bolttable handle.
package store

// NamespaceClass says what a format change in a namespace does.
type NamespaceClass uint8

// The NamespaceClass members.
const (
	// Durable holds what only the owner, or time, created; a format change
	// refuses the open, so the file is never reset.
	Durable NamespaceClass = iota + 1
	// Derived holds what the forges hold now; a format change resets it and
	// the next poll reads it back.
	Derived
)

// NamespaceSpec names one namespace and its class.
type NamespaceSpec struct {
	Name  string
	Class NamespaceClass
}

// TableSpec is one table's place in the file: its namespace, its name and
// its prefix byte, unique across the file. A Silent table's writes record
// no subject in the change feed.
type TableSpec struct {
	Name      string
	Namespace NamespaceSpec
	Prefix    byte
	Silent    bool
}

// The namespaces of the file.
var (
	NSOwner   = NamespaceSpec{Name: "owner", Class: Durable}
	NSForge   = NamespaceSpec{Name: "forge", Class: Derived}
	NSHistory = NamespaceSpec{Name: "history", Class: Durable}
)

// The tables of the file. Prefixes are grouped by namespace: owner 0x01 to
// 0x1F, forge 0x20 to 0x3F, history from 0x40. A prefix is never reused for
// another table, since a stored file may still hold its rows.
var (
	Meta         = TableSpec{Namespace: NSOwner, Name: "meta", Prefix: 0x01, Silent: true}
	Credentials  = TableSpec{Namespace: NSOwner, Name: "credentials", Prefix: 0x02, Silent: true}
	Mutes        = TableSpec{Namespace: NSOwner, Name: "mutes", Prefix: 0x03}
	Marks        = TableSpec{Namespace: NSOwner, Name: "marks", Prefix: 0x04}
	Actions      = TableSpec{Namespace: NSOwner, Name: "actions", Prefix: 0x05}
	WriteTimes   = TableSpec{Namespace: NSOwner, Name: "write_times", Prefix: 0x06, Silent: true}
	Removals     = TableSpec{Namespace: NSOwner, Name: "removals", Prefix: 0x07, Silent: true}
	Held         = TableSpec{Namespace: NSOwner, Name: "held", Prefix: 0x08, Silent: true}
	DisabledSeen = TableSpec{Namespace: NSOwner, Name: "disabled_seen", Prefix: 0x09, Silent: true}

	Repositories    = TableSpec{Namespace: NSForge, Name: "repositories", Prefix: 0x20}
	PullRequests    = TableSpec{Namespace: NSForge, Name: "pull_requests", Prefix: 0x21}
	Issues          = TableSpec{Namespace: NSForge, Name: "issues", Prefix: 0x22}
	Runs            = TableSpec{Namespace: NSForge, Name: "runs", Prefix: 0x23}
	Streaks         = TableSpec{Namespace: NSForge, Name: "streaks", Prefix: 0x24}
	Workflows       = TableSpec{Namespace: NSForge, Name: "workflows", Prefix: 0x25}
	Alerts          = TableSpec{Namespace: NSForge, Name: "alerts", Prefix: 0x26}
	Labels          = TableSpec{Namespace: NSForge, Name: "labels", Prefix: 0x27}
	RepoReads       = TableSpec{Namespace: NSForge, Name: "repo_reads", Prefix: 0x28, Silent: true}
	ConnectionReads = TableSpec{Namespace: NSForge, Name: "connection_reads", Prefix: 0x29}
	Budgets         = TableSpec{Namespace: NSForge, Name: "budgets", Prefix: 0x2A}
	Reasons         = TableSpec{Namespace: NSForge, Name: "reasons", Prefix: 0x2B}

	RunsPerDay = TableSpec{Namespace: NSHistory, Name: "runs_per_day", Prefix: 0x40}
	OpenCounts = TableSpec{Namespace: NSHistory, Name: "open_counts", Prefix: 0x41}
	Passes     = TableSpec{Namespace: NSHistory, Name: "passes", Prefix: 0x42}
)

// Namespaces lists every namespace of the file.
func Namespaces() []NamespaceSpec { return []NamespaceSpec{NSOwner, NSForge, NSHistory} }

// Tables lists every table of the file. NewSchema declares Meta; each
// table's owner declares its own through Define.
func Tables() []TableSpec {
	return []TableSpec{
		Meta, Credentials, Mutes, Marks, Actions, WriteTimes, Removals, Held, DisabledSeen,
		Repositories, PullRequests, Issues, Runs, Streaks, Workflows, Alerts, Labels,
		RepoReads, ConnectionReads, Budgets, Reasons,
		RunsPerDay, OpenCounts, Passes,
	}
}
