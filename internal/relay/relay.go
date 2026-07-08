package relay

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/announce"
)

// laptopPrefix is the browser path under which a specific laptop's API is
// addressed, e.g. /l/My-Mac/api/projects -> the My-Mac connector's /api/projects.
const laptopPrefix = "/l/"

// Relay proxies browser HTTP traffic to one or more connected connectors
// ("laptops") over their tunnels. Each connector dials in and announces its host
// and owner (the user who launched it). The relay lists connectors
// at GET /api/laptops and routes /l/{host}/... requests to that host's
// connector. When an owner resolver is set (SetOwnerResolver), it isolates per
// user: each web user only lists and reaches the connectors they own. The SPA
// shell (/, /static) is served from an injected handler so the app loads even
// with no connectors. The relay holds no agent and no session state.
type Relay struct {
	mu    sync.Mutex
	links map[string]*link // connectors keyed by linkKey(owner, host)
	order []string         // link keys in registration order, for stable listing
	shell http.Handler     // serves the SPA shell (/ + /static); may be nil
	// announceAPI, when set, serves the announcements API at /api/announcements
	// (GET + POST/DELETE). announce holds the same store so the relay can fold
	// the current list into GET /api/relay, which the web UI and TUI already
	// poll. Both may be nil.
	announceAPI http.Handler
	announce    *announce.Store
	// isAdmin reports whether a request may author announcements; used to set the
	// "admin" hint in GET /api/relay so the SPA can show the compose controls.
	// With no built-in auth this is always true. nil => not admin.
	isAdmin func(*http.Request) bool
	// version is this relay binary's build version, surfaced at GET /api/relay so
	// the web UI can warn when a connected terminal (connector) is older.
	version string
	// downloads, when non-empty, is a directory of prebuilt CLI binaries the
	// relay offers at GET /download (a landing page), GET /download/{file} (the
	// file), and GET /install.sh (a curl|sh installer). Empty leaves them off.
	downloads string
	// tunnelAddr is the PUBLIC host:port connectors dial to reach this relay's
	// tunnel (the L4 endpoint fronting -tunnel), advertised at GET /api/relay and
	// baked into /install.sh so a freshly-installed machine can auto-register its
	// background daemon. Empty disables the installer's auto-daemon step.
	tunnelAddr string
	// owner resolves the web user's account for a request, returning ok=false
	// when the request is anonymous. nil disables per-user isolation (every
	// connector is visible), which is the default now that there's no login.
	owner func(*http.Request) (account string, ok bool)

	// connAuth verifies a connector's fHello token and returns the authenticated
	// owner. When set, AddConnector rejects a connector whose token doesn't
	// verify and derives the trusted owner from the token (ignoring the
	// self-declared User). nil disables connector auth (owner is self-declared),
	// which is the default now that there's no login.
	connAuth func(token string) (owner string, ok bool)

	// seen records the last time each connector (owner+host) was attached, kept
	// after it disconnects so the web UI can distinguish a laptop that's offline
	// (installed but not running) from one that's never connected. In-memory and
	// best-effort: a relay restart forgets it, and old entries are pruned.
	seen map[string]seenInfo

	logf func(string, ...any)
}

// seenInfo is a remembered connector: its host/owner and when it was last seen.
type seenInfo struct {
	host  string
	owner string
	at    time.Time
}

// seenTTL bounds how long a disconnected connector is remembered.
const seenTTL = 30 * 24 * time.Hour

// linkKey namespaces a connector by its owner so two users with the same host
// name don't collide. Owner is lower-cased so matching is case-insensitive.
func linkKey(owner, host string) string {
	return strings.ToLower(strings.TrimSpace(owner)) + "\x00" + host
}

// LaptopInfo describes one connector for GET /api/laptops. Base is the path
// prefix the browser uses for that laptop's API (e.g. "/l/My-Mac"). Online is
// false for a laptop that has connected before but isn't attached now; LastSeen
// (unix seconds) is set for those so the UI can show "offline since …".
type LaptopInfo struct {
	Host     string `json:"host"`
	Name     string `json:"name"`
	Base     string `json:"base"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen,omitempty"`
	Version  string `json:"version,omitempty"` // the connector's build version (online laptops)
}

