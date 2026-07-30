package web

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// metrics holds lightweight, dependency-free server counters exposed at /metrics
// in Prometheus text-exposition format. We deliberately avoid a Prometheus client
// library — atomic counters + a hand-rolled encoder keep the binary lean and the
// relay's single-pod footprint small, which matches how the rest of the app does
// observability (stdlib only).
type metrics struct {
	startedAt time.Time

	requestsTotal   sync.Map // routeStatus{route,status} -> *int64
	inFlight        int64
	sseActive       int64    // currently-open event streams (subscriber gauge)
	sseTotal        int64    // event streams opened over the process lifetime
	replayServed    int64    // capped-replay connects
	historyServed   int64    // /history page fetches
	requestDuration sync.Map // route -> *durationAgg
}

type routeStatus struct {
	route  string
	status int
}

// durationAgg is a minimal running aggregate (count + summed seconds) per route,
// enough to derive an average latency without a full histogram dependency.
type durationAgg struct {
	count   int64
	sumNano int64
}

func newMetrics() *metrics { return &metrics{startedAt: time.Now()} }

func (m *metrics) observe(route string, status int, dur time.Duration) {
	ks := routeStatus{route: route, status: status}
	if v, ok := m.requestsTotal.Load(ks); ok {
		atomic.AddInt64(v.(*int64), 1)
	} else {
		var n int64 = 1
		actual, loaded := m.requestsTotal.LoadOrStore(ks, &n)
		if loaded {
			atomic.AddInt64(actual.(*int64), 1)
		}
	}
	agg, _ := m.requestDuration.LoadOrStore(route, &durationAgg{})
	a := agg.(*durationAgg)
	atomic.AddInt64(&a.count, 1)
	atomic.AddInt64(&a.sumNano, dur.Nanoseconds())
}

func (m *metrics) sseOpen()  { atomic.AddInt64(&m.sseActive, 1); atomic.AddInt64(&m.sseTotal, 1) }
func (m *metrics) sseClose() { atomic.AddInt64(&m.sseActive, -1) }

// statusRecorder captures the response status (and byte count) for logging and
// metrics without altering the response. It transparently passes Flush through
// so SSE streaming still works.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer so long-lived SSE responses keep
// streaming (net/http's ResponseWriter implements http.Flusher).
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLogEnabled reports whether per-request access logging is on. It's opt-in
// (MN_ACCESS_LOG=1/true/on) so the default output stays quiet.
func accessLogEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_ACCESS_LOG"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// observe wraps a handler with request metrics + optional structured access
// logging. It normalizes the path into a low-cardinality route label so per-ID
// paths (/api/sessions/<id>/events) don't explode the metric space.
func (m *metrics) middleware(next http.Handler, logAccess bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.inFlight, 1)
		defer atomic.AddInt64(&m.inFlight, -1)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		route := routeLabel(r.Method, r.URL.Path)
		m.observe(route, rec.status, dur)

		if logAccess {
			log.Printf("access method=%s path=%s route=%s status=%d bytes=%d dur=%s",
				r.Method, r.URL.Path, route, rec.status, rec.bytes, dur.Round(time.Millisecond))
		}
	})
}

// routeLabel maps a concrete request path to a stable, low-cardinality label.
// Session IDs (and similar opaque segments) are collapsed to :id so the metric
// series count stays bounded regardless of how many sessions exist.
func routeLabel(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if looksLikeID(s) {
			segs[i] = ":id"
		}
	}
	p := "/" + strings.Join(segs, "/")
	if p == "/" {
		p = "/"
	}
	return method + " " + p
}

// looksLikeID heuristically detects opaque path segments (session ids, hashes)
// so they collapse into a single metric label.
func looksLikeID(s string) bool {
	if len(s) < 8 {
		return false
	}
	hexish := 0
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-' {
			hexish++
		}
	}
	return hexish == len(s)
}

// handleHealthz is an unauthenticated liveness/readiness probe: 200 with a small
// JSON payload carrying the version and uptime. Safe to expose to k8s probes.
func (m *metrics) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":%q,"uptimeSeconds":%d}`+"\n",
		config.Version, int64(time.Since(m.startedAt).Seconds()))
}

// handleMetrics renders the counters in Prometheus text-exposition format. It's
// hand-rolled (no client_golang dep) and intentionally small.
func (m *metrics) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder

	writeGauge := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
	}
	writeCounter := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}

	writeGauge("moony_web_uptime_seconds", "Server uptime in seconds.", int64(time.Since(m.startedAt).Seconds()))
	writeGauge("moony_web_requests_in_flight", "In-flight HTTP requests.", atomic.LoadInt64(&m.inFlight))
	writeGauge("moony_web_sse_streams_active", "Currently open SSE event streams.", atomic.LoadInt64(&m.sseActive))
	writeCounter("moony_web_sse_streams_total", "SSE event streams opened over the process lifetime.", atomic.LoadInt64(&m.sseTotal))
	writeCounter("moony_web_replay_served_total", "Capped-replay event-stream connects.", atomic.LoadInt64(&m.replayServed))
	writeCounter("moony_web_history_served_total", "History (older-turn) page fetches.", atomic.LoadInt64(&m.historyServed))

	// Per-route request totals (labelled by method+path route and status).
	type rc struct {
		ks routeStatus
		n  int64
	}
	var rows []rc
	m.requestsTotal.Range(func(k, v any) bool {
		rows = append(rows, rc{ks: k.(routeStatus), n: atomic.LoadInt64(v.(*int64))})
		return true
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ks.route != rows[j].ks.route {
			return rows[i].ks.route < rows[j].ks.route
		}
		return rows[i].ks.status < rows[j].ks.status
	})
	b.WriteString("# HELP moony_web_requests_total Total HTTP requests by route and status.\n# TYPE moony_web_requests_total counter\n")
	for _, row := range rows {
		method, path := splitRoute(row.ks.route)
		fmt.Fprintf(&b, "moony_web_requests_total{method=%q,path=%q,status=%q} %d\n",
			method, path, strconv.Itoa(row.ks.status), row.n)
	}

	// Per-route average request duration (seconds).
	type da struct {
		route string
		count int64
		sum   int64
	}
	var durs []da
	m.requestDuration.Range(func(k, v any) bool {
		a := v.(*durationAgg)
		durs = append(durs, da{route: k.(string), count: atomic.LoadInt64(&a.count), sum: atomic.LoadInt64(&a.sumNano)})
		return true
	})
	sort.Slice(durs, func(i, j int) bool { return durs[i].route < durs[j].route })
	b.WriteString("# HELP moony_web_request_duration_seconds_avg Average request duration by route.\n# TYPE moony_web_request_duration_seconds_avg gauge\n")
	for _, d := range durs {
		if d.count == 0 {
			continue
		}
		method, path := splitRoute(d.route)
		avg := float64(d.sum) / float64(d.count) / 1e9
		fmt.Fprintf(&b, "moony_web_request_duration_seconds_avg{method=%q,path=%q} %g\n", method, path, avg)
	}

	io.WriteString(w, b.String())
}

func splitRoute(route string) (method, path string) {
	if i := strings.IndexByte(route, ' '); i >= 0 {
		return route[:i], route[i+1:]
	}
	return "", route
}
