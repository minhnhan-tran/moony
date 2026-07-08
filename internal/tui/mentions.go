package tui

// File mentions: typing "@path" in a message attaches that file's contents to
// the prompt before it is sent. This is a UX shortcut for "look at this file"
// without having to ask the agent to read_file first.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pysty1995/moony/internal/attach"
)

// mentionPattern matches an @-prefixed file path. A mention starts on a word
// boundary and consumes everything until whitespace, a colon, or a comma — so
// "@cmd/main.go" works but "@everyone" or an email doesn't accidentally match.
var mentionPattern = regexp.MustCompile(`(^|[\s(\[])@([^\s,:()\[\]]+)`)

// mentionMaxBytes caps how much of any one mentioned file we attach.
const mentionMaxBytes = 32 * 1024

// expandMentions returns text annotated with the contents of any @file mentions
// it contains. The original text appears at the top, followed by the contents
// of each unique resolved file. Mentions that don't resolve to a regular file
// (or are too big) are left as-is in the text and skipped silently.
func expandMentions(text, workingDir string) string {
	matches := mentionPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text
	}
	seen := map[string]bool{}
	type attachment struct {
		ref      string
		path     string
		body     string
		truncate bool
	}
	var atts []attachment
	for _, m := range matches {
		ref := m[2]
		if seen[ref] {
			continue
		}
		seen[ref] = true

		// Resolve relative to the working directory.
		full := ref
		if !filepath.IsAbs(full) {
			full = filepath.Join(workingDir, ref)
		}
		// Images, PDFs, and Office files are sent as attachments (see
		// attachmentMentions), not inlined as text here.
		if attach.Attachable(full) {
			continue
		}
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			continue
		}
		// Skip very large files outright; the user can read_file with paging.
		if fi.Size() > 4*mentionMaxBytes {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		truncate := false
		if len(data) > mentionMaxBytes {
			data = data[:mentionMaxBytes]
			truncate = true
		}
		atts = append(atts, attachment{ref: ref, path: full, body: string(data), truncate: truncate})
	}
	if len(atts) == 0 {
		return text
	}

	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\n--- attached files (from @-mentions) ---\n")
	for _, a := range atts {
		fmt.Fprintf(&b, "\n@%s (path: %s):\n```\n%s", a.ref, a.path, a.body)
		if !strings.HasSuffix(a.body, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n")
		if a.truncate {
			b.WriteString("(truncated)\n")
		}
	}
	return b.String()
}

// attachmentMentions resolves @-mentions that point to attachable files
// (images, PDFs, Office documents) into Attachments. Non-attachable mentions
// (plain text/code files) are left for expandMentions to inline. Per-file
// failures (too large, unreadable) are returned as human-readable notes.
func attachmentMentions(text, workingDir string) ([]attach.Attachment, []string) {
	matches := mentionPattern.FindAllStringSubmatch(text, -1)
	var atts []attach.Attachment
	var notes []string
	seen := map[string]bool{}
	for _, m := range matches {
		ref := m[2]
		if seen[ref] {
			continue
		}
		full := ref
		if !filepath.IsAbs(full) {
			full = filepath.Join(workingDir, ref)
		}
		if !attach.Attachable(full) {
			continue
		}
		seen[ref] = true
		a, err := attach.FromPath(full)
		if err != nil {
			notes = append(notes, "could not attach @"+ref+": "+err.Error())
			continue
		}
		atts = append(atts, a)
	}
	return atts, notes
}