// pendingReq couples an in-flight browser request to the response frames
// streaming back for it. Frames are held in an unbounded, ordered queue that the
// consumer (roundTrip) drains; the shared readLoop only ever appends (push never
// blocks). That isolation is what stops one slow or stalled browser from
// stalling every other session multiplexed over the same connector tunnel —
// head-of-line blocking — since all of a connector's in-flight requests share a
// single readLoop goroutine. wake nudges the waiting consumer that the queue or
// closed state changed.
type pendingReq struct {
	mu     sync.Mutex
	queue  []*frame
	closed bool          // connector dropped (or we gave up on it): no more frames
	wake   chan struct{} // buffered(1) nudge to the waiting consumer
}

// maxPendingFrames caps the per-request response backlog. It's a safety valve
// for a consumer that has stalled WITHOUT disconnecting (e.g. a laptop that slept
// mid-stream): rather than grow a queue without bound — or, as before, block the
// shared read loop and stall every other session — the relay drops just that one
// request (an SSE client simply reconnects). Generous, so normal streaming never
// trips it; a healthy consumer keeps the queue near-empty.
const maxPendingFrames = 2048

func newPendingReq() *pendingReq { return &pendingReq{wake: make(chan struct{}, 1)} }

// push appends a response frame for the consumer and nudges it. It never blocks
// the caller (the shared readLoop). It returns false when the request is already
// closed or has overflowed its backlog, signalling the caller to stop routing
// frames to it (and to tell the connector to cancel it).
func (pr *pendingReq) push(f *frame) bool {
	pr.mu.Lock()
	if pr.closed {
		pr.mu.Unlock()
		return false
	}
	if len(pr.queue) >= maxPendingFrames {
		pr.closed = true // stalled consumer: give up on this one request
		pr.mu.Unlock()
		pr.signal()
		return false
	}
	pr.queue = append(pr.queue, f)
	pr.mu.Unlock()
	pr.signal()
	return true
}

// markClosed records that no more frames will arrive (the connector dropped).
// The consumer drains whatever is already queued, then observes the closure.
func (pr *pendingReq) markClosed() {
	pr.mu.Lock()
	pr.closed = true
	pr.mu.Unlock()
	pr.signal()
}

// pop removes and returns the next queued frame in order. f is nil when the
// queue is empty; closed reports whether the stream has ended, so an empty queue
// with closed==true is end-of-stream.
func (pr *pendingReq) pop() (f *frame, closed bool) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(pr.queue) > 0 {
		f = pr.queue[0]
		pr.queue[0] = nil // drop the reference so the frame can be GC'd
		pr.queue = pr.queue[1:]
		if len(pr.queue) == 0 {
			pr.queue = nil // fully drained — release the backing array
		}
	}
	return f, pr.closed
}

func (pr *pendingReq) signal() {
	select {
	case pr.wake <- struct{}{}:
	default:
	}
}

// New returns a relay with no connectors attached yet.
func New() *Relay {
	return &Relay{links: map[string]*link{}, seen: map[string]seenInfo{}, logf: log.Printf}
}

// SetShell sets the handler that serves the SPA shell (index + static assets)
// for any path that isn't /api/laptops or /l/{host}/... . Pass web.SPAHandler().
func (r *Relay) SetShell(h http.Handler) { r.shell = h }

// SetAnnouncements wires the announcements feature: api serves
// /api/announcements (pass web.AnnouncementsHandler), store lets GET /api/relay
// include the current list, and isAdmin marks whether the requester may author.
// Any may be nil to leave the feature off.
func (r *Relay) SetAnnouncements(store *announce.Store, api http.Handler, isAdmin func(*http.Request) bool) {
	r.announce = store
	r.announceAPI = api
	r.isAdmin = isAdmin
}

// SetVersion records this relay's build version, served at GET /api/relay so the
// web UI can compare it against a connector's version and warn when the terminal
// is behind. Empty when unset (the endpoint then reports an empty version).
func (r *Relay) SetVersion(v string) { r.version = v }

// SetDownloads enables the binary-download endpoints, serving files from dir at
// GET /download/{file}, a landing page at GET /download, and a curl|sh installer
// at GET /install.sh. Empty (the default) leaves those routes off (404), so the
// SPA shell handles those paths instead.
func (r *Relay) SetDownloads(dir string) { r.downloads = dir }

// SetTunnelAddr records the public host:port connectors dial to reach this
// relay's tunnel. It's advertised at GET /api/relay and substituted into
// /install.sh so the installer can auto-register the machine's background
// daemon. Empty (the default) leaves the installer binary-only.
func (r *Relay) SetTunnelAddr(addr string) { r.tunnelAddr = strings.TrimSpace(addr) }

