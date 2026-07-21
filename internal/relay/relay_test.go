package relay

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newRelay() *Relay {
	r := New()
	r.logf = func(string, ...any) {}
	return r
}

// attach wires an unowned connector (serving h, announcing host) to r over an
// in-memory pipe, returning once the relay has registered it.
func attach(t *testing.T, r *Relay, host string, h http.Handler) {
	attachOwned(t, r, "", host, h)
}

// attachOwned wires a connector announcing (owner, host) to r.
func attachOwned(t *testing.T, r *Relay, owner, host string, h http.Handler) {
	t.Helper()
	a, b := net.Pipe()
	go r.AddConnector(a)
	go func() { _ = ServeConnector(b, host, owner, "", "", h, nil) }()
	t.Cleanup(func() { a.Close(); b.Close() })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.lookup(host, owner, owner != "") != nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("connector %q (owner %q) did not attach to the relay", host, owner)
}

// startPair wires a relay and a single connector (host) over a pipe.
func startPair(t *testing.T, host string, h http.Handler) *Relay {
	t.Helper()
	r := newRelay()
	attach(t, r, host, h)
	return r
}

func TestProxiesRequest(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "pong:"+r.URL.RawQuery)
	})
	r := startPair(t, "mac", h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/mac/api/ping?x=1", nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if rec.Header().Get("X-Test") != "yes" {
		t.Errorf("response header did not pass through: %v", rec.Header())
	}
	if rec.Body.String() != "pong:x=1" {
		t.Errorf("body = %q, want pong:x=1 (path+query must reach the connector, prefix stripped)", rec.Body.String())
	}
}

func TestProxiesPostBody(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(append([]byte("got:"), b...))
	})
	r := startPair(t, "mac", h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/l/mac/echo", strReader(`{"text":"hi"}`)))
	if rec.Body.String() != `got:{"text":"hi"}` {
		t.Errorf("body = %q, want the echoed request body", rec.Body.String())
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	// Shrink the cap for the test instead of allocating ~2x the real 48MiB
	// limit; the behavior being verified (413 over, 200 at) is independent of
	// the constant's actual value.
	orig := maxRoundTripBodyBytes
	maxRoundTripBodyBytes = 1024
	t.Cleanup(func() { maxRoundTripBodyBytes = orig })

	h := http.NewServeMux()
	h.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(append([]byte("got:"), b...))
	})
	r := startPair(t, "mac", h)

	// A body over the cap gets a clean 413 instead of being buffered whole and
	// failing further down the pipeline.
	oversized := strings.Repeat("x", int(maxRoundTripBodyBytes)+1)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/l/mac/echo", strings.NewReader(oversized)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// A body right at the cap still goes through normally.
	atCap := strings.Repeat("y", int(maxRoundTripBodyBytes))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/l/mac/echo", strings.NewReader(atCap)))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a body exactly at the cap", rec.Code)
	}
}

func TestStreamsChunks(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for _, c := range []string{"a", "b", "c"} {
			io.WriteString(w, c)
			if fl != nil {
				fl.Flush()
			}
		}
	})
	r := startPair(t, "mac", h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/mac/stream", nil))
	if rec.Body.String() != "abc" {
		t.Errorf("streamed body = %q, want abc", rec.Body.String())
	}
}

// TestLaptopNotConnected checks a request for a laptop with no connector returns
// 503 (not a hang or a 404).
func TestLaptopNotConnected(t *testing.T) {
	r := newRelay()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/ghost/api/projects", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for an unconnected laptop", rec.Code)
	}
}

