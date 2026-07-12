package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// memoryHeading is the AGENTS.md section under which memories are stored.
const memoryHeading = "## Memory"

// memoryMaxLen caps a single memory note so a runaway value can't bloat the
// system prompt.
const memoryMaxLen = 1000

// memoryCategory describes a kind of memory and the "### …" subheading it is
// filed under within the "## Memory" section.
type memoryCategory struct {
	key     string // canonical key used by the tool/flags (e.g. "rule")
	heading string // "### Rules" etc.
}

// memoryCategories is the ordered set of supported categories. Order controls
// how subsections appear in the file and in MemoryView. "note" is the default.
var memoryCategories = []memoryCategory{
	{"rule", "### Rules"},
	{"preference", "### Preferences"},
	{"convention", "### Conventions"},
	{"note", "### Notes"},
}

// categoryHeading returns the "### …" subheading for a category key, defaulting
// to the Notes heading for an empty or unknown key.
func categoryHeading(key string) string {
	for _, c := range memoryCategories {
		if c.key == key {
			return c.heading
		}
	}
	return "### Notes"
}

// normalizeCategory maps an arbitrary category string to a known key. When cat
// is empty or unrecognized, it classifies text heuristically.
func normalizeCategory(cat, text string) string {
	cat = strings.ToLower(strings.TrimSpace(cat))
	switch cat {
	case "rule", "rules":
		return "rule"
	case "preference", "preferences", "pref":
		return "preference"
	case "convention", "conventions", "conv":
		return "convention"
	case "note", "notes":
		return "note"
	}
	return classifyMemory(text)
}