// SetOwnerResolver enables per-user isolation: f returns the authenticated web
// user's auth account for a request (ok=false when auth is off or the
// request is anonymous). When set, each user only sees and can reach the
// connectors whose owner matches their account. Pass nil to show every connector.
func (r *Relay) SetOwnerResolver(f func(*http.Request) (string, bool)) { r.owner = f }

// SetConnectorAuth enables connector authentication: f verifies a connector's
// fHello token and returns the authenticated owner (ok=false to reject). When
// set, AddConnector refuses any connector whose token doesn't verify and uses
// the token's owner as the trusted identity, ignoring the self-declared User
// field. Pass nil (the default) to accept any connector with a self-declared
// owner. Pair this with SetOwnerResolver so the same accounts line up.
func (r *Relay) SetConnectorAuth(f func(token string) (owner string, ok bool)) { r.connAuth = f }

// Connected reports whether at least one connector is currently attached.
func (r *Relay) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.links) > 0
}

// laptops snapshots the connectors visible to account: those attached now
// (online, in registration order) followed by ones seen before but not attached
// now (offline, most-recent first), so the UI can offer "reconnect" for an
// offline laptop and "install" only when the user has truly never connected.
// When filter is true only the user's own connectors are returned.
func (r *Relay) laptops(account string, filter bool) []LaptopInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LaptopInfo, 0, len(r.links))
	online := make(map[string]bool, len(r.links))
	for _, k := range r.order {
		l := r.links[k]
		if l == nil {
			continue
		}
		online[k] = true
		if filter && !strings.EqualFold(l.owner, account) {
			continue
		}
		out = append(out, LaptopInfo{Host: l.host, Name: l.host, Base: laptopPrefix + url.PathEscape(l.host), Online: true, Version: l.version})
	}
	// Offline: known connectors not attached right now.
	var offline []seenInfo
	for k, s := range r.seen {
		if online[k] {
			continue
		}
		if filter && !strings.EqualFold(s.owner, account) {
			continue
		}
		offline = append(offline, s)
	}
	sort.Slice(offline, func(i, j int) bool { return offline[i].at.After(offline[j].at) })
	for _, s := range offline {
		out = append(out, LaptopInfo{Host: s.host, Name: s.host, Base: laptopPrefix + url.PathEscape(s.host), Online: false, LastSeen: s.at.Unix()})
	}
	return out
}

// lookup resolves the connector for host. When filter is true it returns only
// the connector owned by account (per-user isolation); otherwise it returns any
// connector with that host.
func (r *Relay) lookup(host, account string, filter bool) *link {
	r.mu.Lock()
	defer r.mu.Unlock()
	if filter {
		return r.links[linkKey(account, host)]
	}
	for _, l := range r.links {
		if l.host == host {
			return l
		}
	}
	return nil
}

// hostExists reports whether any connected connector has this host (regardless
// of owner), so the router can tell "not yours" (403) from "not connected" (503).
func (r *Relay) hostExists(host string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.links {
		if l.host == host {
			return true
		}
	}
	return false
}

