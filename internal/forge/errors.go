package forge

import (
	"strconv"
	"strings"
	"time"
)

// ErrorKind is what a forge failure means for the caller.
type ErrorKind string

// The ErrorKind members.
const (
	// KindUnauthorized is a credential the forge rejected.
	KindUnauthorized ErrorKind = "unauthorized"
	// KindReconnectRequired is a credential that must be granted again.
	KindReconnectRequired ErrorKind = "reconnect_required"
	// KindForbidden is a live credential refused this request; Needs names
	// the missing permission where the forge said.
	KindForbidden ErrorKind = "forbidden"
	KindNotFound  ErrorKind = "not_found"
	// KindNotMergeable is a merge the forge refused for the pull request's
	// state.
	KindNotMergeable ErrorKind = "not_mergeable"
	KindConflict     ErrorKind = "conflict"
	// KindRateLimited is the forge refusing further requests for now.
	KindRateLimited ErrorKind = "rate_limited"
	// KindDeferred is a read held back, unsent, to keep the read reserve.
	KindDeferred ErrorKind = "deferred"
	// KindTransient is a failure worth retrying later: the network, or an
	// answer that can change.
	KindTransient ErrorKind = "transient"
	// KindUpstream is an answer forge-scout cannot use.
	KindUpstream ErrorKind = "upstream"
	// KindUnsupported is an operation the forge does not offer.
	KindUnsupported ErrorKind = "unsupported"
	// KindInvalid is a request refused before sending.
	KindInvalid ErrorKind = "invalid"
	KindUnknown ErrorKind = "unknown"
)

// Error is the one value a forge failure crosses packages as. Status is the
// HTTP status of the answer, 0 for one never sent or never answered.
// RetryAfter is the wait the forge asked for, 0 when it named none. Code is
// the forge library's machine-readable code where it gave one.
type Error struct {
	Kind       ErrorKind
	Code       string
	Needs      string
	Message    string
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("forge ")
	b.WriteString(string(e.Kind))
	if e.Code != "" {
		b.WriteString(" (" + e.Code + ")")
	}
	if e.Status != 0 {
		b.WriteString(" status " + strconv.Itoa(e.Status))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}