// hasAnyPrefix reports whether the lowercased, trimmed text begins with any of
// the given prefixes.
func hasAnyPrefix(text string, prefixes ...string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, p := range prefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// classifyMemory guesses a category from the phrasing of text. It is a coarse
// heuristic used only when the caller doesn't supply an explicit category.
func classifyMemory(text string) string {
	switch {
	case hasAnyPrefix(text, "always ", "never ", "must ", "don't ", "do not ", "ensure ", "make sure "):
		return "rule"
	case hasAnyPrefix(text, "prefer ", "i prefer", "i like", "i'd like", "i want", "please use", "use the"):
		return "preference"
	case hasAnyPrefix(text, "we use", "we always use", "we prefer", "convention", "follow ", "by convention"):
		return "convention"
	default:
		return "note"
	}
}

// autoCapturePrefixes are the directive openings that, on a standalone prompt,
// signal a durable rule/preference/convention worth auto-remembering.
var autoCapturePrefixes = []string{
	"always ", "never ", "prefer ", "don't ", "do not ",
	"we use ", "we always use ", "we prefer ", "make sure ", "ensure ",
}

// detectStandaloneRule reports whether prompt looks like a single directive
// worth auto-capturing, returning the inferred category. It is deliberately
// strict to avoid capturing ordinary task requests or questions.
func detectStandaloneRule(prompt string) (string, bool) {
	t := strings.TrimSpace(prompt)
	if t == "" {
		return "", false
	}
	// Single line only.
	if strings.ContainsAny(t, "\n\r") {
		return "", false
	}
	// Reasonable length: long enough to be a real rule, short enough to be one.
	if len(t) < 8 || len(t) > 240 {
		return "", false
	}
	// Questions are not directives.
	if strings.HasSuffix(t, "?") {
		return "", false
	}
	if !hasAnyPrefix(t, autoCapturePrefixes...) {
		return "", false
	}
	return classifyMemory(t), true
}

// memoryFile returns the AGENTS.md path memories are written to: the global
// user file (<config>/moony/AGENTS.md) when global is true, else the
// project's ./AGENTS.md.
func (a *Agent) memoryFile(global bool) (string, error) {
	if global {
		return config.GlobalConfigPath("AGENTS.md")
	}
	return filepath.Join(a.cfg.WorkingDir, "AGENTS.md"), nil
}

// Remember stores a memory note in the target AGENTS.md (the project file by
// default) under the "## Memory" section, filed beneath the "### …" subheading
// for its category, then rebuilds the system prompt so the note applies for the
// rest of this session. An empty/unknown category is classified from the text.
// It returns a short confirmation.
func (a *Agent) Remember(text, category string, global bool) (string, error) {
	text = strings.TrimSpace(strings.TrimLeft(text, "# "))
	if text == "" {
		return "", fmt.Errorf("nothing to remember")
	}
	if len(text) > memoryMaxLen {
		text = text[:memoryMaxLen]
	}
	cat := normalizeCategory(category, text)
	path, err := a.memoryFile(global)
	if err != nil {
		return "", err
	}
	if err := upsertMemoryBullet(path, categoryHeading(cat), text); err != nil {
		return "", err
	}
	a.rebuildSystemPrompt()

	scope := "project"
	if global {
		scope = "global"
	}
	return fmt.Sprintf("Remembered (%s, %s): %s", scope, cat, text), nil
}

// AutoCaptureRule inspects a raw user prompt and, when it looks like a standalone
// directive (always…/never…/prefer…/we use…), saves it to project memory
// additively — the prompt is still processed normally by the caller. It returns
// a short info message and true when something was captured. Disabled when the
// MOONY_MEMORY_AUTOCAPTURE env var is set to "0".
func (a *Agent) AutoCaptureRule(prompt string) (string, bool) {
	if os.Getenv("MOONY_MEMORY_AUTOCAPTURE") == "0" {
		return "", false
	}
	cat, ok := detectStandaloneRule(prompt)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(prompt)
	// Skip if this exact note is already remembered (project or global).
	if a.memoryContains(text) {
		return "", false
	}
	msg, err := a.Remember(text, cat, false)
	if err != nil {
		return "", false
	}
	return "Auto-" + strings.ToLower(msg[:1]) + msg[1:], true
}

// memoryContains reports whether the given note text is already stored as a
// bullet in either the project or global memory file.
func (a *Agent) memoryContains(text string) bool {
	want := "- " + strings.TrimSpace(text)
	for _, global := range []bool{false, true} {
		path, err := a.memoryFile(global)
		if err != nil {
			continue
		}
		for _, ln := range readMemoryBullets(path) {
			if strings.TrimSpace(ln) == want {
				return true
			}
		}
	}
	return false
}

// rebuildSystemPrompt regenerates the system prompt from the current config and
// tools, re-reading AGENTS.md so freshly saved memories take effect immediately.
// It also re-detects MCP servers (e.g. codebase-memory) that usually register
// AFTER the agent is created (async connect), so their tool-specific prompt
// guidance reflects what's actually available.
func (a *Agent) rebuildSystemPrompt() {
	a.system = buildSystemPrompt(a.cfg, a.tools)
}

// RefreshSystemPrompt rebuilds the system prompt from the current config and the
// agent's current tool set. Call it after tools are added or removed at runtime
// — notably once MCP servers connect and register their tools — so the prompt's
// tool list and any tool-specific guidance (e.g. the codebase-memory hints)
// reflect what's actually available. Safe to call between turns; the running
// turn captured a.system at its start.
func (a *Agent) RefreshSystemPrompt() {
	a.rebuildSystemPrompt()
}

// AttachMemory wires the remember tool's sink to this agent so the model can
// persist memories (which store under the project AGENTS.md and reload the prompt).
func (a *Agent) AttachMemory(sink *tools.MemorySink) {
	sink.SetSave(func(text, category string) (string, error) {
		return a.Remember(text, category, false)
	})
	// Feed the recall tool: flatten every AGENTS.md bullet (project + global)
	// into notes tagged with their section heading.
	sink.SetList(func() []tools.MemoryNote {
		var notes []tools.MemoryNote
		for _, global := range []bool{false, true} {
			scope := "project"
			if global {
				scope = "global"
			}
			for _, s := range a.AgentsSections(global) {
				section := strings.TrimSpace(s.Heading)
				if section == "" {
					section = scope
				} else {
					section = scope + " · " + section
				}
				for _, bullet := range s.Bullets {
					if b := strings.TrimSpace(bullet); b != "" {
						notes = append(notes, tools.MemoryNote{Section: section, Text: b})
					}
				}
			}
		}
		return notes
	})
}

// MemorySection is one AGENTS.md heading and its bullet lines, for the web editor
// (which shows every section, not only "## Memory").
type MemorySection struct {
	Heading string   `json:"heading"`
	Bullets []string `json:"bullets"`
}

// AgentsSections returns every "##"/"###" section, in file order, each with its
// bullets ("- " stripped), from the PROJECT AGENTS.md (global=false) or the
// GLOBAL one shared across all projects (global=true) — the two "places" the web
// memory editor and /memory show side by side.
func (a *Agent) AgentsSections(global bool) []MemorySection {
	path, err := a.memoryFile(global)
	if err != nil {
		return nil
	}
	return readAgentsSections(path)
}

// MemoryBulletCount is the total editable bullets across all AGENTS.md sections,
// project + global — what the composer's memory pill shows.
func (a *Agent) MemoryBulletCount() int {
	n := 0
	for _, global := range []bool{false, true} {
		for _, s := range a.AgentsSections(global) {
			n += len(s.Bullets)
		}
	}
	return n
}

// AddBullet appends a bullet under the named section (creating the section if it
// doesn't exist) of the project (global=false) or global (global=true) AGENTS.md,
// then rebuilds the system prompt so it applies next turn. An empty section
// defaults to "Memory".
func (a *Agent) AddBullet(section, text string, global bool) error {
	text = cleanBullet(text)
	if text == "" {
		return fmt.Errorf("nothing to add")
	}
	if section == "" {
		section = "Memory"
	}
	path, err := a.memoryFile(global)
	if err != nil {
		return err
	}
	if err := upsertSectionBullet(path, section, text); err != nil {
		return err
	}
	a.rebuildSystemPrompt()
	return nil
}

// UpdateBullet replaces the bullet matching oldText (anywhere in the project or
// global AGENTS.md, per global) with newText IN PLACE, then rebuilds the prompt.
// If oldText is gone (e.g. a concurrent edit) it's added to `section` instead, so
// the edit can't be lost.
func (a *Agent) UpdateBullet(section, oldText, newText string, global bool) error {
	newText = cleanBullet(newText)
	if newText == "" {
		return fmt.Errorf("nothing to save")
	}
	path, err := a.memoryFile(global)
	if err != nil {
		return err
	}
	if !replaceBulletInFile(path, oldText, newText) {
		if section == "" {
			section = "Memory"
		}
		if err := upsertSectionBullet(path, section, newText); err != nil {
			return err
		}
	}
	a.rebuildSystemPrompt()
	return nil
}

// DeleteBullet removes the bullet matching text exactly (anywhere in the project
// or global AGENTS.md, per global), then rebuilds the prompt. Idempotent.
func (a *Agent) DeleteBullet(text string, global bool) error {
	path, err := a.memoryFile(global)
	if err != nil {
		return err
	}
	removeBulletInFile(path, text)
	a.rebuildSystemPrompt()
	return nil
}

// ReplaceSection overwrites a section's ENTIRE bullet list in one shot, parsed
// from raw multi-line markdown text (one bullet per non-empty line; a leading
// "- "/"* " is stripped if present, same normalization as a single AddBullet).
// This is the web memory editor's per-section textarea save path — an
// alternative to the single-bullet Add/Update/Delete above, for editing a whole
// section's content at once instead of bullet-by-bullet. Empty text removes the
// section entirely.
func (a *Agent) ReplaceSection(section, text string, global bool) error {
	section = strings.TrimSpace(section)
	if section == "" {
		return fmt.Errorf("section is required")
	}
	path, err := a.memoryFile(global)
	if err != nil {
		return err
	}
	if err := replaceSectionBullets(path, section, parseBulletLines(text)); err != nil {
		return err
	}
	a.rebuildSystemPrompt()
	return nil
}

// parseBulletLines splits raw multi-line markdown into cleaned bullet texts,
// one per non-empty line, reusing cleanBullet's normalization (strip a leading
// "- ", collapse internal whitespace, cap length) for each line.
func parseBulletLines(text string) []string {
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		if b := cleanBullet(ln); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// replaceSectionBullets rewrites the bullet lines under the heading titled
// `section` (## or ###) to exactly `bullets`, creating the section if it's
// missing (and bullets is non-empty) or removing it entirely (heading included)
// when bullets is empty. Mirrors upsertSectionBullet's section-bounds search.
func replaceSectionBullets(path, section string, bullets []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := splitLines(string(data))

	secStart := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if (strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ")) && strings.TrimSpace(strings.TrimLeft(t, "# ")) == section {
			secStart = i
			break
		}
	}

	if secStart == -1 {
		if len(bullets) == 0 {
			return nil // nothing to create, nothing to remove
		}
		block := make([]string, len(bullets))
		for i, b := range bullets {
			block[i] = "- " + b
		}
		out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if out != "" {
			out += "\n\n"
		}
		out += "## " + section + "\n\n" + strings.Join(block, "\n") + "\n"
		return os.WriteFile(path, []byte(out), 0o644)
	}

	secEnd := len(lines)
	for i := secStart + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ") {
			secEnd = i
			break
		}
	}

	var rebuilt []string
	if len(bullets) == 0 {
		rebuilt = append(rebuilt, lines[:secStart]...) // drop the heading too
	} else {
		rebuilt = append(rebuilt, lines[:secStart+1]...)
		rebuilt = append(rebuilt, "")
		for _, b := range bullets {
			rebuilt = append(rebuilt, "- "+b)
		}
	}
	rebuilt = append(rebuilt, lines[secEnd:]...)
	return writeLines(path, rebuilt)
}

