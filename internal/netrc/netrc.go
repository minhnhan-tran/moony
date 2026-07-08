// Package netrc reads ~/.netrc to supply per-host credentials, primarily so
// web_fetch / batch_web_fetch can authenticate to internal endpoints (e.g.
// Artifactory) without the model having to handle secrets.
//
// Only the machine/login/password grammar is interpreted. The token in the
// password field is sent as an opaque Bearer credential (mirroring how
// Artifactory identity tokens are used), so HTTP Basic auth is intentionally
// NOT synthesized from login:password. "account" is consumed but unused, and
// "default" / "macdef" stanzas are deliberately ignored so a credential is
// never sent to an unintended host.
package netrc

import (
	"os"
	"path/filepath"
	"strings"
)

// Entry is one machine stanza from a netrc file.
type Entry struct {
	Machine  string
	Login    string
	Password string
}

// File is a parsed netrc keyed by lower-cased machine host.
type File map[string]Entry

// Path returns the netrc location: $NETRC when set, otherwise ~/.netrc. It is
// empty when no home directory can be resolved and $NETRC is unset.
func Path() string {
	if p := strings.TrimSpace(os.Getenv("NETRC")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".netrc")
}

// Load reads and parses the netrc at Path(). A missing file yields an empty
// File and no error; only genuine read failures return an error.
func Load() (File, error) {
	path := Path()
	if path == "" {
		return File{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return nil, err
	}
	return Parse(string(data)), nil
}

// Parse tokenizes whitespace-delimited netrc content into a File. Recognized
// keywords: machine, login, password (account is skipped; default and macdef
// are ignored). Unknown tokens are skipped, which also discards macdef bodies
// in the common case.
func Parse(content string) File {
	out := File{}
	toks := strings.Fields(content)
	var cur *Entry
	flush := func() {
		if cur != nil && cur.Machine != "" {
			out[strings.ToLower(cur.Machine)] = *cur
		}
		cur = nil
	}
	for i := 0; i < len(toks); i++ {
		switch strings.ToLower(toks[i]) {
		case "machine":
			flush()
			if i+1 < len(toks) {
				cur = &Entry{Machine: toks[i+1]}
				i++
			}
		case "default":
			// A default stanza applies to every host; ignored on purpose so a
			// token is never sent to an unintended server.
			flush()
		case "login":
			if cur != nil && i+1 < len(toks) {
				cur.Login = toks[i+1]
				i++
			}
		case "password":
			if cur != nil && i+1 < len(toks) {
				cur.Password = toks[i+1]
				i++
			}
		case "account", "macdef":
			if i+1 < len(toks) {
				i++ // consume the following token (value / macro name), unused
			}
		}
	}
	flush()
	return out
}

// Bearer returns the Authorization header value ("Bearer <token>") for host,
// taken from the password field of the exactly-matching (case-insensitive)
// machine entry. It returns "" when there is no entry or it has no password.
func (f File) Bearer(host string) string {
	if f == nil {
		return ""
	}
	e, ok := f[strings.ToLower(strings.TrimSpace(host))]
	if !ok || e.Password == "" {
		return ""
	}
	return "Bearer " + e.Password
}
