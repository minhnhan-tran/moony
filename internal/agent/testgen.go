package agent

// Auto-test generation (opt-in via MN_AUTO_TESTGEN). At the end of a turn that
// added or edited source files, draft or update the sibling unit-test file with
// the aux (self-review) model and surface it as a REVIEWABLE SUGGESTION. It is
// suggest-only: nothing is ever written to the working tree, so it can't fight
// the user's edits or clutter commits. Bounded by MN_AUTO_TESTGEN_MAX.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

const (
	// defaultTestgenMax caps drafted files per turn when MN_AUTO_TESTGEN_MAX is unset.
	defaultTestgenMax = 2
	// maxTestgenSrcBytes bounds how much of a source/test file is sent to the model.
	maxTestgenSrcBytes = 24000
	// testgenTimeout bounds a single draft call.
	testgenTimeout = 90 * time.Second
)

// testTarget is a source file that changed this turn and its sibling test.
type testTarget struct {
	srcAbs, srcRel   string
	testAbs, testRel string
	testExists       bool
	lang             string // fenced-code language hint for the prompt/suggestion
}

// maybeSuggestTests drafts tests for this turn's changed source files when
// MN_AUTO_TESTGEN is enabled, emitting each as a styled suggestion. Suggest-only:
// nothing is written to disk. No-op when disabled, nothing eligible changed, or
// no LLM client is available.
func (a *Agent) maybeSuggestTests(ctx context.Context, emit func(Event)) {
	if a == nil || a.cfg == nil || !a.cfg.AutoTestGen || a.client == nil {
		return
	}
	if a.snapshots == nil || !a.snapshots.mutatedThisTurn() {
		return
	}
	targets := eligibleTestTargets(a.snapshots.currentTurnChanges(), a.cfg.WorkingDir, a.cfg.AutoTestGenMax)
	if len(targets) == 0 {
		return
	}
	model, prov, _ := config.AuxModelRoute(a.cfg.Model, a.cfg.LLMProvider)
	if prov == "" {
		prov = a.cfg.LLMProvider
	}
	level := config.NormalizeReasoningFor(model, config.AuxReasoning())
	emit(Info{Text: fmt.Sprintf(
		"Auto-testgen: drafting tests for %d changed file(s) via **%s** via `%s` (reasoning=**%s**) — suggestions only, nothing is written.",
		len(targets), shortModelName(model), prov, level)})

	for _, tgt := range targets {
		select {
		case <-ctx.Done():
			return
		default:
		}
		draft, err := a.draftOneTest(ctx, tgt)
		if err != nil || strings.TrimSpace(draft) == "" {
			continue
		}
		emit(Info{Text: renderTestSuggestion(tgt, draft)})
	}
}

// eligibleTestTargets selects source files changed this turn that warrant a
// drafted test: added/edited (not deleted) source files in a supported language
// that are not themselves tests and whose sibling test was NOT also changed this
// turn (the user is presumably already handling it). Results are deduped and
// capped at max (defaultTestgenMax when max <= 0).
func eligibleTestTargets(changes []fileSnapshot, workingDir string, max int) []testTarget {
	if max <= 0 {
		max = defaultTestgenMax
	}
	// Set of all paths touched this turn, to skip files whose sibling test the
	// turn already modified.
	touched := make(map[string]bool, len(changes))
	for _, c := range changes {
		touched[filepath.Clean(c.path)] = true
	}

	var out []testTarget
	seen := map[string]bool{}
	for _, c := range changes {
		if c.missing { // deletion
			continue
		}
		src := filepath.Clean(c.path)
		lang, testPath, ok := siblingTestPath(src)
		if !ok {
			continue
		}
		if touched[filepath.Clean(testPath)] {
			continue // its test was already edited this turn
		}
		if seen[src] {
			continue
		}
		seen[src] = true
		out = append(out, testTarget{
			srcAbs:     src,
			srcRel:     relOrBase(workingDir, src),
			testAbs:    testPath,
			testRel:    relOrBase(workingDir, testPath),
			testExists: fileExists(testPath),
			lang:       lang,
		})
		if len(out) >= max {
			break
		}
	}
	return out
}