// cleanBullet normalizes a bullet's text for storage: strip a leading "# "/"- ",
// collapse any newlines/whitespace runs to single spaces (a markdown bullet is
// one line), and cap the length.
func cleanBullet(text string) string {
	text = strings.TrimLeft(strings.TrimSpace(text), "# ")
	text = strings.TrimPrefix(text, "- ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > memoryMaxLen {
		text = text[:memoryMaxLen]
	}
	return text
}

// readAgentsSections parses every "##"/"###" heading that has bullets from the
// file, in order, returning each section's heading and bullet texts.
func readAgentsSections(path string) []MemorySection {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []MemorySection
	cur := -1
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "### ") || strings.HasPrefix(t, "## ") {
			out = append(out, MemorySection{Heading: strings.TrimSpace(strings.TrimLeft(t, "# "))})
			cur = len(out) - 1
			continue
		}
		if strings.HasPrefix(t, "- ") {
			if cur == -1 {
				out = append(out, MemorySection{Heading: ""})
				cur = 0
			}
			out[cur].Bullets = append(out[cur].Bullets, strings.TrimSpace(strings.TrimPrefix(t, "- ")))
		}
	}
	kept := out[:0]
	for _, s := range out {
		if len(s.Bullets) > 0 {
			kept = append(kept, s)
		}
	}
	return kept
}