// AddConnector registers a freshly-accepted connector. It reads the connector's
// hello frame (its host + owner), registers it under (owner, host) — replacing
// any existing connector with the same owner+host (last wins) — serves its
// tunnel until the connection drops, then deregisters. Blocks for the lifetime
// of the connection.
func (r *Relay) AddConnector(conn net.Conn) {
	t := newTunnel(conn)
	hello, err := t.readFrame()
	if err != nil || hello.Type != fHello {
		t.close() // a connector must say hello first
		return
	}
	host := strings.TrimSpace(hello.Host)
	if host == "" {
		host = conn.RemoteAddr().String()
	}
	owner := strings.TrimSpace(hello.User)
	// When connector auth is enabled, the owner is derived from a verified token,
	// not self-declared: reject any connector whose token doesn't verify.
	if r.connAuth != nil {
		verified, ok := r.connAuth(hello.Token)
		if !ok {
			r.logf("relay: connector %q rejected — invalid or missing connector token", host)
			t.close()
			return
		}
		owner = strings.TrimSpace(verified)
	}
	key := linkKey(owner, host)
	l := &link{host: host, owner: owner, version: strings.TrimSpace(hello.Ver), t: t, pending: map[uint64]*pendingReq{}, logf: r.logf}

	r.mu.Lock()
	old := r.links[key]
	if old == nil {
		r.order = append(r.order, key)
	}
	r.links[key] = l
	r.markSeenLocked(key, host, owner)
	r.mu.Unlock()
	if old != nil {
		r.logf("relay: connector %q (owner %q) reconnected — replacing the previous connection (two connectors on one machine evict each other; run only one)", host, owner)
		old.t.close() // same owner+host reconnect: last wins
	}
	r.logf("relay: connector %q (owner %q) connected from %s", host, owner, conn.RemoteAddr())

	// Tell the connector our version so its TUI can warn when the terminal is
	// behind this relay (the mirror of the web UI's version banner). Best effort:
	// an older connector just ignores this frame.
	_ = t.writeFrame(&frame{Type: fWelcome, Ver: r.version})

	l.readLoop()

	r.mu.Lock()
	if r.links[key] == l {
		delete(r.links, key)
		for i, k := range r.order {
			if k == key {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
	r.markSeenLocked(key, host, owner) // last-seen = disconnect time
	r.mu.Unlock()
	t.close()
	l.failAll() // unblock any browser requests waiting on this connector
	r.logf("relay: connector %q (owner %q) disconnected", host, owner)
}

// markSeenLocked records that (host, owner) was just seen and prunes entries
// older than seenTTL. Caller holds r.mu.
func (r *Relay) markSeenLocked(key, host, owner string) {
	now := time.Now()
	r.seen[key] = seenInfo{host: host, owner: owner, at: now}
	for k, s := range r.seen {
		if now.Sub(s.at) > seenTTL {
			delete(r.seen, k)
		}
	}
}

// ServeHTTP routes a browser request: the laptops list and per-laptop API are
// handled here (scoped to the authenticated user when isolation is on);
// everything else falls to the SPA shell.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	account, filter := r.resolveOwner(req)
	switch {
	case req.URL.Path == "/healthz":
		// Unauthenticated liveness/readiness probe for load balancers and Kube.
		// (auth.Wrap treats /healthz as public, so it never gets gated.)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
		return
	case req.URL.Path == "/api/laptops":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.laptops(account, filter))
		return
	case req.URL.Path == "/api/relay":
		// This relay's build version (+ whether it offers CLI downloads), so the
		// SPA can flag an out-of-date terminal and the CLI's /update can check
		// the latest version. Public (see internal/auth): no per-user data.
		w.Header().Set("Content-Type", "application/json")
		info := map[string]any{
			"version":   r.version,
			"downloads": r.downloads != "",
			"tunnel":    r.tunnelAddr,
		}
		info["admin"] = r.isAdmin != nil && r.isAdmin(req)
		// Announcements (the banner) are shown to everyone; only authoring is
		// admin-gated (handled by the announcements API + the UI hiding the menu).
		if r.announce != nil {
			info["announcements"] = r.announce.List()
		}
		_ = json.NewEncoder(w).Encode(info)
		return
	case req.URL.Path == "/install.sh":
		r.serveInstallScript(w, req)
		return
	case req.URL.Path == "/api/announcements" && r.announceAPI != nil:
		// Maintainer announcement banners, shared across the whole fleet.
		r.announceAPI.ServeHTTP(w, req)
		return
	case req.URL.Path == "/download":
		r.serveDownloadPage(w, req)
		return
	case strings.HasPrefix(req.URL.Path, "/download/"):
		r.serveDownloadFile(w, req)
		return
	case strings.HasPrefix(req.URL.Path, laptopPrefix):
		host, fwd := splitLaptopPath(req.URL.Path)
		l := r.lookup(host, account, filter)
		if l == nil {
			if filter && r.hostExists(host) {
				http.Error(w, "That laptop belongs to another user.", http.StatusForbidden)
			} else {
				http.Error(w, "That laptop is not connected. Start the moony connector on it.", http.StatusServiceUnavailable)
			}
			return
		}
		if req.URL.RawQuery != "" {
			fwd += "?" + req.URL.RawQuery
		}
		l.roundTrip(w, req, fwd)
	default:
		if r.shell != nil {
			r.shell.ServeHTTP(w, req)
			return
		}
		http.NotFound(w, req)
	}
}

// resolveOwner returns the authenticated web user's account and whether per-user
// isolation applies (false when no resolver is set or the request is anonymous).
func (r *Relay) resolveOwner(req *http.Request) (string, bool) {
	if r.owner == nil {
		return "", false
	}
	return r.owner(req)
}

// splitLaptopPath splits "/l/{host}/rest..." into the (URL-decoded) host and the
// forward path "/rest..." sent to that laptop's connector.
func splitLaptopPath(p string) (host, forward string) {
	rest := p[len(laptopPrefix):]
	hostEnc := rest
	forward = "/"
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostEnc, forward = rest[:i], rest[i:]
	}
	if dec, err := url.PathUnescape(hostEnc); err == nil {
		host = dec
	} else {
		host = hostEnc
	}
	return host, forward
}

