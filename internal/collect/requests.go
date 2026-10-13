package collect

import (
	"log/slog"

	"github.com/cplieger/github-scout/internal/forge"
	"github.com/cplieger/github-scout/internal/spend"
)

// requestFamilies names the requests_<family> keys of each read phase, in
// the order a scan line carries them.
var requestFamilies = []struct{ phase, key string }{
	{phaseOpen, "open"},
	{phaseDiscovery, "discovery"},
	{phasePRs, "prs"},
	{phaseIssues, "issues"},
	{phaseRuns, "runs"},
	{phaseChecks, "checks"},
	{phaseSecurity, "code_scanning"},
	{phaseWorkflows, "workflows"},
}

// requestBook splits what a connection's meter counts over the scan's read
// phases: each phase is charged what the meter counted while it was under
// way, which holds because a connection's reads run one at a time.
type requestBook struct {
	meter *spend.Meter
	by    map[string]spend.Spend
	at    spend.Spend
}

func newRequestBook(m *spend.Meter) requestBook {
	return requestBook{meter: m, by: map[string]spend.Spend{}, at: m.Spent()}
}

// charge charges phase what the meter counted since the last charge.
func (b *requestBook) charge(phase string) {
	now := b.meter.Spent()
	b.by[phase] = b.by[phase].Plus(now.Minus(b.at))
	b.at = now
}

// enter starts phase, charging the one under way what it sent.
func (r *connResult) enter(phase string) {
	r.book.charge(r.phase)
	r.phase = phase
}

// readCache is what the read caches of the connection's clients did since
// cs.cache, their counts when the last scan read them or zero since an open.
func readCache(cs *connState) forge.ReadCache {
	now := cs.conn.ReadCache()
	if cs.gh != nil {
		now = now.Plus(cs.gh.ReadCache())
	}
	d := now.Minus(cs.cache)
	cs.cache = now
	return d
}

// requestAttrs is what the scan's requests cost (see spend.Meter) and what
// its read cache did: every attempt sent, the REST requests the forge
// billed, the GraphQL documents it answered and their points, which can read
// low (see forgeconn.Client.price), then the REST requests and documents of
// each read phase.
func requestAttrs(r *connResult) []slog.Attr {
	var total spend.Spend
	for _, s := range r.book.by {
		total = total.Plus(s)
	}
	attrs := make([]slog.Attr, 0, 7+2*len(requestFamilies))
	attrs = append(attrs,
		slog.Int("requests_sent", total.Sent), slog.Int("requests_rest", total.REST),
		slog.Int("requests_graphql", total.Documents), slog.Int("points_graphql", total.Points),
		slog.Int("cache_hits", r.cache.Hits), slog.Int("cache_misses", r.cache.Misses), slog.Int("cache_saved", r.cache.Saved))
	for _, f := range requestFamilies {
		s := r.book.by[f.phase]
		attrs = append(attrs, slog.Int("requests_"+f.key, s.REST), slog.Int("requests_"+f.key+"_graphql", s.Documents))
	}
	return attrs
}
