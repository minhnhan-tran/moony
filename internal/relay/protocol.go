// Package relay implements a reverse HTTP tunnel so the moony web UI can
// be fronted by a remotely-hosted relay while the agent, session store, and
// AppleConnect auth all stay on the user's machine.
//
// One or more connectors (the user's laptops) dial OUT to the relay and serve
// their local web app's HTTP handler over that connection. Each connector
// announces its host in a hello frame; the relay lists the connected laptops at
// GET /api/laptops and routes /l/{host}/... browser requests to that laptop's
// connector (streaming responses back), so the browser sees and drives the
// LOCAL projects and sessions of whichever laptop it picks. The relay serves the
// SPA shell itself and holds no agent or session state.
//
// Frames are length-prefixed JSON (4-byte big-endian length + body) over one
// connection; the transport is any net.Conn, so the local prototype uses plain
// TCP and a hosted deployment can wrap the same protocol in TLS/WSS later.
package relay

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

type frameType uint8

const (
	fReq     frameType = iota + 1 // relay -> connector: a browser request (Method, Path, Header, Body)
	fHead                         // connector -> relay: response status + headers
	fBody                         // connector -> relay: a response body chunk
	fEnd                          // connector -> relay: response complete
	fCancel                       // relay -> connector: browser disconnected; cancel the request
	fHello                        // connector -> relay: first frame, announces the connector's Host
	fWelcome                      // relay -> connector: reply to fHello, carries the relay's build version
)

// maxFrameBytes guards against a corrupt/hostile length prefix.
const maxFrameBytes = 64 << 20

// frame is one message on the relay<->connector tunnel.
type frame struct {
	Type   frameType   `json:"t"`
	ID     uint64      `json:"id"`
	Method string      `json:"m,omitempty"`
	Path   string      `json:"p,omitempty"`
	Header http.Header `json:"h,omitempty"`
	Status int         `json:"s,omitempty"`
	Body   []byte      `json:"b,omitempty"`
	Host   string      `json:"host,omitempty"` // fHello: the connector's machine name
	User   string      `json:"user,omitempty"` // fHello: the connector owner's AppleConnect username
	Ver    string      `json:"ver,omitempty"`  // fWelcome: the relay's build version; fHello: the connector's build version
}

// tunnel is a length-prefixed JSON frame transport over a single connection.
// Writes are serialized by wmu; readFrame is meant to be called from one
// goroutine.
type tunnel struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

func newTunnel(c net.Conn) *tunnel { return &tunnel{conn: c, r: bufio.NewReader(c)} }

func (t *tunnel) writeFrame(f *frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	t.wmu.Lock()
	defer t.wmu.Unlock()
	if _, err := t.conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err = t.conn.Write(b)
	return err
}

func (t *tunnel) readFrame() (*frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(t.r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameBytes {
		return nil, fmt.Errorf("relay: frame too large (%d bytes)", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(t.r, buf); err != nil {
		return nil, err
	}
	var f frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (t *tunnel) close() error { return t.conn.Close() }
