package forgeconn_test

import (
	"slices"
	"testing"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/forgeconn/forgeconntest"
	"github.com/cplieger/forgeapi/v3"
)

func TestForgejo_before_17_offers_no_rerun(t *testing.T) {
	t.Parallel()
	w := world()
	w.Version = "16.0.0+gitea-1.22.0"
	srv, c := open(t, forgeconntest.Forgejo, w)
	if c.CanRerun() != forge.SupportNo {
		t.Errorf("CanRerun = %q, want no: the API document declares no re-run route", c.CanRerun())
	}
	sent := len(srv.Requests())
	run := forge.Run{Repo: appKey, ID: 102, Number: 2}
	wantErr(t, c.RerunFailedRun(t.Context(), &run), forge.KindUnsupported, forgeapi.CodeCapabilityUnsupported)
	wantErr(t, c.RerunFailedChecks(t.Context(), appItem(2), failed), forge.KindUnsupported, forgeapi.CodeCapabilityUnsupported)
	if got := srv.Requests()[sent:]; len(got) != 0 {
		t.Errorf("requests = %q, want none sent", got)
	}
	handledAll(t, srv)
}

func TestForgejo_rerun_of_a_run_that_did_not_fail_is_refused_before_the_write(t *testing.T) {
	t.Parallel()
	srv, c := open(t, forgeconntest.Forgejo, world())
	run := forge.Run{Repo: appKey, ID: 101, Number: 1}
	wantErr(t, c.RerunFailedRun(t.Context(), &run), forge.KindConflict, forgeapi.CodeRunNotFailed)
	if sent(srv, "POST ") {
		t.Errorf("requests = %q, want no re-run sent", srv.Requests())
	}
	handledAll(t, srv)
}

func TestGiteaFamily_removes_a_label_by_the_segment_its_product_takes(t *testing.T) {
	t.Parallel()
	// Gitea's route takes the label's id, Forgejo's its name.
	for _, tc := range []struct {
		product forgeconntest.Product
		route   string
	}{
		{forgeconntest.Gitea, "DELETE /api/v1/repos/acme/app/issues/1/labels/1"},
		{forgeconntest.Forgejo, "DELETE /api/v1/repos/acme/app/issues/1/labels/bug"},
	} {
		t.Run(string(tc.product), func(t *testing.T) {
			t.Parallel()
			srv, c := open(t, tc.product, world())
			if err := c.RemoveLabels(t.Context(), appItem(1), forge.KindPullRequest, []string{"bug"}); err != nil {
				t.Fatalf("RemoveLabels: %v", err)
			}
			if !slices.Contains(srv.Requests(), tc.route) {
				t.Errorf("requests = %q, want %q", srv.Requests(), tc.route)
			}
			handledAll(t, srv)
		})
	}
}
