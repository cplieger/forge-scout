// Package ghquota is the one account of a GitHub connection's REST request
// quota that both of its clients, forgeapi's and the REST client for the
// routes forgeapi does not model, draw on. It observes every response either
// sends and keeps each rate-limit pool GitHub names.
package ghquota

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
)

// corePool is the pool a response naming none drew on, and the one the REST
// routes of both clients share.
const corePool = "core"

// Meter is one GitHub connection's quota. It observes nothing until Arm, so
// a connection that turns out not to be GitHub leaves it inert. Its methods
// are safe for concurrent use.
type Meter struct {
	pools map[string]forge.Budget
	now   func() time.Time
	mu    sync.Mutex
	armed bool
}

// New returns an unarmed meter reading the time from now, time.Now when nil.
func New(now func() time.Time) *Meter {
	if now == nil {
		now = time.Now
	}
	return &Meter{pools: map[string]forge.Budget{}, now: now}
}

// Arm starts the meter on a connection that answered as GitHub.
func (m *Meter) Arm() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.armed = true
}

// Admit reports whether a background read may be sent: a *forge.Error of
// kind deferred while the shared REST pool is at or below the read reserve,
// whose RetryAfter is the wait until the pool's reported reset.
func (m *Meter) Admit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.pool(corePool)
	if !b.Known() || b.Remaining > forge.ReadReserve {
		return nil
	}
	e := &forge.Error{Kind: forge.KindDeferred, Message: "the GitHub REST budget is at the read reserve"}
	if !b.Reset.IsZero() {
		e.RetryAfter = b.Reset.Sub(m.now())
	}
	return e
}

// Budget is the shared REST pool's budget as the last response reported it,
// unknown once its window renews.
func (m *Meter) Budget() forge.Budget {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pool(corePool)
}

// pool is the named pool's budget; m.mu must be held.
func (m *Meter) pool(name string) forge.Budget {
	b, ok := m.pools[name]
	if !ok || (!b.Reset.IsZero() && !m.now().Before(b.Reset)) {
		return forge.Budget{Remaining: forge.CountUnknown}
	}
	return b
}

// Observe wraps next so the meter accounts each attempt it carries, a retry
// included. It passes every request and answer on unchanged.
func (m *Meter) Observe(next http.RoundTripper) http.RoundTripper {
	return observer{next: next, meter: m}
}

type observer struct {
	next  http.RoundTripper
	meter *Meter
}

func (o observer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.next.RoundTrip(req)
	o.meter.see(resp)
	return resp, err
}

// see accounts one sent attempt: the pool GitHub's rate-limit headers name
// takes the remaining count and reset they report, else the known REST pool
// loses one.
func (m *Meter) see(resp *http.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.armed {
		return
	}
	if resp == nil {
		m.spendOne()
		return
	}
	n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil || n < 0 {
		m.spendOne()
		return
	}
	name := resp.Header.Get("X-RateLimit-Resource")
	if name == "" {
		name = corePool
	}
	b := forge.Budget{Remaining: n}
	if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		b.Reset = time.Unix(s, 0).UTC()
	}
	m.pools[name] = b
}

// spendOne charges a response that reported no pool to the known REST pool.
func (m *Meter) spendOne() {
	if b := m.pool(corePool); b.Known() {
		b.Remaining = max(b.Remaining-1, 0)
		m.pools[corePool] = b
	}
}

// Throttled reports a 403 GitHub sends for a rate limit: X-RateLimit-Remaining
// at 0, or a Retry-After header
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
func Throttled(h http.Header) bool {
	n, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	return (err == nil && n <= 0) || h.Get("Retry-After") != ""
}