// link is one connected connector: its tunnel plus the in-flight request
// bookkeeping for proxying browser requests to it.
type link struct {
	host    string
	owner   string // the username that launched this connector
	version string // the connector's build version (from its hello frame)
	t       *tunnel

	nextID  uint64
	pmu     sync.Mutex
	pending map[uint64]*pendingReq

	logf func(string, ...any)
}

// readLoop dispatches response frames from the connector to the matching
// in-flight browser request, keyed by id. It must never block: every request on
// this connector is multiplexed through this one goroutine, so it hands each
// frame to the request's own queue (push) rather than waiting on a consumer.
func (l *link) readLoop() {
	for {
		f, err := l.t.readFrame()
		if err != nil {
			return
		}
		l.pmu.Lock()
		pr := l.pending[f.ID]
		l.pmu.Unlock()
		if pr == nil {
			continue // request already finished or abandoned
		}
		if !pr.push(f) {
			// The consumer overflowed its backlog (stalled without disconnecting)
			// or is already closed. Stop routing to it and tell the connector to
			// cancel — on a separate goroutine, so a blocked tunnel write can never
			// stall this read loop (which would re-introduce head-of-line blocking).
			l.pmu.Lock()
			delete(l.pending, f.ID)
			l.pmu.Unlock()
			id := f.ID
			go func() { _ = l.t.writeFrame(&frame{Type: fCancel, ID: id}) }()
		}
	}
}

func (l *link) register(id uint64) *pendingReq {
	pr := newPendingReq()
	l.pmu.Lock()
	l.pending[id] = pr
	l.pmu.Unlock()
	return pr
}

func (l *link) unregister(id uint64, pr *pendingReq) {
	l.pmu.Lock()
	if l.pending[id] == pr {
		delete(l.pending, id)
	}
	l.pmu.Unlock()
}

// failAll wakes every waiting browser request after the connector dropped. It's
// called only once the read loop has stopped, so no goroutine is still pushing
// to these requests; each consumer drains whatever's queued, then sees the close.
func (l *link) failAll() {
	l.pmu.Lock()
	for _, pr := range l.pending {
		pr.markClosed()
	}
	l.pending = map[uint64]*pendingReq{}
	l.pmu.Unlock()
}

// roundTrip proxies one browser request to this connector (sending forwardPath
// as the request path) and streams the response back. SSE works because each
// connector body frame is written and flushed as it arrives.
func (l *link) roundTrip(w http.ResponseWriter, req *http.Request, forwardPath string) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}

	id := atomic.AddUint64(&l.nextID, 1)
	pr := l.register(id)
	defer l.unregister(id, pr)

	if err := l.t.writeFrame(&frame{Type: fReq, ID: id, Method: req.Method, Path: forwardPath, Header: req.Header, Body: body}); err != nil {
		http.Error(w, "connector unavailable", http.StatusBadGateway)
		return
	}

	flusher, _ := w.(http.Flusher)
	wroteHead := false
	for {
		f, closed := pr.pop()
		if f == nil {
			if closed { // connector dropped (or we gave up on this stalled request)
				if !wroteHead {
					http.Error(w, "connector disconnected", http.StatusBadGateway)
				}
				return
			}
			select {
			case <-req.Context().Done():
				_ = l.t.writeFrame(&frame{Type: fCancel, ID: id}) // best effort
				return
			case <-pr.wake:
			}
			continue
		}
		switch f.Type {
		case fHead:
			copyHeader(w.Header(), f.Header)
			status := f.Status
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			wroteHead = true
			if flusher != nil {
				flusher.Flush()
			}
		case fBody:
			if !wroteHead {
				w.WriteHeader(http.StatusOK)
				wroteHead = true
			}
			if len(f.Body) > 0 {
				if _, err := w.Write(f.Body); err != nil {
					_ = l.t.writeFrame(&frame{Type: fCancel, ID: id})
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		case fEnd:
			if !wroteHead {
				w.WriteHeader(http.StatusOK)
			}
			return
		}
	}
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
