// commands_user.go — user-defined slash commands plus token/quota/session summaries.

package web

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

func tokensSummary(sess *session, cfg *config.Config) string {
	in, out, lastIn, spend, budget, quotaKnown := sess.usageSnapshot()
	cacheRead, cacheCreate := sess.cacheSnapshot()
	msg := fmt.Sprintf("Tokens this session — in: %d, out: %d, total: %d", in, out, in+out)
	// Prompt-cache split: how much of the input was read from cache (billed cheap
	// when the route discounts it) vs wrote new cache entries. Zero cached reads
	// means caching isn't engaging on this route.
	if in > 0 {
		if cacheRead > 0 || cacheCreate > 0 {
			msg += fmt.Sprintf("\nPrompt cache — %d read from cache (%d%% of input), %d written", cacheRead, 100*cacheRead/in, cacheCreate)
		} else {
			msg += "\nPrompt cache — 0 cached reads (caching not engaging on this route)"
		}
	}
	if lastIn > 0 {
		cw := cfg.ContextWindowFor(cfg.Model)
		msg += fmt.Sprintf("\nContext: %d / %d tokens (%d%% of %s's window)", lastIn, cw, contextPercent(lastIn, cw), shortModel(cfg.Model))
	}
	if cost := config.CostUSDFull(cfg.Model, in, out, cacheRead, cacheCreate); cost > 0 {
		msg += fmt.Sprintf("\nEstimated cost (cache-adjusted): $%.2f", cost)
		if full := config.CostUSD(cfg.Model, in, out); full > cost {
			msg += fmt.Sprintf(" (vs $%.2f at full price — prompt cache saved ~$%.2f)", full, full-cost)
		}
	}
	if quotaKnown {
		if budget > 0 {
			msg += fmt.Sprintf("\nFloodgate quota (24h): $%.2f / $%.2f", spend, budget)
		} else {
			msg += fmt.Sprintf("\nFloodgate quota (24h): $%.2f spent (no budget cap)", spend)
		}
	}
	// The diagnostic: if cached reads are a large share but the authoritative quota
	// is well below the full-price estimate, the route IS discounting caching.
	if quotaKnown && cacheRead > 0 {
		msg += "\n(If the quota above is much lower than the estimate, cached reads are being discounted.)"
	}
	return msg
}

func formatQuota(r *llm.UsageResponse) string {
	spend, budget := r.Usage.Spend, r.Quota.Budget.Spend
	if budget > 0 {
		return fmt.Sprintf("Floodgate quota (24h): $%.2f / $%.2f", spend, budget)
	}
	return fmt.Sprintf("Floodgate quota (24h): $%.2f spent (no budget cap)", spend)
}

func (s *Server) sessionsSummary(workingDir string) string {
	sessions, err := s.store.ListByDir(workingDir)
	if err != nil || len(sessions) == 0 {
		return "No saved sessions for this project yet."
	}
	var b strings.Builder
	b.WriteString("Sessions in this project (click one in the sidebar to open it):\n")
	for _, ss := range sessions {
		title := ss.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "- %s — %s (%s)\n", ss.ID, title, ss.UpdatedAt.Format("Jan 2 15:04"))
	}
	return strings.TrimRight(b.String(), "\n")
}

const initPrompt = "Please bootstrap an AGENTS.md for this repository.\n\n" +
	"1. Use list_dir, glob, and grep (and read_file when needed) to survey the codebase.\n" +
	"2. Identify: language(s), build/test commands, layout, conventions, anything unusual.\n" +
	"3. Write a concise AGENTS.md (200–500 words) with these sections:\n" +
	"   - **Project**: one-paragraph description.\n" +
	"   - **Build & test**: exact commands (e.g. `make build`, `make test`, `go vet ./...`).\n" +
	"   - **Layout**: top-level directories and what each holds.\n" +
	"   - **Conventions**: style/naming rules visible in the code.\n" +
	"   - **Caveats**: anything an agent should avoid touching or doing here.\n" +
	"4. Use write_file to save it as `AGENTS.md` at the repo root, then summarize what you wrote."

type userCommand struct {
	name        string
	description string
	template    string
}

func loadUserCommands(workingDir string) []userCommand {
	merged := map[string]userCommand{}
	if base, err := config.GlobalConfigDir(); err == nil {
		for _, c := range readCommandDir(filepath.Join(base, "commands"), "global") {
			merged[c.name] = c
		}
	}
	for _, c := range readCommandDir(filepath.Join(workingDir, ".sqe", "commands"), "project") {
		merged[c.name] = c
	}
	out := make([]userCommand, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func readCommandDir(dir, source string) []userCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []userCommand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		desc, body := parseCommandFile(string(data))
		if desc == "" {
			desc = source + " command"
		}
		out = append(out, userCommand{name: "/" + strings.TrimSuffix(e.Name(), ".md"), description: desc, template: body})
	}
	return out
}

func parseCommandFile(s string) (description, body string) {
	body = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", body
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", body
	}
	front := s[4 : 4+end]
	body = strings.TrimSpace(strings.TrimLeft(s[4+end+4:], "\r\n"))
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "description") {
			description = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return description, body
}

func expandUserCommand(cmd userCommand, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(cmd.template, "$ARGUMENTS") {
		return strings.ReplaceAll(cmd.template, "$ARGUMENTS", args)
	}
	if args == "" {
		return cmd.template
	}
	return cmd.template + "\n\n" + args
}