// siblingTestPath returns the fenced-code language hint and the conventional
// sibling test path for a source file, plus whether the file is an eligible,
// non-test source file in a supported language.
func siblingTestPath(src string) (lang, testPath string, ok bool) {
	if isExcludedPath(src) {
		return "", "", false
	}
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	switch ext {
	case ".go":
		if strings.HasSuffix(base, "_test.go") {
			return "", "", false
		}
		return "go", filepath.Join(dir, name+"_test.go"), true
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
			return "", "", false
		}
		lang = strings.TrimPrefix(ext, ".")
		return lang, filepath.Join(dir, name+".test"+ext), true
	case ".py":
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
			return "", "", false
		}
		return "python", filepath.Join(dir, "test_"+base), true
	default:
		return "", "", false
	}
}

// isExcludedPath skips generated/vendored/dependency trees that shouldn't get tests.
func isExcludedPath(p string) bool {
	excluded := map[string]bool{
		"vendor": true, "node_modules": true, "testdata": true,
		".git": true, "dist": true, "build": true,
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if excluded[seg] {
			return true
		}
	}
	return false
}

// draftOneTest asks the aux model for the complete sibling test file. It returns
// the drafted contents (never writes to disk).
func (a *Agent) draftOneTest(ctx context.Context, tgt testTarget) (string, error) {
	srcBytes, err := os.ReadFile(tgt.srcAbs)
	if err != nil || len(strings.TrimSpace(string(srcBytes))) == 0 {
		return "", err
	}
	src := clip(string(srcBytes), maxTestgenSrcBytes)

	var existing string
	if tgt.testExists {
		if b, err := os.ReadFile(tgt.testAbs); err == nil {
			existing = clip(string(b), maxTestgenSrcBytes)
		}
	}

	sys := "You are a precise test-writing assistant. Given a source file, output the COMPLETE " +
		"contents of its unit-test file and NOTHING else — no explanation, wrapped in a single " +
		"fenced code block. Cover the main exported behavior and important edge cases, and match " +
		"the project's existing test style, framework, and package/import conventions. If an " +
		"existing test file is provided, UPDATE it (keep the passing tests, add missing coverage) " +
		"and output the full new file."

	var b strings.Builder
	fmt.Fprintf(&b, "Source file `%s`:\n\n```%s\n%s\n```\n", tgt.srcRel, tgt.lang, src)
	if existing != "" {
		fmt.Fprintf(&b, "\nExisting test file `%s` to update:\n\n```%s\n%s\n```\n", tgt.testRel, tgt.lang, existing)
	}
	fmt.Fprintf(&b, "\nWrite the test file `%s`.", tgt.testRel)

	rctx, cancel := context.WithTimeout(ctx, testgenTimeout)
	defer cancel()
	resp, err := a.streamAux(rctx, sys, []llm.Message{llm.UserText(b.String())})
	if err != nil || resp == nil {
		return "", err
	}
	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// renderTestSuggestion formats a drafted test as a styled, reviewable Info
// suggestion. The draft is shown as-is when the model already fenced it.
func renderTestSuggestion(tgt testTarget, draft string) string {
	verb := "Create"
	if tgt.testExists {
		verb = "Update"
	}
	body := strings.TrimSpace(draft)
	if !strings.HasPrefix(body, "```") {
		body = fmt.Sprintf("```%s\n%s\n```", tgt.lang, body)
	}
	return fmt.Sprintf(
		"**Auto-testgen suggestion** — %s `%s` (covers changes in `%s`). Review and apply if useful; nothing was written to your tree.\n\n%s",
		verb, tgt.testRel, tgt.srcRel, body)
}

func relOrBase(root, p string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(p)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
