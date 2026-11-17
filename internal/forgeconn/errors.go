package forgeconn

import (
	"errors"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

// codeKinds are the codes whose remedy their kind does not name.
var codeKinds = map[string]forge.ErrorKind{
	forgeapi.CodeReconnectRequired:     forge.KindReconnectRequired,
	forgeapi.CodeCapabilityUnsupported: forge.KindUnsupported,
	forgeapi.CodeRunUndated:            forge.KindInvalid,
}

// mapErr turns a forgeapi failure into the *forge.Error it crosses packages
// as. A context error, which forgeapi returns unchanged, stays the context
// error, and so does nil.
func mapErr(err error) error {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok {
		return err
	}
	return &forge.Error{
		Kind: kindOf(fe), Code: fe.Code, Needs: fe.Needs, Message: fe.Message,
		Status: fe.Status, RetryAfter: fe.RetryAfter,
	}
}

// kindOf reads codeKinds first, then the kind. forgeapi answers its own deferral at the read reserve as a
// rate limit that was never sent, and a refusal made before any request with
// no kind at all.
func kindOf(fe *forgeapi.Error) forge.ErrorKind {
	if kind, ok := codeKinds[fe.Code]; ok {
		return kind
	}
	switch fe.Kind {
	case forgeapi.KindUnauthorized:
		return forge.KindUnauthorized
	case forgeapi.KindForbidden:
		return forge.KindForbidden
	case forgeapi.KindNotFound:
		return forge.KindNotFound
	case forgeapi.KindNotMergeable:
		return forge.KindNotMergeable
	case forgeapi.KindConflict:
		return forge.KindConflict
	case forgeapi.KindRateLimited:
		if fe.Status == 0 {
			return forge.KindDeferred
		}
		return forge.KindRateLimited
	case forgeapi.KindTransient:
		return forge.KindTransient
	case forgeapi.KindUpstream:
		return forge.KindUpstream
	case forgeapi.KindUnknown:
		if fe.Status == 0 && fe.Code != "" {
			return forge.KindInvalid
		}
	}
	return forge.KindUnknown
}
