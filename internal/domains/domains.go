// Package domains implements a persisted allow/deny list of web domains that
// gates the web_fetch tool. It lives in one global file:
//
//	~/.config/moony/domains.json   {"allow":[...],"deny":[...]}
//
// Semantics (a deny-list that upgrades to an allowlist "lockdown"):
//
//   - A host matching a deny rule is always blocked.
//   - If the allow list is non-empty, ONLY hosts matching an allow rule may be
//     fetched (everything else is blocked).
//   - If the allow list is empty, any host that isn't denied is permitted.
//
// Rules are bare domains ("apple.com"); a rule matches that host and all of its
// subdomains, so "apple.com" covers "apple.com" and "docs.apple.com".
//
// A fresh install is seeded (see EnsureDefaults) with allow=["apple.com"], so by
// default web_fetch may reach only Apple hosts until the user adds more or
// clears the list.
package domains

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// List is the allow/deny state, mirroring the on-disk JSON.
type List struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`

	malformed bool // the file existed but could not be read/parsed (unexported: not persisted)
}

// defaultAllow seeds a fresh install so web_fetch is restricted to Apple hosts
// out of the box. Because a non-empty allow list activates lockdown mode, the
// practical effect is "Apple-only until you add more hosts or clear the list".
var defaultAllow = []string{"apple.com"}

// Load reads the global domains file. A missing or malformed file yields an
// empty list (best effort), which permits everything. The Apple-only default is
// materialized by EnsureDefaults at startup, not synthesized here, so the file
// stays the single source of truth (clearing every rule re-permits all hosts).
func Load() *List {
	l := &List{}
	path := globalPath()
	if path == "" {
		return l
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			l.malformed = true // exists but unreadable — fail closed rather than permit all
		}
		return l
	}
	if err := json.Unmarshal(data, l); err != nil {
		return &List{malformed: true} // corrupt — don't silently permit every host
	}
	return l
}

// EnsureDefaults writes the default domains file the first time the agent runs
// (when none exists yet) so the Apple-only default is active and is visible and
// editable in the /domains command and the web UI's Domains menu. It never
// overwrites an existing file, so user edits — including clearing the list — are
// always preserved. Best effort: a failure to write just leaves it unconfigured.
func EnsureDefaults() {
	path := globalPath()
	if path == "" {
		return
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return // file exists, or stat failed for some other reason — leave it
	}
	_ = (&List{Allow: append([]string{}, defaultAllow...)}).save()
}

// Check reports whether web_fetch may request host, and a human reason when not.
func (l *List) Check(host string) (allowed bool, reason string) {
	if l.malformed {
		return false, "blocked: domains.json is malformed — fix or remove it (web_fetch is disabled until then)"
	}
	h := normalizeHost(host)
	if h == "" {
		return false, "missing host"
	}
	for _, d := range l.Deny {
		if hostMatches(h, d) {
			return false, "blocked: " + d + " is on the deny list"
		}
	}
	if len(l.Allow) > 0 {
		for _, d := range l.Allow {
			if hostMatches(h, d) {
				return true, ""
			}
		}
		return false, "blocked: " + h + " is not on the allow list (allowlist mode is active)"
	}
	return true, ""
}

// Add records domain in the allow (or deny) list and persists it. Adding to one
// list removes it from the other so they never conflict.
func (l *List) Add(domain string, deny bool) error {
	d := normalizeDomain(domain)
	if d == "" {
		return os.ErrInvalid
	}
	if deny {
		l.Deny = add(l.Deny, d)
		l.Allow = remove(l.Allow, d)
	} else {
		l.Allow = add(l.Allow, d)
		l.Deny = remove(l.Deny, d)
	}
	return l.save()
}

// Remove deletes domain from both lists and persists.
func (l *List) Remove(domain string) error {
	d := normalizeDomain(domain)
	l.Allow = remove(l.Allow, d)
	l.Deny = remove(l.Deny, d)
	return l.save()
}

// Allowed and Denied expose the lists for display.
func (l *List) Allowed() []string { return append([]string{}, l.Allow...) }
func (l *List) Denied() []string  { return append([]string{}, l.Deny...) }

// HostFromURL extracts the lower-case host (no port) from a raw URL, for Check.
func HostFromURL(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		return normalizeHost(u.Hostname())
	}
	return ""
}

func (l *List) save() error {
	path := globalPath()
	if path == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func globalPath() string {
	p, err := config.GlobalConfigPath("domains.json")
	if err != nil {
		return ""
	}
	return p
}

// normalizeHost lower-cases a host and strips any port.
func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// normalizeDomain reduces user input ("https://Docs.Apple.com/x", "*.apple.com:443")
// to a bare lower-case domain ("docs.apple.com", "apple.com").
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndexByte(d, '@'); i >= 0 {
		d = d[i+1:] // strip any userinfo
	}
	d = strings.TrimPrefix(d, "*.")
	d = strings.Trim(d, ".")
	if i := strings.LastIndexByte(d, ':'); i >= 0 {
		d = d[:i] // strip port
	}
	return d
}

// hostMatches reports whether host equals rule or is a subdomain of it.
func hostMatches(host, rule string) bool {
	return host == rule || strings.HasSuffix(host, "."+rule)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func add(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func remove(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