// upsertSectionBullet appends "- text" under the heading titled `section` (matched
// against ## or ### headings), creating the file/section if absent. Dedups the
// exact bullet within that section.
func upsertSectionBullet(path, section, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	bullet := "- " + text
	lines := splitLines(string(data))

	secStart := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if (strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ")) && strings.TrimSpace(strings.TrimLeft(t, "# ")) == section {
			secStart = i
			break
		}
	}
	if secStart == -1 {
		out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if out != "" {
			out += "\n\n"
		}
		out += "## " + section + "\n\n" + bullet + "\n"
		return os.WriteFile(path, []byte(out), 0o644)
	}
	secEnd := len(lines)
	for i := secStart + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "### ") {
			secEnd = i
			break
		}
	}
	for i := secStart + 1; i < secEnd; i++ {
		if strings.TrimSpace(lines[i]) == bullet {
			return nil // already present
		}
	}
	insertAt := secEnd
	for insertAt > secStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	lines = insertLines(lines, insertAt, []string{bullet})
	return writeLines(path, lines)
}

// replaceBulletInFile replaces the first "- oldText" line (anywhere) with
// "- newText", preserving its indentation. Reports whether it replaced.
func replaceBulletInFile(path, oldText, newText string) bool {
	want := "- " + strings.TrimSpace(oldText)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := splitLines(string(data))
	for i, ln := range lines {
		if strings.TrimSpace(ln) == want {
			indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
			lines[i] = indent + "- " + newText
			_ = writeLines(path, lines)
			return true
		}
	}
	return false
}

