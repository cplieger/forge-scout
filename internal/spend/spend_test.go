package spend

import (
	"errors"
	"maps"
	"net/http"
	"testing"

	"github.com/cplieger/forge-scout/internal/forge"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// send sends one attempt to url through m, answered with status and the
// headers in name, value pairs, or with no answer when status is 0.
func send(t *testing.T, m *Meter, method, url string, status int, header ...string) {
	t.Helper()
	sendAsking(t, m, nil, method, url, status, header...)
}

// sendAsking is send with asked's request headers.
func sendAsking(t *testing.T, m *Meter, asked http.Header, method, url string, status int, header ...string) {
	t.Helper()
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	var answer *http.Response
	next := roundTrip(func(r *http.Request) (*http.Response, error) {
		if status == 0 {
			return nil, errors.New("connection reset")
		}
		answer = &http.Response{StatusCode: status, Header: h, Body: http.NoBody, Request: r}
		return answer, nil
	})
	req, err := http.NewRequestWithContext(t.Context(), method, url, http.NoBody)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	maps.Copy(req.Header, asked)
	resp, err := m.Observe(next).RoundTrip(req)
	if resp != answer || (err != nil) != (status == 0) {
		t.Fatalf("Observe answered %v, %v for status %d, want the attempt's own answer passed on", resp, err, status)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

const (
	rest    = "https://forge.test/api/v3/repos/o/r/actions/runs"
	graphql = "https://forge.test/api/graphql"
)

func TestSpent_on_github_bills_every_answered_rest_attempt_but_a_conditional_304(t *testing.T) {
	var m Meter
	m.Detect()
	m.Name(forge.ProductGitHub)
	send(t, &m, http.MethodGet, rest, http.StatusOK)
	send(t, &m, http.MethodGet, rest, http.StatusBadGateway)
	sendAsking(t, &m, http.Header{"If-None-Match": {`"a"`}}, http.MethodGet, rest, http.StatusNotModified)
	sendAsking(t, &m, http.Header{"If-Modified-Since": {"Mon, 12 Oct 2026 10:00:00 GMT"}}, http.MethodGet, rest, http.StatusNotModified)
	send(t, &m, http.MethodGet, rest, 0)
	if got, want := m.Spent(), (Spend{Sent: 5, REST: 2}); got != want {
		t.Errorf("Spent = %+v, want %+v: five attempts sent, the 200 and the 502 billed, the two conditional 304s free and the unanswered one unknown to GitHub", got, want)
	}
}

func TestSpent_on_github_bills_a_304_to_an_unconditional_read(t *testing.T) {
	var m Meter
	m.Detect()
	m.Name(forge.ProductGitHub)
	send(t, &m, http.MethodGet, rest, http.StatusNotModified)
	if got, want := m.Spent(), (Spend{Sent: 1, REST: 1}); got != want {
		t.Errorf("Spent = %+v, want %+v: GitHub exempts only a 304 to a conditional request", got, want)
	}
}

func TestSpent_on_every_other_forge_bills_a_304(t *testing.T) {
	for _, p := range []forge.Product{forge.ProductGitLab, forge.ProductGitea, forge.ProductForgejo} {
		t.Run(p.String(), func(t *testing.T) {
			var m Meter
			m.Detect()
			m.Name(p)
			send(t, &m, http.MethodGet, rest, http.StatusOK)
			send(t, &m, http.MethodGet, rest, http.StatusNotModified)
			send(t, &m, http.MethodGet, rest, 0)
			if got, want := m.Spent(), (Spend{Sent: 3, REST: 2}); got != want {
				t.Errorf("Spent = %+v, want %+v: the 200 and the 304 each billed one", got, want)
			}
		})
	}
}

func TestSpent_counts_graphql_documents_apart_from_rest_requests(t *testing.T) {
	var m Meter
	m.Detect()
	m.Name(forge.ProductGitHub)
	send(t, &m, http.MethodPost, graphql, http.StatusOK)
	send(t, &m, http.MethodPost, "https://api.github.test/graphql", http.StatusOK)
	send(t, &m, http.MethodPost, graphql, 0)
	m.ChargePoints(7)
	if got, want := m.Spent(), (Spend{Sent: 3, Documents: 2, Points: 7}); got != want {
		t.Errorf("Spent = %+v, want %+v: two answered documents, none of them a REST request, and the points charged", got, want)
	}
	if got := m.Documents(); got != 2 {
		t.Errorf("Documents = %d, want the 2 answered", got)
	}
}

func TestSpent_prices_a_github_open_by_the_answers_that_report_its_budget(t *testing.T) {
	var m Meter
	m.Detect()
	send(t, &m, http.MethodGet, "https://github.test/api/v4/metadata", http.StatusNotFound)
	send(t, &m, http.MethodGet, "https://api.github.test/meta", http.StatusOK, "X-RateLimit-Remaining", "4999")
	send(t, &m, http.MethodGet, "https://api.github.test/user", http.StatusOK, "X-RateLimit-Remaining", "4998")
	sendAsking(t, &m, http.Header{"If-None-Match": {`"a"`}}, http.MethodGet, "https://api.github.test/user", http.StatusNotModified, "X-RateLimit-Remaining", "4998")
	m.Name(forge.ProductGitHub)
	if got, want := m.Spent(), (Spend{Sent: 4, REST: 2}); got != want {
		t.Errorf("Spent = %+v, want %+v: the web host's 404 to another family's probe reports no GitHub budget, and the conditional 304 is free, so neither is billed", got, want)
	}
}

func TestSpent_prices_another_forges_open_at_one_each_answer(t *testing.T) {
	var m Meter
	m.Detect()
	send(t, &m, http.MethodGet, "https://forge.test/api/v4/metadata", http.StatusNotFound)
	send(t, &m, http.MethodGet, "https://forge.test/api/v1/version", http.StatusOK)
	send(t, &m, http.MethodGet, "https://forge.test/api/v1/user", 0)
	m.Name(forge.ProductForgejo)
	if got, want := m.Spent(), (Spend{Sent: 3, REST: 2}); got != want {
		t.Errorf("Spent = %+v, want %+v", got, want)
	}
}

func TestSpent_an_open_that_named_no_product_bills_each_answer_and_never_falls(t *testing.T) {
	var m Meter
	m.Detect()
	send(t, &m, http.MethodGet, "https://github.test/api/v4/metadata", http.StatusNotFound)
	failed := m.Spent()
	if want := (Spend{Sent: 1, REST: 1}); failed != want {
		t.Errorf("Spent of an open that named no product = %+v, want %+v", failed, want)
	}
	m.Detect()
	m.Name(forge.ProductGitHub)
	if got := m.Spent(); got.REST < failed.REST || got.Sent < failed.Sent {
		t.Errorf("Spent after the next open named GitHub = %+v, below the %+v read before it", got, failed)
	}
}

func TestSpent_a_reopen_prices_its_own_detection(t *testing.T) {
	var m Meter
	m.Detect()
	m.Name(forge.ProductGitHub)
	send(t, &m, http.MethodGet, rest, http.StatusOK)
	m.Detect()
	send(t, &m, http.MethodGet, "https://github.test/api/v4/metadata", http.StatusNotFound)
	m.Name(forge.ProductGitHub)
	if got, want := m.Spent(), (Spend{Sent: 2, REST: 1}); got != want {
		t.Errorf("Spent after a reopen = %+v, want %+v: the reopen's probe GitHub does not bill stays unbilled", got, want)
	}
}

func TestChargePoints_charges_no_negative_points(t *testing.T) {
	var m Meter
	m.ChargePoints(-2)
	if got := m.Spent().Points; got != 0 {
		t.Errorf("Points = %d, want 0", got)
	}
}

func TestSpend_Minus_is_what_one_spend_holds_beyond_another(t *testing.T) {
	a := Spend{Sent: 9, REST: 5, Documents: 3, Points: 8}
	b := Spend{Sent: 4, REST: 2, Documents: 1, Points: 3}
	if got, want := a.Minus(b), (Spend{Sent: 5, REST: 3, Documents: 2, Points: 5}); got != want {
		t.Errorf("Minus = %+v, want %+v", got, want)
	}
	if got := b.Plus(a.Minus(b)); got != a {
		t.Errorf("Plus(Minus) = %+v, want %+v", got, a)
	}
}
