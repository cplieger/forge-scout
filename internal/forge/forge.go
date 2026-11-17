// Package forge holds the domain types every part of forge-scout shares: the
// keys of what it stores, read states and their fold, connection conditions,
// the rows a forge connection answers and the one error a forge failure
// crosses packages as. It declares no interfaces and does no IO; no forge
// library type appears in it, so a library's change stops at the adapter.
//
// Every enum is a string type whose value is its stored, wire and metric
// label spelling.
package forge

import "strings"

// Product is the forge product a connection answered as. Its value is the
// forge label a metric carries.
type Product string

// The Product members.
const (
	ProductGitHub  Product = "github"
	ProductGitLab  Product = "gitlab"
	ProductGitea   Product = "gitea"
	ProductForgejo Product = "forgejo"
)

// GitHubDotcomAPI is GitHub.com's REST root.
const GitHubDotcomAPI = "https://api.github.com"

// githubDotcomHosts are the hosts that address GitHub.com, whose API is
// GitHubDotcomAPI whatever the scheme or path, as forgeapi resolves them.
var githubDotcomHosts = map[string]bool{"github.com": true, "www.github.com": true, "api.github.com": true}

// GitHubDotcom reports whether host, in any case, addresses GitHub.com.
func GitHubDotcom(host string) bool { return githubDotcomHosts[strings.ToLower(host)] }

// ReadReserve is the part of each request pool kept for actions: a
// background read that would take a pool under it is deferred, while an
// action's writes and its own bounded reads pass it.
const ReadReserve = 250

// CountUnknown is a count the forge did not report.
const CountUnknown = -1

// Condition is a standing state of a connection that a person sees and an
// alert reads. Its value is the condition label a metric carries.
type Condition string

// The Condition members, in the order Conditions lists them.
const (
	ConditionNotSignedIn        Condition = "not_signed_in"
	ConditionCredentialDisabled Condition = "credential_disabled" //nolint:gosec // G101 matches the word credential; this is a condition code
	ConditionOwnerUnresolved    Condition = "owner_unresolved"
	ConditionReconnectRequired  Condition = "reconnect_required"
	ConditionReadOnly           Condition = "read_only"
	ConditionSealingKeyMissing  Condition = "sealing_key_missing"
	// ConditionHeld is one request pool held after a primary rate limit,
	// until its reset.
	ConditionHeld Condition = "held"
	// ConditionBehind is a connection that missed its read floor.
	ConditionBehind Condition = "behind"
	// ConditionUnreachable is a forge that failed three cycles in a row.
	ConditionUnreachable Condition = "unreachable"
	// ConditionSlowed is the whole connection held after a secondary limit.
	ConditionSlowed Condition = "slowed"
	// ConditionStretched is GitHub's GraphQL allowance spacing the lists over
	// more than one cycle.
	ConditionStretched Condition = "stretched"
	// ConditionPaced is background reads stopped at GitHub's response-time
	// cap.
	ConditionPaced Condition = "paced"
)

// Conditions is every Condition, the fixed label set a metric declares.
func Conditions() []Condition {
	return []Condition{
		ConditionNotSignedIn, ConditionCredentialDisabled, ConditionOwnerUnresolved,
		ConditionReconnectRequired, ConditionReadOnly, ConditionSealingKeyMissing,
		ConditionHeld, ConditionBehind, ConditionUnreachable, ConditionSlowed,
		ConditionStretched, ConditionPaced,
	}
}
