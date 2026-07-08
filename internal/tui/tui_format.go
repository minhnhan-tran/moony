// tui_format.go — stateless string/format/util helpers for the TUI.

package tui

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/config"
)

// shortDur formats an elapsed duration compactly: 416ms / 5.3s / 53s / 3m 34s
// (mirrors the web fmtDur).
func shortDur(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	s := float64(ms) / 1000
	if s < 60 {
		if s < 10 {
			return fmt.Sprintf("%.1fs", s)
		}
		return fmt.Sprintf("%.0fs", s)
	}
	return fmt.Sprintf("%dm %ds", int(s)/60, int(s)%60)
}

// splitProjectName splits a (pretty) directory path into its parent prefix
// (everything up to and including the last separator) and the final segment —
// the project name. Only the project name is emphasized in the header.
func splitProjectName(p string) (parent, name string) {
	if i := strings.LastIndex(p, string(os.PathSeparator)); i >= 0 {
		return p[:i+1], p[i+1:]
	}
	return "", p
}

// matchCommands returns the slash commands whose name starts with prefix.
func matchCommands(cmds []slashCommand, prefix string) []slashCommand {
	var out []slashCommand
	for _, c := range cmds {
		if strings.HasPrefix(c.name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// commonPrefix returns the longest common name prefix across the commands.
func commonPrefix(cmds []slashCommand) string {
	if len(cmds) == 0 {
		return ""
	}
	cp := cmds[0].name
	for _, c := range cmds[1:] {
		for !strings.HasPrefix(c.name, cp) {
			cp = cp[:len(cp)-1]
		}
	}
	return cp
}

// markdownStyle picks the glamour theme for assistant markdown. It defaults to
// the dark or light theme based on the terminal background (detected once at
// startup) and deliberately never uses glamour's "auto" style: "auto" probes
// the terminal's background color at runtime via OSC queries, which race Bubble
// Tea's input reader and leak escape sequences (e.g. `]11;rgb:…` / `[…R`) into
// the textarea. An explicit GLAMOUR_STYLE env var still wins, except for "auto".
func markdownStyle() string {
	if s := os.Getenv("GLAMOUR_STYLE"); s != "" && s != "auto" {
		return s
	}
	if lipgloss.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// soundEnabled reports whether the turn-complete chime should play. On by
// default; disable with SQE_SOUND=0 (or off/false/no).
func soundEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_SOUND"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// markdownStyleConfig returns a glamour style derived from the dark/light theme
// with the heading prefixes cleared. By default glamour renders ATX headings
// with a literal "## "/"### " prefix (see styles.go in the glamour module);
// that looks like unrendered markdown, so we strip it. ok is false when a custom
// GLAMOUR_STYLE is set (not dark/light), in which case the caller should fall
// back to that named style as-is.
func markdownStyleConfig() (ansi.StyleConfig, bool) {
	var base ansi.StyleConfig
	switch markdownStyle() {
	case "dark":
		base = styles.DarkStyleConfig
	case "light":
		base = styles.LightStyleConfig
	default:
		return ansi.StyleConfig{}, false
	}
	for _, h := range []*ansi.StyleBlock{&base.H1, &base.H2, &base.H3, &base.H4, &base.H5, &base.H6} {
		h.Prefix = ""
	}
	return base, true
}

func isCanceled(err error) bool {
	return err == context.Canceled || strings.Contains(err.Error(), "context canceled")
}

// sanitizeForInput strips leaked terminal-control fragments from a KeyMsg before
// it reaches the textarea, returning the (possibly cleaned) message and whether
// anything remains worth forwarding. Non-rune keys pass through untouched. This
// is the guard that keeps mouse-scroll escape sequences from being typed into
// the input box (see sgrMouseFragmentRe).
func sanitizeForInput(msg tea.Msg) (tea.Msg, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok || k.Type != tea.KeyRunes {
		return msg, true
	}
	// A lone "[" or "O" carrying the Alt modifier is the leftover CSI/SS3
	// introducer of an escape sequence (e.g. a mouse report "\x1b[<…M") that a
	// short read split right after its ESC byte: Bubble Tea's fallback parser
	// turns the surviving "\x1b[" into Alt+[. A real Option+[ / Option+O yields
	// a different rune, and neither is a textarea binding, so dropping these is
	// safe and stops a stray "[" from appearing while scrolling.
	if k.Alt && len(k.Runes) == 1 && (k.Runes[0] == '[' || k.Runes[0] == 'O') {
		return msg, false
	}
	s := string(k.Runes)
	if csiFragmentRe.MatchString(s) {
		return msg, false
	}
	cleaned := sgrMouseFragmentRe.ReplaceAllString(s, "")
	if cleaned == s {
		return msg, true // nothing stripped — forward as typed
	}
	if cleaned == "" {
		return msg, false // the whole burst was leak
	}
	k.Runes = []rune(cleaned)
	return tea.KeyMsg(k), true
}

// appendHistory adds s to the history unless it duplicates the last entry.
func appendHistory(h []string, s string) []string {
	if n := len(h); n > 0 && h[n-1] == s {
		return h
	}
	return append(h, s)
}

// currentItem returns the index of the first item marked current, or 0.
func currentItem(items []menuItem) int {
	for i, it := range items {
		if it.current {
			return i
		}
	}
	return 0
}

// digitKey returns the integer value of a single-digit key string, or -1.
func digitKey(s string) int {
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return -1
}

// clampPct converts a 0..1 ratio to a 0..100 integer percentage.
// formatUSD formats a dollar amount with adaptive precision: cents for
// sub-dollar values, two decimals up to $99.99, then whole dollars beyond.
// mouseWheelDelta returns the lines-per-wheel-tick value, honoring the
// SQE_WHEEL_DELTA env var (1–10). The default of 2 balances trackpad and
// notched-mouse feel.
func mouseWheelDelta() int {
	if v := strings.TrimSpace(os.Getenv("SQE_WHEEL_DELTA")); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return 2
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 10 {
			return n
		}
	}
	return 2
}

func formatUSD(v float64) string {
	switch {
	case v < 0.01:
		return fmt.Sprintf("%.4f", v)
	case v < 100:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// formatTokens renders a token count compactly for the status bar: the raw
// number below 1,000, "x.xk" in the thousands, and "x.xm" at a million or more
// (e.g. 942 -> "942", 1500 -> "1.5k", 2_300_000 -> "2.3m").
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func clampPct(f float64) int {
	p := int(f * 100)
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// prettyDir shortens a path for display: it maps the home dir to "~" and, when
// the result is long, abbreviates each parent segment to its first letter
// (fish-style) while keeping the final folder in full, e.g.
// ~/Documents/Source Code/moony → ~/D/S/moony.
func prettyDir(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			p = "~"
		} else if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			p = "~" + p[len(home):]
		}
	}
	const maxW = 40
	if len([]rune(p)) <= maxW {
		return p
	}
	return abbreviateDir(p)
}

// abbreviateDir shortens each parent path segment to a leading letter (keeping a
// leading dot for hidden dirs) and preserves the final segment in full.
func abbreviateDir(p string) string {
	sep := string(os.PathSeparator)
	leading := ""
	rest := p
	if strings.HasPrefix(rest, sep) {
		leading, rest = sep, strings.TrimPrefix(rest, sep)
	}
	parts := strings.Split(rest, sep)
	if len(parts) <= 1 {
		return p
	}
	for i := 0; i < len(parts)-1; i++ {
		parts[i] = abbrevSegment(parts[i])
	}
	return leading + strings.Join(parts, sep)
}

// abbrevSegment returns the first rune of a path segment, keeping a leading dot
// for hidden directories (".config" → ".c").
func abbrevSegment(s string) string {
	r := []rune(s)
	switch {
	case len(r) == 0:
		return ""
	case r[0] == '.' && len(r) > 1:
		return string(r[:2])
	default:
		return string(r[:1])
	}
}

// shortModel produces a compact, readable model label from a Floodgate id,
// stripping the OpenAI-route cloud prefix ("aws:"/"gcp:"), the "anthropic."
// vendor prefix, and any trailing ":0" version tag.
func shortModel(model string) string {
	if rest, ok := strings.CutPrefix(model, "aws:"); ok {
		model = rest
	} else if rest, ok := strings.CutPrefix(model, "gcp:"); ok {
		model = rest
	}
	model = strings.TrimPrefix(model, "anthropic.")
	if i := strings.IndexByte(model, ':'); i >= 0 {
		model = model[:i]
	}
	return model
}

// relayWebURL derives the browsable https origin for a configured relay from
// its host:port dial address (empty when no relay is set). Port 443 is elided.
func relayWebURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	// RelayAddr is the tunnel dial-in address (host:8771, a raw-TCP VIP). The
	// mirror web UI is served by the same relay host over the HTTPS ingress
	// (443), NOT the tunnel port — so drop the port and use the bare host.
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil && h != "" {
		host = h
	}
	return "https://" + host
}

// welcomeBanner renders the boxed greeting shown on a fresh session.
func welcomeBanner(cfg *config.Config) string {
	var b strings.Builder
	b.WriteString(asciiLogo())
	b.WriteString("\n\n")
	b.WriteString(infoStyle.Render("A terminal coding agent — bring your own model provider"))
	b.WriteString("\n\n")
	b.WriteString(headerMetaStyle.Render("model       ") + bodyStyle.Render(shortModel(cfg.Model)) + "\n")
	b.WriteString(headerMetaStyle.Render("reasoning   ") + bodyStyle.Render(cfg.ReasoningLevel) + "\n")
	b.WriteString(headerMetaStyle.Render("dir         ") + bodyStyle.Render(prettyDir(cfg.WorkingDir)))
	if url := relayWebURL(cfg.RelayAddr); url != "" {
		b.WriteString("\n" + headerMetaStyle.Render("mirror web  ") + bodyStyle.Render(url))
	}
	b.WriteString("\n\n")
	b.WriteString(
		welcomeKeyStyle.Render("enter") + infoStyle.Render(" send") +
			infoStyle.Render("    ") + welcomeKeyStyle.Render("/help") + infoStyle.Render(" commands") +
			infoStyle.Render("    ") + welcomeKeyStyle.Render("/models") + infoStyle.Render(" pick model") +
			infoStyle.Render("    ") + welcomeKeyStyle.Render("ctrl+c") + infoStyle.Render(" quit"),
	)
	return welcomeBoxStyle.Render(b.String())
}

// logoGlyphs is a compact 5-row block font for the wordmark. Every glyph is
// padded to a fixed width so columns line up when joined.
var logoGlyphs = map[rune][5]string{
	'S': {"████", "█   ", "████", "   █", "████"},
	'Q': {"████", "█  █", "█ ██", "████", "   █"},
	'E': {"████", "█   ", "███ ", "█   ", "████"},
	'C': {"████", "█   ", "█   ", "█   ", "████"},
	'O': {"████", "█  █", "█  █", "█  █", "████"},
	'D': {"███ ", "█  █", "█  █", "█  █", "███ "},
	'A': {"████", "█  █", "████", "█  █", "█  █"},
	'G': {"████", "█   ", "█ ██", "█  █", "████"},
	'N': {"█  █", "██ █", "█ ██", "█  █", "█  █"},
	'T': {"████", " █  ", " █  ", " █  ", " █  "},
	' ': {"  ", "  ", "  ", "  ", "  "},
}

// asciiLogo renders "SQE CODE AGENT" in the compact block font with a vertical
// blue→pink gradient.
func asciiLogo() string {
	const word = "SQEQCT CODING AGENT"
	var rows [5]strings.Builder
	for i, r := range word {
		g, ok := logoGlyphs[r]
		if !ok {
			continue
		}
		for line := 0; line < 5; line++ {
			if i > 0 {
				rows[line].WriteByte(' ') // inter-glyph gap
			}
			rows[line].WriteString(g[line])
		}
	}

	var b strings.Builder
	gradient := logoGradientColors()
	for line := 0; line < 5; line++ {
		if line > 0 {
			b.WriteByte('\n')
		}
		c := gradient[line%len(gradient)]
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Render(rows[line].String()))
	}
	return b.String()
}
