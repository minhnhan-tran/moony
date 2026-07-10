package lsp

// nav.go adds symbol-navigation requests (definition / references / hover) on
// top of the diagnostics-only plumbing in lsp.go and manager.go, for the
// lsp_definition/lsp_references/lsp_hover agent tools (internal/tools/lsp.go).

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Range is a 0-indexed LSP text range.
type Range struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

// Location is a simplified LSP location: an absolute file path plus a
// 0-indexed range, returned by Definition and References.
type Location struct {
	Path  string
	Range Range
}

// Definition sends textDocument/definition for a 0-indexed position.
func (s *Server) Definition(ctx context.Context, path string, line, char int) ([]Location, error) {
	raw, err := s.positionRequest(ctx, "textDocument/definition", path, line, char, nil)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// References sends textDocument/references for a 0-indexed position.
func (s *Server) References(ctx context.Context, path string, line, char int, includeDeclaration bool) ([]Location, error) {
	extra := map[string]any{"context": map[string]any{"includeDeclaration": includeDeclaration}}
	raw, err := s.positionRequest(ctx, "textDocument/references", path, line, char, extra)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// Hover sends textDocument/hover for a 0-indexed position, returning "" if
// the server has nothing to say.
func (s *Server) Hover(ctx context.Context, path string, line, char int) (string, error) {
	raw, err := s.positionRequest(ctx, "textDocument/hover", path, line, char, nil)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var env struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil
	}
	return hoverText(env.Contents), nil
}

// positionRequest builds and sends a standard {textDocument, position} LSP
// request, merging in any extra top-level fields (e.g. references' context).
func (s *Server) positionRequest(ctx context.Context, method, path string, line, char int, extra map[string]any) (json.RawMessage, error) {
	body := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     map[string]any{"line": line, "character": char},
	}
	for k, v := range extra {
		body[k] = v
	}
	params, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return s.call(ctx, method, params)
}

// parseLocations accepts the shapes a definition/references response can
// take: null, a single Location, or a Location[]. LocationLink isn't handled
// since we don't advertise linkSupport, so compliant servers won't send it.
func parseLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type rawLoc struct {
		URI   string `json:"uri"`
		Range Range  `json:"range"`
	}
	var list []rawLoc
	if err := json.Unmarshal(raw, &list); err == nil {
		out := make([]Location, 0, len(list))
		for _, l := range list {
			out = append(out, Location{Path: uriToPath(l.URI), Range: l.Range})
		}
		return out
	}
	var single rawLoc
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{{Path: uriToPath(single.URI), Range: single.Range}}
	}
	return nil
}

// hoverText extracts plain text from a textDocument/hover `contents` value,
// which may be a bare string, {language,value}/{kind,value}, or an array of
// those (MarkedString | MarkedString[] | MarkupContent).
func hoverText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Value != "" {
		return obj.Value
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		parts := make([]string, 0, len(arr))
		for _, item := range arr {
			if t := hoverText(item); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// uriToPath reverses pathToURI. On Windows, a drive-letter path's first
// segment contains a colon, so url.URL.String() escapes it with a "./"
// prefix to avoid looking like a URI scheme; url.Parse then reads that back
// as Host="." and Path="/C:/foo" rather than folding the drive into Path.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}
	p := u.Path
	switch {
	case u.Host != "" && u.Host != ".":
		// e.g. a hand-written "file://C:/foo" (Host="C:", Path="/foo").
		p = u.Host + p
	case isWindowsDriveRoot(p):
		// The "./"-escaped form above: "/C:/foo" -> "C:/foo".
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// isWindowsDriveRoot reports whether p is "/<letter>:/..." or "/<letter>:\...":
// a leading slash immediately followed by a drive letter and colon.
func isWindowsDriveRoot(p string) bool {
	if len(p) < 4 || p[0] != '/' || p[2] != ':' || (p[3] != '/' && p[3] != '\\') {
		return false
	}
	c := p[1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// Definition looks up the definition of the symbol at a 1-indexed
// line/column in path, opening the file with its current on-disk content
// first so the server sees up-to-date text. Returns the server name that
// answered ("" if no server handles this extension, in which case locs and
// err are both nil).
func (m *Manager) Definition(ctx context.Context, path string, line, col int) (locs []Location, server string, err error) {
	srv, err := m.prepare(path)
	if err != nil || srv == nil {
		return nil, "", err
	}
	locs, err = srv.Definition(ctx, path, line-1, col-1)
	return locs, srv.name, err
}

// References looks up references to the symbol at a 1-indexed line/column.
func (m *Manager) References(ctx context.Context, path string, line, col int, includeDeclaration bool) (locs []Location, server string, err error) {
	srv, err := m.prepare(path)
	if err != nil || srv == nil {
		return nil, "", err
	}
	locs, err = srv.References(ctx, path, line-1, col-1, includeDeclaration)
	return locs, srv.name, err
}

// Hover looks up hover info (signature/doc) at a 1-indexed line/column.
func (m *Manager) Hover(ctx context.Context, path string, line, col int) (text, server string, err error) {
	srv, err := m.prepare(path)
	if err != nil || srv == nil {
		return "", "", err
	}
	text, err = srv.Hover(ctx, path, line-1, col-1)
	return text, srv.name, err
}

// prepare finds the running server that handles path's extension (nil, nil
// if none do) and syncs it with path's current on-disk content so
// position-based queries see fresh text.
func (m *Manager) prepare(path string) (*Server, error) {
	if m.Empty() {
		return nil, nil
	}
	ext := filepath.Ext(path)
	for _, srv := range m.servers {
		if !srv.HandlesExtension(ext) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := srv.NotifyChange(path, string(data)); err != nil {
			return nil, err
		}
		return srv, nil
	}
	return nil, nil
}
