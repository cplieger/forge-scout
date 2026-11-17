package forgeconn

import (
	"context"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forgeapi/v3"
)

// Walk ceilings, in pages. Every list call reads one page (WithListPages(1)),
// so a ceiling bounds the requests one read spends; reaching one with a
// continuation left cuts the listing.
const (
	maxRepoPages     = 10
	maxSnapshotPages = 40
	maxRepoItemPages = 10
	maxRunPages      = 40
	maxLabelPages    = 10
)

// walked is the rows a walk read and how it ended.
type walked[T any] struct {
	end     forge.End
	cut     forge.CutReason
	rows    []T
	indexed bool
}

// walk follows Next across list calls up to maxPages. forgeapi's page cap with
// a continuation is its own per-call bound and the walk goes on; any other
// partial marker ends the walk cut, keeping the rows read. A failed page fails
// the walk.
func walk[T any](ctx context.Context, c *Client, maxPages int, fetch func(context.Context, forgeapi.Cursor) (forgeapi.Page[T], error)) (walked[T], error) {
	var w walked[T]
	var after forgeapi.Cursor
	for range maxPages {
		page, err := fetch(ctx, after)
		if err != nil {
			return walked[T]{}, mapErr(err)
		}
		w.rows = append(w.rows, page.Items...)
		w.indexed = w.indexed || page.Indexed
		if p := page.Partial; p != nil && (p.Reason != forgeapi.PartialPaginationCap || page.Next == "") {
			w.end, w.cut = forge.EndCut, c.cutOf(p.Reason)
			return w, nil
		}
		if page.Next == "" {
			w.end = forge.EndWhole
			return w, nil
		}
		after = page.Next
	}
	w.end, w.cut = forge.EndCut, forge.CutCeiling
	return w, nil
}

// cutOf is the cut reason of a page forgeapi marked partial. forgeapi gives a
// deferral at the reserve and a spent budget one reason and documents telling
// them apart by the budget left: a deferral leaves some.
func (c *Client) cutOf(reason forgeapi.PartialReason) forge.CutReason {
	switch reason {
	case forgeapi.PartialRateLimited:
		if c.api.BudgetState().Remaining > 0 {
			return forge.CutDeferred
		}
		return forge.CutRateLimited
	case forgeapi.PartialBudget:
		return forge.CutDeferred
	case forgeapi.PartialGraphQLPartial:
		return forge.CutGraphQLPartial
	case forgeapi.PartialPaginationCap:
		return forge.CutPaginationCap
	case forgeapi.PartialResultWindow:
		return forge.CutResultWindow
	}
	return forge.CutUnknown
}

// listing is a walk's rows mapped by row, which drops a row it cannot key; a
// dropped row cuts a listing the walk read whole, since a row is missing.
func listing[T, R any](w *walked[T], row func(*T) (R, bool)) forge.Listing[R] {
	out := forge.Listing[R]{End: w.end, Cut: w.cut, Indexed: w.indexed, Rows: make([]R, 0, len(w.rows))}
	for i := range w.rows {
		r, ok := row(&w.rows[i])
		if !ok {
			if out.End == forge.EndWhole {
				out.End, out.Cut = forge.EndCut, forge.CutGraphQLPartial
			}
			continue
		}
		out.Rows = append(out.Rows, r)
	}
	return out
}