// removeBulletInFile removes the first "- text" line (anywhere). Reports removal.
func removeBulletInFile(path, text string) bool {
	want := "- " + strings.TrimSpace(text)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := splitLines(string(data))
	for i, ln := range lines {
		if strings.TrimSpace(ln) == want {
			lines = append(lines[:i], lines[i+1:]...)
			_ = writeLines(path, lines)
			return true
		}
	}
	return false
}

// memGroup holds the bullets stored under one category subheading.
type memGroup struct {
	heading string
	bullets []string
}

// MemoryView summarizes the memory notes currently in the project and global
// AGENTS.md files, grouped by category, for the /memory command.
func (a *Agent) MemoryView() string {
	home, _ := os.UserHomeDir()
	var b strings.Builder
	for _, s := range []struct {
		label  string
		global bool
	}{{"Project", false}, {"Global", true}} {
		path, err := a.memoryFile(s.global)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s memory — %s\n", s.label, tildePath(path, home))
		groups := readMemoryGrouped(path)
		if len(groups) == 0 {
			b.WriteString("  (none yet)\n")
			continue
		}
		for _, g := range groups {
			if g.heading != "" {
				b.WriteString("  " + g.heading + "\n")
			}
			for _, ln := range g.bullets {
				b.WriteString("    " + ln + "\n")
			}
		}
	}
	b.WriteString("\nAdd a note with /memory <note> or by starting a line with #. " +
		"Categorize with --rule/--preference/--convention/--note, or use " +
		"/memory --global <note> for the cross-project file.")
	return b.String()
}

// ParseMemoryFlags splits a /memory argument into the note text, an explicit
// category key (empty when none given), and whether the global file was
// requested. Recognized flags: --global/-g, --rule/-r, --preference/--pref/-p,
// --convention/--conv/-c, --note/-n. Flags may appear anywhere in the argument.
func ParseMemoryFlags(arg string) (note, category string, global bool) {
	fields := strings.Fields(arg)
	var kept []string
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "--global", "-g":
			global = true
		case "--rule", "-r":
			category = "rule"
		case "--preference", "--pref", "-p":
			category = "preference"
		case "--convention", "--conv", "-c":
			category = "convention"
		case "--note", "-n":
			category = "note"
		default:
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, " "), category, global
}

