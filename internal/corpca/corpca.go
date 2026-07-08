// Package corpca embeds Apple's internal corporate CA certificates (root and
// intermediate) so the binary can validate TLS on internal *.apple.com hosts
// (e.g. github.pie.apple.com, artifacts.apple.com) even on machines that don't
// already have those CAs installed in the OS trust store — plain Linux boxes,
// Docker/CI containers, or a Mac that isn't MDM-enrolled. Ported/adapted from
// apple-pi.
//
// The bundle is appended to (never replaces) the system trust pool, so public
// TLS verification is unaffected; it only adds trust for the extra Apple
// corporate CAs.
package corpca

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net/http"
	"sync"
)

//go:embed apple_corporate_ca.pem
var bundle []byte

var (
	poolOnce sync.Once
	pool     *x509.CertPool
)

// Pool returns the system trust pool with Apple's corporate CAs appended.
// Falls back to a fresh pool if the system pool can't be loaded (e.g. no
// system store available, as in some minimal Linux images).
func Pool() *x509.CertPool {
	poolOnce.Do(func() {
		p, err := x509.SystemCertPool()
		if err != nil || p == nil {
			p = x509.NewCertPool()
		}
		p.AppendCertsFromPEM(bundle)
		pool = p
	})
	return pool
}

// InstallDefaultTransport patches http.DefaultTransport so any http.Client
// left at its zero value (Transport: nil) trusts Apple's corporate CAs. It
// clones the standard library's default transport settings and only swaps in
// the augmented RootCAs, so proxy/dialer/timeout behavior is unchanged. Call
// this once, early in main(), before any HTTP clients are used.
func InstallDefaultTransport() {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	t := base.Clone()
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	t.TLSClientConfig.RootCAs = Pool()
	http.DefaultTransport = t
}
