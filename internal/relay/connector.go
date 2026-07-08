package relay

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

var errTunnelClosed = errors.New("relay: tunnel closed")

// ServeConnector announces this connector's host, owner, and build version to
// the relay, then reads browser requests off the tunnel and serves each one
// against h (the local web app's handler), streaming the response back frame by
// frame. owner is the username of whoever launched the connector;
// the relay uses it to show each web user only their own connectors. token, when
// non-empty, is the per-user connector credential (MOONY_RELAY_TOKEN): a relay
// with auth enabled verifies it and derives the trusted owner from it, ignoring
// owner; a relay without auth ignores token and trusts owner. version is
// this terminal's build version, surfaced in GET /api/laptops so the web UI can
// flag (and live-clear) an out-of-date terminal from the laptop poll. onRelayVersion,
// when non-nil, is called with the relay's build version once it arrives (an
// fWelcome frame), so the caller can warn if this terminal is behind; it may
// never fire against an older relay. It blocks until the connection drops and
// returns the read error so the caller can reconnect.
func ServeConnector(conn net.Conn, host, owner, token, version string, h http.Handler, onRelayVersion func(string)) error {
	t := newTunnel(conn)
	// The hello frame must be first: the relay registers this connector by
	// (owner, host) before serving any browser request to it.
	if err := t.writeFrame(&frame{Type: fHello, Host: host, User: owner, Token: token, Ver: version}); err != nil {
		return err
	}
	var mu sync.Mutex
	cancels := map[uint64]context.CancelFunc{}

	for {
		f, err := t.readFrame()
		if err != nil {
			mu.Lock()
			for _, c := range cancels {
				c()
			}
			mu.Unlock()
			return err
		}
		switch f.Type {
		case fWelcome:
			// The relay's version; let the caller (the TUI) compare + warn.
			if onRelayVersion != nil {
				onRelayVersion(f.Ver)
			}
		case fReq:
			ctx, cancel := context.WithCancel(context.Background())
			mu.Lock()
			cancels[f.ID] = cancel
			mu.Unlock()
			go func(f *frame) {
				defer func() {
					mu.Lock()
					delete(cancels, f.ID)
					mu.Unlock()
					cancel()
				}()
				rw := &tunnelRW{t: t, id: f.ID, header: http.Header{}}
				req, err := http.NewRequestWithContext(ctx, f.Method, f.Path, bytes.NewReader(f.Body))
				if err != nil {
					rw.WriteHeader(http.StatusBadRequest)
					rw.finish()
					return
				}
				if f.Header != nil {
					req.Header = f.Header
				}
				h.ServeHTTP(rw, req)
				rw.finish()
			}(f)
		case fCancel:
			mu.Lock()
			if c := cancels[f.ID]; c != nil {
				c()
			}
			mu.Unlock()
		}
	}
}

// tunnelRW is an http.ResponseWriter that forwards the handler's status,
// headers, and body chunks to the relay as frames. Each Write emits a body frame
// immediately, so streaming responses (SSE) flow through without buffering;
// Flush is therefore a no-op.
type tunnelRW struct {
	t      *tunnel
	id     uint64
	header http.Header

	mu        sync.Mutex
	wroteHead bool
	failed    bool
}

func (rw *tunnelRW) Header() http.Header { return rw.header }

func (rw *tunnelRW) writeHeadLocked(status int) {
	if rw.wroteHead || rw.failed {
		return
	}
	rw.wroteHead = true
	if err := rw.t.writeFrame(&frame{Type: fHead, ID: rw.id, Status: status, Header: rw.header.Clone()}); err != nil {
		rw.failed = true
	}
}

func (rw *tunnelRW) WriteHeader(status int) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	rw.writeHeadLocked(status)
}

func (rw *tunnelRW) Write(b []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if !rw.wroteHead {
		rw.writeHeadLocked(http.StatusOK)
	}
	if rw.failed {
		return 0, errTunnelClosed
	}
	cp := append([]byte(nil), b...) // the caller may reuse b
	if err := rw.t.writeFrame(&frame{Type: fBody, ID: rw.id, Body: cp}); err != nil {
		rw.failed = true
		return 0, err
	}
	return len(b), nil
}

// Flush satisfies http.Flusher (so the SSE handler streams); each Write already
// sends a frame, so there's nothing extra to do.
func (rw *tunnelRW) Flush() {}

func (rw *tunnelRW) finish() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.failed {
		return
	}
	if !rw.wroteHead {
		rw.writeHeadLocked(http.StatusOK)
	}
	_ = rw.t.writeFrame(&frame{Type: fEnd, ID: rw.id})
}
