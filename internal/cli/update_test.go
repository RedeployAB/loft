package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RedeployAB/loft/internal/release"
)

// answered builds a check that has already resolved to cfg.
func answered(cfg cliConfig) *versionCheck {
	vc := &versionCheck{done: make(chan struct{}), cfg: cfg}
	close(vc.done)
	return vc
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := release.Version
	release.Version = v
	t.Cleanup(func() { release.Version = prev })
}

func platform(version, minCLI string) cliConfig {
	return cliConfig{Version: version, CLI: release.Policy{Min: minCLI}}
}

// TestVerdicts covers the wiring around release.Policy: the platform's answer feeds the refusal and
// the notice, no answer or an unstamped build yields neither.
func TestVerdicts(t *testing.T) {
	withVersion(t, "v0.1.6")
	if err := answered(platform("v0.1.7", "v0.1.7")).errUnsupported(); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("too old: got %v", err)
	}
	if err := answered(platform("v0.1.7", "v0.1.0")).errUnsupported(); err != nil {
		t.Fatalf("supported: got %v", err)
	}
	if latest, cur, ok := answered(platform("v0.1.8", "v0.1.0")).newerRelease(); !ok || latest.String() != "v0.1.8" || cur.String() != "v0.1.6" {
		t.Fatalf("newer: got %v %v %v", latest, cur, ok)
	}
	if _, _, ok := answered(platform("v0.1.6", "v0.1.0")).newerRelease(); ok {
		t.Fatal("same version reported as newer")
	}
	// No answer (fetch failed, or a platform that predates the fields): no verdict either way.
	none := answered(cliConfig{Issuer: "x"})
	if err := none.errUnsupported(); err != nil {
		t.Fatalf("no answer refused: %v", err)
	}
	if _, _, ok := none.newerRelease(); ok {
		t.Fatal("no answer reported a newer release")
	}

	// A dev build is not a release: never refused, never nagged, whatever the platform says.
	withVersion(t, "dev")
	worst := answered(cliConfig{Version: "v9.9.9", CLI: release.Policy{Min: "v9.0.0", Blocked: []string{"dev"}}})
	if err := worst.errUnsupported(); err != nil {
		t.Fatalf("dev build refused: %v", err)
	}
	if _, _, ok := worst.newerRelease(); ok {
		t.Fatal("dev build reported a newer release")
	}
}

// TestStartVersionCheck runs the check against a platform over HTTP: the CLI identifies itself, an
// unsupported CLI is refused before any upload, and an unreachable platform or an opted-out user
// yields no verdict rather than an error.
func TestStartVersionCheck(t *testing.T) {
	withVersion(t, "v0.1.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/loft" {
			http.NotFound(w, r)
			return
		}
		if r.UserAgent() != release.UserAgent() {
			t.Errorf("User-Agent = %q, want %q", r.UserAgent(), release.UserAgent())
		}
		_ = json.NewEncoder(w).Encode(platform("v0.2.0", "v0.1.5"))
	}))
	defer srv.Close()

	err := startVersionCheck(context.Background(), srv.URL).errUnsupported()
	if err == nil || !strings.Contains(err.Error(), "needs v0.1.5") {
		t.Fatalf("got %v, want a too-old refusal", err)
	}

	t.Setenv("LOFT_NO_UPDATE_CHECK", "1")
	if err := startVersionCheck(context.Background(), srv.URL).errUnsupported(); err != nil {
		t.Fatalf("opted out but refused: %v", err)
	}
	t.Setenv("LOFT_NO_UPDATE_CHECK", "")

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if err := startVersionCheck(context.Background(), gone.URL).errUnsupported(); err != nil {
		t.Fatalf("unreachable platform: got %v, want nil", err)
	}
	if err := startVersionCheck(context.Background(), "").errUnsupported(); err != nil {
		t.Fatalf("no platform: got %v, want nil", err)
	}
}
