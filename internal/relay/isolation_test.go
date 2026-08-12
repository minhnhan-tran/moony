package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	isoOwner = "alice@example.com"
	isoHost  = "laptop-a"
)

// These use attachOwned (relay_test.go:29), which wires a real connector over a
// net.Pipe and waits for it to register, rather than poking r.links directly: a
// hand-built fixture that forgot r.order would make laptops() return nothing and
// the assertions below would pass whether or not the bug was present.

// With auth on, an anonymous request must see nothing. The ownership checks are
// guarded by `if filter && ...`, so a request whose owner cannot be resolved used
// to skip them entirely — the widest possible result from the narrowest input.
func TestAnonymousRequestSeesNoConnectors(t *testing.T) {
	r := New()
	attachOwned(t, r, isoOwner, isoHost, http.NotFoundHandler())

	// Guard against a vacuous test: unfiltered, the connector really is visible.
	if got := r.laptops("", false); len(got) != 1 {
		t.Fatalf("fixture is not wired: unfiltered listing returned %d connectors, want 1", len(got))
	}

	r.SetOwnerResolver(func(*http.Request) (string, bool) { return "", false })
	req := httptest.NewRequest(http.MethodGet, "/api/laptops", nil)
	account, filter := r.resolveOwner(req)
	if !filter {
		t.Fatal("an unresolved owner must still filter; otherwise every connector is listed")
	}
	if got := r.laptops(account, filter); len(got) != 0 {
		t.Errorf("anonymous request listed %d connectors: %+v", len(got), got)
	}
	if l := r.lookup(isoHost, account, filter); l != nil {
		t.Error("anonymous request resolved another account's connector")
	}
}

// An authenticated owner still sees their own connector — the fix must not lock
// everyone out.
func TestOwnerStillSeesOwnConnector(t *testing.T) {
	r := New()
	attachOwned(t, r, isoOwner, isoHost, http.NotFoundHandler())
	r.SetOwnerResolver(func(*http.Request) (string, bool) { return isoOwner, true })

	req := httptest.NewRequest(http.MethodGet, "/api/laptops", nil)
	account, filter := r.resolveOwner(req)
	if got := r.laptops(account, filter); len(got) != 1 {
		t.Errorf("owner saw %d of their own connectors, want 1", len(got))
	}
	if l := r.lookup(isoHost, account, filter); l == nil {
		t.Error("owner could not resolve their own connector")
	}
}

// Without a resolver (auth disabled) the relay stays open by design: one operator,
// no accounts. This pins that difference so the change above cannot be mistaken
// for a general lockdown.
func TestNoResolverMeansNoFiltering(t *testing.T) {
	r := New()
	attachOwned(t, r, isoOwner, isoHost, http.NotFoundHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/laptops", nil)
	if _, filter := r.resolveOwner(req); filter {
		t.Error("with no owner resolver the relay should not filter")
	}
}