// upsertMemoryBullet stores "- text" under the given "### …" subheading inside
// the "## Memory" section of the file at path, creating the file, the Memory
// section, and the subheading as needed. If the exact bullet already exists
// anywhere in the Memory section it is left in place (no duplicate is added).
func upsertMemoryBullet(path, heading, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	bullet := "- " + text

	lines := splitLines(string(data))

	// Locate the "## Memory" section boundaries.
	memStart := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == memoryHeading {
			memStart = i
			break
		}
	}
	if memStart == -1 {
		// No Memory section: append one with the subheading and bullet.
		out := strings.Join(lines, "\n")
		out = strings.TrimRight(out, "\n")
		if out != "" {
			out += "\n\n"
		}
		out += memoryHeading + "\n\n" + heading + "\n\n" + bullet + "\n"
		return os.WriteFile(path, []byte(out), 0o644)
	}

	// Find the end of the Memory section (next "## " heading or EOF).
	memEnd := len(lines)
	for i := memStart + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			memEnd = i
			break
		}
	}

	// Dedup: if the exact bullet already exists in the section, do nothing.
	for i := memStart + 1; i < memEnd; i++ {
		if strings.TrimSpace(lines[i]) == bullet {
			return nil
		}
	}

	// Find the requested subheading within the section.
	subStart := -1
	for i := memStart + 1; i < memEnd; i++ {
		if strings.TrimSpace(lines[i]) == heading {
			subStart = i
			break
		}
	}

	if subStart == -1 {
		// Subheading missing: append it (and the bullet) at the end of the
		// Memory section, before the trailing blank lines.
		insertAt := memEnd
		for insertAt > memStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		block := []string{"", heading, "", bullet}
		lines = insertLines(lines, insertAt, block)
		return writeLines(path, lines)
	}

	// Subheading present: find the end of its bullet block (next "### "/"## "
	// or EOF within the section) and insert after the last bullet there.
	subEnd := memEnd
	for i := subStart + 1; i < memEnd; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "### ") {
			subEnd = i
			break
		}
	}
	insertAt := subEnd
	for insertAt > subStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	lines = insertLines(lines, insertAt, []string{bullet})
	return writeLines(path, lines)
}

// appendMemoryBullet stores text under the default Notes subheading. Retained
// for callers/tests that don't care about categories.
func appendMemoryBullet(path, text string) error {
	return upsertMemoryBullet(path, categoryHeading("note"), text)
}

// splitLines splits content into lines, dropping a single trailing newline so
// we don't carry an empty final element.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.TrimRight(content, "\n")
	return strings.Split(content, "\n")
}

// insertLines returns lines with block inserted at index at.
func insertLines(lines []string, at int, block []string) []string {
	if at < 0 {
		at = 0
	}
	if at > len(lines) {
		at = len(lines)
	}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:at]...)
	out = append(out, block...)
	out = append(out, lines[at:]...)
	return out
}

// writeLines writes lines back to path with a single trailing newline.
func writeLines(path string, lines []string) error {
	out := strings.Join(lines, "\n")
	out = strings.TrimRight(out, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

// readMemoryBullets returns the "- …" lines under the "## Memory" section,
// across all category subheadings.
func readMemoryBullets(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	inMem := false
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## ") {
			inMem = t == memoryHeading
			continue
		}
		if inMem && strings.HasPrefix(t, "- ") {
			out = append(out, t)
		}
	}
	return out
}

// readMemoryGrouped returns the bullets under the "## Memory" section grouped by
// their "### …" subheading. Bullets that precede any subheading are grouped
// under an empty heading.
func readMemoryGrouped(path string) []memGroup {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var groups []memGroup
	cur := -1
	inMem := false
	ensure := func(heading string) {
		for i := range groups {
			if groups[i].heading == heading {
				cur = i
				return
			}
		}
		groups = append(groups, memGroup{heading: heading})
		cur = len(groups) - 1
	}
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## ") {
			inMem = t == memoryHeading
			cur = -1
			continue
		}
		if !inMem {
			continue
		}
		if strings.HasPrefix(t, "### ") {
			ensure(t)
			continue
		}
		if strings.HasPrefix(t, "- ") {
			if cur == -1 {
				ensure("")
			}
			groups[cur].bullets = append(groups[cur].bullets, t)
		}
	}
	// Drop empty groups (heading with no bullets).
	var out []memGroup
	for _, g := range groups {
		if len(g.bullets) > 0 {
			out = append(out, g)
		}
	}
	return out
}
