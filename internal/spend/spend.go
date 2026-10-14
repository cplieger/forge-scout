// Package spend is the one account, on every forge, of what a connection's
// requests cost: each attempt it sends, and what the forge bills for it by
// the rule of the product the connection answered as.
package spend

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/forge-scout/internal/forge"
)

// Spend is what a connection's requests have cost.
type Spend struct {
	// Sent is every attempt handed to the wire: retries, redirect hops and
	// attempts that got no answer included.
	Sent int
	// REST is the REST requests the forge billed (see Meter).
	REST int
	// Documents is the GraphQL documents the forge answered.
	Documents int
	// Points is what those documents cost, in the forge's own GraphQL units.
	Points int
}

// Minus is what s holds beyond t.
func (s Spend) Minus(t Spend) Spend {
	return Spend{Sent: s.Sent - t.Sent, REST: s.REST - t.REST, Documents: s.Documents - t.Documents, Points: s.Points - t.Points}
}

// Plus is s and t together.
func (s Spend) Plus(t Spend) Spend {
	return Spend{Sent: s.Sent + t.Sent, REST: s.REST + t.REST, Documents: s.Documents + t.Documents, Points: s.Points + t.Points}
}

// Meter is one connection's spend for the life of the process; a reopened
// connection keeps its meter, so a failed open's requests count too. A REST
// attempt the forge answered is billed one, except a 304 Not Modified to a
// conditional request on GitHub, which bills nothing
// (https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate).
// An attempt with no answer is billed nothing, as no forge is known to have
// counted it. The zero value is ready; its methods are safe for concurrent use.
type Meter struct {
	spent   Spend
	product forge.Product
	// pending is the answered REST attempts since Detect, and pendingGitHub
	// the ones GitHub bills, until Name says which rule prices them.
	pending, pendingGitHub int
	mu                     sync.Mutex
}

// Detect starts an open: the attempts until Name are priced by the product it
// names. An earlier open that named none is priced at one each answer.
func (m *Meter) Detect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent.REST += m.pending
	m.pending, m.pendingGitHub = 0, 0
	m.product = forge.ProductUnknown
}

// Name records the product the connection answered as.
func (m *Meter) Name(p forge.Product) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p == forge.ProductGitHub {
		m.spent.REST += m.pendingGitHub
	} else {
		m.spent.REST += m.pending
	}
	m.pending, m.pendingGitHub = 0, 0
	m.product = p
}

// Observe wraps next so the meter counts each attempt it carries, for a
// transport every attempt, a retry and a redirect hop included, goes through.
// It passes the request and the answer on unchanged.
func (m *Meter) Observe(next http.RoundTripper) http.RoundTripper {
	return observer{next: next, m: m}
}

type observer struct {
	next http.RoundTripper
	m    *Meter
}

func (o observer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.next.RoundTrip(req)
	o.m.see(req, resp)
	return resp, err
}

func (m *Meter) see(req *http.Request, resp *http.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent.Sent++
	switch {
	case resp == nil:
	case strings.HasSuffix(req.URL.Path, "/graphql"):
		m.spent.Documents++
	case m.product == forge.ProductUnknown:
		m.pending++
		if githubBills(req, resp) && reportsGitHubBudget(resp) {
			m.pendingGitHub++
		}
	case m.product != forge.ProductGitHub || githubBills(req, resp):
		m.spent.REST++
	}
}

// githubBills reports whether GitHub bills a REST answer to req: all but a
// 304 to a request carrying a validator.
func githubBills(req *http.Request, resp *http.Response) bool {
	return resp.StatusCode != http.StatusNotModified || req.Header.Get("If-None-Match") == "" && req.Header.Get("If-Modified-Since") == ""
}

// reportsGitHubBudget reports an answer carrying GitHub's rate-limit count.
// Before the product is named, detection asks other families' routes first,
// and github.com's web host answers those 404 with no rate-limit header and
// no charge.
func reportsGitHubBudget(resp *http.Response) bool {
	n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	return err == nil && n >= 0
}

// ChargePoints charges points, in the forge's GraphQL units, for documents
// the connection sent.
func (m *Meter) ChargePoints(points int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent.Points += max(points, 0)
}

// Documents is the GraphQL documents answered so far.
func (m *Meter) Documents() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent.Documents
}

// Spent is what the connection has spent so far. Read during an open, its
// answers not priced yet count one each.
func (m *Meter) Spent() Spend {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.spent
	s.REST += m.pending
	return s
}