// TestConnectorReceivesRelayVersion checks the relay sends its build version to
// a freshly-connected connector (the fWelcome handshake frame), so the terminal
// can warn when it's behind.
func TestConnectorReceivesRelayVersion(t *testing.T) {
	r := newRelay()
	r.SetVersion("3.4.5")

	a, b := net.Pipe()
	go r.AddConnector(a)
	got := make(chan string, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	go func() { _ = ServeConnector(b, "mac", "user", "", "", h, func(v string) { got <- v }) }()
	t.Cleanup(func() { a.Close(); b.Close() })

	select {
	case v := <-got:
		if v != "3.4.5" {
			t.Fatalf("relay version = %q, want 3.4.5", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connector never received the relay version (fWelcome)")
	}
}

// TestRelayVersionEndpoint checks GET /api/relay reports the relay's configured
// build version and whether CLI downloads are offered (so the web UI can flag a
// connector that's behind, with a Download link).
func TestRelayVersionEndpoint(t *testing.T) {
	r := newRelay()
	r.SetVersion("1.2.3")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/relay", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Version   string `json:"version"`
		Downloads bool   `json:"downloads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
	if got.Downloads {
		t.Errorf("downloads = true, want false when no downloads dir is set")
	}

	// With a downloads dir, the flag flips on.
	r.SetDownloads(t.TempDir())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/relay", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Downloads {
		t.Errorf("downloads = false, want true once a downloads dir is set")
	}

	// The public tunnel address is advertised once set (for the installer's
	// auto-daemon), and absent/empty otherwise.
	var withTunnel struct {
		Tunnel string `json:"tunnel"`
	}
	r.SetTunnelAddr("relay.example:8771")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/relay", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &withTunnel)
	if withTunnel.Tunnel != "relay.example:8771" {
		t.Errorf("tunnel = %q, want relay.example:8771", withTunnel.Tunnel)
	}
}

// TestLaptopsList checks GET /api/laptops lists connected connectors in
// registration order with a per-laptop base path.
func TestLaptopsList(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	r := newRelay()
	attach(t, r, "Moon-Mac", ok)
	attach(t, r, "work box", ok) // a space exercises URL-escaping in Base

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/laptops", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []LaptopInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d laptops, want 2: %+v", len(got), got)
	}
	if got[0].Host != "Moon-Mac" || got[0].Base != "/l/Moon-Mac" {
		t.Errorf("laptop 0 = %+v, want host Moon-Mac base /l/Moon-Mac", got[0])
	}
	if got[1].Host != "work box" || got[1].Base != "/l/work%20box" {
		t.Errorf("laptop 1 = %+v, want host 'work box' base /l/work%%20box", got[1])
	}
	if !got[0].Online || !got[1].Online {
		t.Errorf("attached laptops should be Online: %+v", got)
	}
}

// TestOfflineLaptopRemembered checks a connector that disconnects is still listed
// (Online=false, with LastSeen) so the UI can tell "offline" from "never seen",
// and that the offline entry respects per-user isolation.
func TestOfflineLaptopRemembered(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})
	r := newRelay()
	a, b := net.Pipe()
	go r.AddConnector(a)
	go func() { _ = ServeConnector(b, "Off-Mac", "alice", "", "", ok, nil) }()

	waitFor(t, func() bool { return r.lookup("Off-Mac", "alice", true) != nil }, "attach")
	if on := r.laptops("alice", true); len(on) != 1 || !on[0].Online {
		t.Fatalf("while connected: want 1 online laptop, got %+v", on)
	}

	// Disconnect and wait for the relay to deregister the live link.
	a.Close()
	b.Close()
	waitFor(t, func() bool { return r.lookup("Off-Mac", "alice", true) == nil }, "deregister")

	off := r.laptops("alice", true)
	if len(off) != 1 || off[0].Online || off[0].Host != "Off-Mac" || off[0].LastSeen == 0 {
		t.Fatalf("after disconnect: want 1 offline 'Off-Mac' with LastSeen, got %+v", off)
	}
	// Another user must not see alice's offline laptop.
	if other := r.laptops("bob", true); len(other) != 0 {
		t.Errorf("offline laptop leaked across users: %+v", other)
	}
}

// waitFor polls cond for up to 2s, failing with label if it never holds.
func waitFor(t *testing.T, cond func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

// TestRoutesToCorrectLaptop checks each laptop's requests reach its own
// connector, even with several attached.
func TestRoutesToCorrectLaptop(t *testing.T) {
	mk := func(name string) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) })
		return mux
	}
	r := newRelay()
	attach(t, r, "alpha", mk("alpha"))
	attach(t, r, "beta", mk("beta"))

	for _, host := range []string{"alpha", "beta"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/"+host+"/whoami", nil))
		if rec.Body.String() != host {
			t.Errorf("/l/%s/whoami = %q, want %q", host, rec.Body.String(), host)
		}
	}

	// An escaped host with a space routes too.
	attach(t, r, "my box", mk("my box"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/my%20box/whoami", nil))
	if rec.Body.String() != "my box" {
		t.Errorf("escaped-host route = %q, want 'my box'", rec.Body.String())
	}
}

// TestPerUserIsolation checks that with an owner resolver set, each web user
// only lists and can reach the connectors they launched — even when two users
// have a laptop with the same host name.
func TestPerUserIsolation(t *testing.T) {
	mk := func(name string) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) })
		return mux
	}
	r := newRelay()
	attachOwned(t, r, "alice", "MacBook", mk("alice-mac"))
	attachOwned(t, r, "bob", "MacBook", mk("bob-mac")) // same host name as Alice's
	attachOwned(t, r, "alice", "mini", mk("alice-mini"))

	// Resolve the "logged-in" user from a test header (stands in for the auth session).
	r.SetOwnerResolver(func(req *http.Request) (string, bool) {
		if u := req.Header.Get("X-Test-User"); u != "" {
			return u, true
		}
		return "", false
	})
	as := func(user, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Alice's list shows only her two laptops.
	var list []LaptopInfo
	if err := json.Unmarshal(as("alice", "/api/laptops").Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("alice should see 2 laptops, got %d: %+v", len(list), list)
	}

	// Same host name, but each user reaches their OWN connector (no collision).
	if got := as("alice", "/l/MacBook/whoami").Body.String(); got != "alice-mac" {
		t.Errorf("alice /l/MacBook = %q, want alice-mac", got)
	}
	if got := as("bob", "/l/MacBook/whoami").Body.String(); got != "bob-mac" {
		t.Errorf("bob /l/MacBook = %q, want bob-mac", got)
	}

	// Bob can't reach Alice's mini — it exists but isn't his (403).
	if code := as("bob", "/l/mini/whoami").Code; code != http.StatusForbidden {
		t.Errorf("bob /l/mini should be 403, got %d", code)
	}
	// A host nobody has → 503.
	if code := as("alice", "/l/ghost/whoami").Code; code != http.StatusServiceUnavailable {
		t.Errorf("alice /l/ghost should be 503, got %d", code)
	}
	// A user with no connectors sees an empty list.
	list = nil
	if err := json.Unmarshal(as("carol", "/api/laptops").Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("carol should see 0 laptops, got %d", len(list))
	}
}

// TestServesShell checks the relay serves the injected SPA shell for non-API,
// non-laptop paths, while /api/laptops stays a relay endpoint.
func TestServesShell(t *testing.T) {
	r := newRelay()
	r.SetShell(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "SHELL:"+req.URL.Path)
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "SHELL:/" {
		t.Errorf("/ = %q, want the shell", rec.Body.String())
	}

	// /api/laptops is the relay's own endpoint, not the shell.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/laptops", nil))
	if rec.Code != http.StatusOK || rec.Body.String() == "SHELL:/api/laptops" {
		t.Errorf("/api/laptops should be served by the relay, got %d %q", rec.Code, rec.Body.String())
	}
}

// TestCancelPropagates checks that when the browser disconnects, the relay sends
// a cancel and the connector cancels the served request's context.
func TestCancelPropagates(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	h := http.NewServeMux()
	h.HandleFunc("GET /hang", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // blocks until the browser-side cancel propagates
		close(canceled)
	})
	r := startPair(t, "mac", h)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/mac/hang", nil).WithContext(ctx))
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}
	cancel() // browser goes away
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not propagate to the connector")
	}
	<-done
}

func strReader(s string) io.Reader { return &sliceReader{b: []byte(s)} }

type sliceReader struct {
	b []byte
	i int
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.i >= len(s.b) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.i:])
	s.i += n
	return n, nil
}

// ensure concurrent requests over one tunnel don't corrupt each other.
func TestConcurrentRequests(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("GET /n/{v}", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.PathValue("v"))
	})
	r := startPair(t, "mac", h)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			path := "/l/mac/n/" + itoa(i)
			r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Body.String() != itoa(i) {
				t.Errorf("req %d got %q", i, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestHealthz checks the relay answers /healthz with 200 unauthenticated and
// with no connectors attached, so Kube/load-balancer probes work.
func TestHealthz(t *testing.T) {
	r := newRelay()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("/healthz = %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

// TestLaptopVersionReported checks a connector's build version (from its hello
// frame) is surfaced in GET /api/laptops, so the web UI can flag/clear an
// out-of-date terminal from the live laptop poll.
func TestLaptopVersionReported(t *testing.T) {
	r := newRelay()
	a, b := net.Pipe()
	go r.AddConnector(a)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	go func() { _ = ServeConnector(b, "Ver-Mac", "", "", "2.4.0", ok, nil) }()
	t.Cleanup(func() { a.Close(); b.Close() })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/laptops", nil))
		var laps []LaptopInfo
		_ = json.Unmarshal(rec.Body.Bytes(), &laps)
		if len(laps) == 1 && laps[0].Version == "2.4.0" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connector version 2.4.0 was not reported in /api/laptops")
}

// TestPendingReqUnboundedAndOrdered checks the per-request buffer: push never
// blocks and preserves order well past the old fixed cap, pop drains in order,
// and overflowing maxPendingFrames closes the request (the safety valve for a
// stalled consumer).
func TestPendingReqUnboundedAndOrdered(t *testing.T) {
	pr := newPendingReq()
	for i := 0; i < 100; i++ { // 100 > the old 16-slot channel buffer
		if !pr.push(&frame{ID: uint64(i)}) {
			t.Fatalf("push %d returned false before the cap", i)
		}
	}
	for i := 0; i < 100; i++ {
		f, _ := pr.pop()
		if f == nil || f.ID != uint64(i) {
			t.Fatalf("pop %d = %v, want frame id %d (ordered)", i, f, i)
		}
	}
	if f, closed := pr.pop(); f != nil || closed {
		t.Fatalf("drained pop = (%v, %v), want (nil, false)", f, closed)
	}

	// Overflow: a consumer that never drains must not buffer without bound.
	over := newPendingReq()
	for i := 0; i < maxPendingFrames; i++ {
		if !over.push(&frame{}) {
			t.Fatalf("push %d returned false before maxPendingFrames", i)
		}
	}
	if over.push(&frame{}) {
		t.Fatal("push past maxPendingFrames should return false (overflow)")
	}
	if _, closed := over.pop(); !closed {
		t.Fatal("overflow should mark the request closed")
	}
}

// blockingRW is an http.ResponseWriter whose first body Write blocks until
// release is closed (and closes blocked when it first stalls), simulating a
// browser consumer that has stalled mid-stream.
type blockingRW struct {
	hdr     http.Header
	release chan struct{}
	blocked chan struct{}
	once    sync.Once
}

func (b *blockingRW) Header() http.Header { return b.hdr }
func (b *blockingRW) WriteHeader(int)     {}
func (b *blockingRW) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.blocked) })
	<-b.release
	return len(p), nil
}

// TestNoHeadOfLineBlocking is the regression test for the multiplexer stall: a
// single readLoop fans every response on a connector out to its request, so a
// request whose browser consumer has stalled used to back up the shared buffer
// and freeze the whole tunnel — blocking new asks on other sessions. A stalled
// request must no longer block another.
func TestNoHeadOfLineBlocking(t *testing.T) {
	const floodFrames = 40 // > the old 16-frame per-request buffer
	h := http.NewServeMux()
	h.HandleFunc("GET /flood", func(w http.ResponseWriter, _ *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < floodFrames; i++ {
			io.WriteString(w, "x")
			if fl != nil {
				fl.Flush()
			}
		}
	})
	h.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "pong")
	})
	r := startPair(t, "mac", h)

	// Request A: its consumer blocks on the first write, so the flood of response
	// frames piles up undrained (enough to overflow the old fixed buffer).
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	blocked := make(chan struct{})
	go r.ServeHTTP(
		&blockingRW{hdr: http.Header{}, release: release, blocked: blocked},
		httptest.NewRequest("GET", "/l/mac/flood", nil),
	)
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("flood request never started writing")
	}
	time.Sleep(50 * time.Millisecond) // let the flood back up behind the stall

	// Request B must still complete promptly while A is stuck.
	done := make(chan string, 1)
	go func() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/l/mac/ping", nil))
		done <- rec.Body.String()
	}()
	select {
	case body := <-done:
		if body != "pong" {
			t.Fatalf("B body = %q, want pong", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled request blocked another over the same tunnel (head-of-line blocking)")
	}
}

// TestConnectorAuth verifies SetConnectorAuth: a connector with a valid token
// registers under the token's TRUSTED owner (ignoring the self-declared User),
// and one with a bad/missing token is rejected outright.
func TestConnectorAuth(t *testing.T) {
	r := newRelay()
	// Verifier: only "goodtoken" is valid, and it authenticates owner "alice".
	r.SetConnectorAuth(func(tok string) (string, bool) {
		if tok == "goodtoken" {
			return "alice", true
		}
		return "", false
	})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })

	// Valid token, but the connector self-declares owner "eve" — the relay must
	// use the token's "alice", not "eve".
	a, b := net.Pipe()
	go r.AddConnector(a)
	go func() { _ = ServeConnector(b, "mac", "eve", "goodtoken", "", h, nil) }()
	t.Cleanup(func() { a.Close(); b.Close() })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.lookup("mac", "alice", true) == nil {
		time.Sleep(2 * time.Millisecond)
	}
	if r.lookup("mac", "alice", true) == nil {
		t.Fatal("valid-token connector did not register under the trusted owner")
	}
	if r.lookup("mac", "eve", true) != nil {
		t.Error("connector registered under the self-declared owner instead of the token owner")
	}

	// Bad token → rejected: never registers, and the relay closes the connection.
	c, d := net.Pipe()
	go r.AddConnector(c)
	errc := make(chan error, 1)
	go func() { errc <- ServeConnector(d, "mac2", "eve", "badtoken", "", h, nil) }()
	t.Cleanup(func() { c.Close(); d.Close() })

	select {
	case <-errc: // ServeConnector returned because the relay closed the tunnel
	case <-time.After(2 * time.Second):
		t.Fatal("bad-token connector was not rejected")
	}
	if r.lookup("mac2", "eve", true) != nil || r.lookup("mac2", "", false) != nil {
		t.Error("bad-token connector should not have registered")
	}
}
