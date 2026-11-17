package forge

import "time"

// ReadState is how well a signal of one repository, or of one connection,
// was last read. Its value is the state label a metric carries.
type ReadState string

// The ReadState members.
const (
	// ReadComplete is a signal read whole.
	ReadComplete ReadState = "complete"
	// ReadPartial is a signal whose last read was cut short; what it did not
	// reach is kept from the last whole read.
	ReadPartial ReadState = "partial"
	// ReadBlind is a signal whose last read failed for a reason other than a
	// lasting limit of the token.
	ReadBlind ReadState = "blind"
	// ReadUnread is a signal not read yet.
	ReadUnread ReadState = "unread"
	// ReadUnsupported is a signal the forge or the token can never read, such
	// as code scanning outside GitHub.
	ReadUnsupported ReadState = "unsupported"
	// ReadExcluded is a repository an exclusion rule leaves out.
	ReadExcluded ReadState = "excluded"
)

// ReadStates is every ReadState, the fixed label set a metric declares.
func ReadStates() []ReadState {
	return []ReadState{ReadComplete, ReadPartial, ReadBlind, ReadUnread, ReadUnsupported, ReadExcluded}
}

// Signal is one family of reads whose state is tracked apart. Its value is
// the signal label a metric carries.
type Signal string

// The Signal members.
const (
	SignalRepositories Signal = "repositories"
	SignalPullRequests Signal = "pull_requests"
	SignalIssues       Signal = "issues"
	SignalChecks       Signal = "checks"
	SignalRuns         Signal = "runs"
	SignalWorkflows    Signal = "workflows"
	SignalCodeScanning Signal = "code_scanning"
	SignalLabels       Signal = "labels"
)

// Signals is every Signal, the fixed label set a metric declares.
func Signals() []Signal {
	return []Signal{
		SignalRepositories, SignalPullRequests, SignalIssues, SignalChecks,
		SignalRuns, SignalWorkflows, SignalCodeScanning, SignalLabels,
	}
}

// ReadMark is one signal's read state and the time of its last whole read,
// zero when it was never read whole.
type ReadMark struct {
	WholeAt time.Time
	State   ReadState
}

// severity orders the states Fold compares, the worst highest.
var severity = map[ReadState]int{ReadComplete: 1, ReadPartial: 2, ReadUnread: 3, ReadBlind: 4}

// Fold is one connection's mark for a signal from its repositories' marks,
// the one rule every connection-level state uses. Excluded marks are left
// out, then unsupported ones; of the rest it answers the worst state (blind,
// then unread, then partial, then complete) with the oldest whole-read time,
// zero when any of them was never read whole; a state it does not know
// counts as unread. When nothing is left it
// answers unsupported if any mark was unsupported, excluded if every mark was
// excluded, and unread for no marks at all.
func Fold(marks ...ReadMark) ReadMark {
	var out ReadMark
	excluded, unsupported, counted := 0, 0, 0
	for _, m := range marks {
		switch m.State {
		case ReadExcluded:
			excluded++
			continue
		case ReadUnsupported:
			unsupported++
			continue
		}
		state := m.State
		if severity[state] == 0 {
			state = ReadUnread
		}
		if counted == 0 || severity[state] > severity[out.State] {
			out.State = state
		}
		if counted == 0 || m.WholeAt.Before(out.WholeAt) {
			out.WholeAt = m.WholeAt
		}
		counted++
	}
	switch {
	case counted > 0:
		return out
	case unsupported > 0:
		return ReadMark{State: ReadUnsupported}
	case excluded > 0:
		return ReadMark{State: ReadExcluded}
	default:
		return ReadMark{State: ReadUnread}
	}
}
