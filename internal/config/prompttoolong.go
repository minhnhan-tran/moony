package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// promptTooLongRE extracts the numbers from Anthropic/Bedrock's context-overflow
// message ("prompt is too long: 203297 tokens > 200000 maximum"). The ">" arrives
// variously literal, JSON-escaped, or HTML-escaped depending on how many layers
// wrapped the error, so all three are accepted.
var promptTooLongRE = regexp.MustCompile(`(?i)prompt is too long:\s*(\d+)\s*tokens\s*(?:>|\\u003e|&gt;)\s*(\d+)\s*maximum`)

// PromptTooLong reports whether err is a provider rejection for exceeding the
// model's context window, along with the token counts when the provider states them.
//
// This is the signal that a turn CAN be recovered by compacting: the request was
// refused before any generation, so nothing was consumed and nothing was lost —
// shrinking the history and re-sending is safe. Distinguish it from a rate limit or a
// quota stop, which compaction cannot help.
//
// used/max are 0 when the provider doesn't quantify the overflow; ok is still true,
// since knowing "it didn't fit" is enough to act on.
func PromptTooLong(err error) (used, max int, ok bool) {
	if err == nil {
		return 0, 0, false
	}
	s := err.Error()
	if m := promptTooLongRE.FindStringSubmatch(s); m != nil {
		used, _ = strconv.Atoi(m[1])
		max, _ = strconv.Atoi(m[2])
		return used, max, true
	}
	// Other providers phrase it differently; match the common variants so the
	// guidance still beats a raw 400 blob.
	ls := strings.ToLower(s)
	switch {
	case strings.Contains(ls, "prompt is too long"),
		strings.Contains(ls, "context length exceeded"),
		strings.Contains(ls, "maximum context length"),
		strings.Contains(ls, "context_length_exceeded"),
		strings.Contains(ls, "too many tokens"),
		strings.Contains(ls, "reduce the length of the messages"),
		strings.Contains(ls, "input is too long"):
		return 0, 0, true
	}
	return 0, 0, false
}

// PromptTooLongNote turns a context-overflow 400 into actionable guidance. The raw
// provider blob ("ValidationException: prompt is too long: 203297 tokens > 200000
// maximum" wrapped in JSON) tells the user nothing about what to DO — and a common
// cause is a quick action or a fallback moving the turn onto a smaller-window model,
// so a conversation that was fine a moment ago suddenly doesn't fit. model is the
// model that rejected the request (may be empty).
func PromptTooLongNote(err error, model string) string {
	return promptTooLongNote(err, model, "")
}

// PromptTooLongNoteFor adds the session's own model, so the message can point at a
// quick action's pin as the cause when that's what shrank the window.
func PromptTooLongNoteFor(err error, model, sessionModel string) string {
	return promptTooLongNote(err, model, sessionModel)
}

func promptTooLongNote(err error, model, sessionModel string) string {
	used, max, ok := PromptTooLong(err)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("This conversation is too long for ")
	if m := strings.TrimSpace(model); m != "" {
		b.WriteString(ModelDisplayName(m))
	} else {
		b.WriteString("the selected model")
	}
	if used > 0 && max > 0 {
		fmt.Fprintf(&b, ": the request needed ~%s tokens but the model's limit is %s.",
			FormatTokenCount(used), FormatTokenCount(max))
	} else {
		b.WriteString(": the request exceeded the model's context window.")
	}
	// Say this explicitly: the user's instinct after an error is to worry the session
	// is corrupt, and a refused request never reached the model at all.
	b.WriteString(" Nothing was sent, so the conversation is intact.")

	// When the turn was pinned onto this model by a quick action, that IS the cause:
	// the session's own model may have had ample room, so "switch models or /compact"
	// would misdirect the user into fixing a session that isn't broken.
	if sessionModel != "" && sessionModel != model {
		if sw := ContextWindow(sessionModel); max > 0 && sw > max {
			fmt.Fprintf(&b, " This turn was pinned to %s by a quick action, which has a smaller window than the session's %s (%s); the conversation itself fits there.",
				ModelDisplayName(model), ModelDisplayName(sessionModel), FormatTokenCount(sw))
			b.WriteString(" Give the action a context scope (recent/task) so it stops sending the whole conversation, or drop its model override.")
			return b.String()
		}
	}

	// Name concrete larger-window models when we can — "pick a bigger model" only
	// helps if the user knows which ones are bigger.
	if roomier := largerContextModels(model, max); len(roomier) > 0 {
		b.WriteString(" Switch to a model with a bigger context window (")
		b.WriteString(strings.Join(roomier, ", "))
		b.WriteString(") with /model, or run /compact to summarize the history first.")
	} else {
		b.WriteString(" Switch to a model with a bigger context window with /model, or run /compact to summarize the history first.")
	}
	return b.String()
}

// ModelDisplayName renders a model id for prose: the curated short label when there
// is one, otherwise the id minus its cloud-provider prefix ("gcp:gemini-2.5-pro"
// reads poorly mid-sentence).
func ModelDisplayName(id string) string {
	if label := ShortModelLabel(id); label != id {
		return label
	}
	if _, rest, ok := strings.Cut(id, ":"); ok && strings.TrimSpace(rest) != "" {
		return rest
	}
	return id
}

// ShortModelLabel maps a model id to its curated display name, or returns the id
// unchanged when it isn't in the known list.
func ShortModelLabel(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return id
	}
	stripped := stripPrefix(trimmed)
	for _, m := range AllKnownModels() {
		if m.ID == trimmed || m.ID == stripped {
			return m.Name
		}
	}
	return id
}

// largerContextModels lists up to three known models whose context window beats the
// one that just rejected the request, largest first.
func largerContextModels(current string, rejectedMax int) []string {
	floor := rejectedMax
	if floor <= 0 {
		floor = ContextWindow(current)
	}
	if floor <= 0 {
		return nil
	}
	type cand struct {
		name string
		win  int
	}
	var out []cand
	seen := map[string]bool{}
	curStripped := stripPrefix(strings.TrimSpace(current))
	for _, m := range AllKnownModels() {
		if stripPrefix(m.ID) == curStripped {
			continue
		}
		w := ContextWindow(m.ID)
		if w <= floor {
			continue
		}
		name := ModelDisplayName(m.ID)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, cand{name, w})
	}
	// Largest first, then by name so the suggestion list is stable across runs
	// (an unstable list makes the same error read differently each time).
	sort.Slice(out, func(i, j int) bool {
		if out[i].win != out[j].win {
			return out[i].win > out[j].win
		}
		return out[i].name < out[j].name
	})
	if len(out) > 3 {
		out = out[:3]
	}
	names := make([]string, 0, len(out))
	for _, c := range out {
		names = append(names, c.name)
	}
	return names
}

// FormatTokenCount renders a token count compactly for prose: 203297 -> "203K",
// 1500000 -> "1.5M".
func FormatTokenCount(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n >= 1_000_000:
		v := float64(n) / 1_000_000
		if v == float64(int(v)) {
			return fmt.Sprintf("%dM", int(v))
		}
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "M"
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return strconv.Itoa(n)
	}
}
