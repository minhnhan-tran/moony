// Package notify fires a best-effort outbound webhook when a turn finishes
// or an approval is needed — useful for headless/scheduled runs and long
// turns where nobody's watching the terminal or browser tab.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Kind identifies which lifecycle event a notification is for.
type Kind string

const (
	TurnDone Kind = "turn_done"
	Approval Kind = "approval"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// runAsync executes f in the background so Send never blocks its caller on a
// slow/unreachable webhook. A var so tests can run it inline.
var runAsync = func(f func()) { go f() }

// WebhookURL returns MN_NOTIFY_WEBHOOK_URL, or "" (disabled) when unset.
func WebhookURL() string { return strings.TrimSpace(os.Getenv("MN_NOTIFY_WEBHOOK_URL")) }

// MinSeconds is the minimum turn duration worth a turn_done notification
// (MN_NOTIFY_MIN_SECONDS, default 20s) — Approval ignores this; it's
// blocking, not a "how long did it take" signal.
func MinSeconds() float64 {
	v := strings.TrimSpace(os.Getenv("MN_NOTIFY_MIN_SECONDS"))
	if v == "" {
		return 20
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 20
	}
	return f
}

// enabledEvents returns the MN_NOTIFY_EVENTS allow-list (comma-separated
// subset of "turn_done,approval"); unset/empty means both are enabled.
func enabledEvents() map[Kind]bool {
	v := strings.TrimSpace(os.Getenv("MN_NOTIFY_EVENTS"))
	if v == "" {
		return map[Kind]bool{TurnDone: true, Approval: true}
	}
	out := map[Kind]bool{}
	for _, part := range strings.Split(v, ",") {
		if k := Kind(strings.TrimSpace(part)); k != "" {
			out[k] = true
		}
	}
	return out
}

// Send fires a fire-and-forget webhook notification for kind, unless
// notifications are disabled (no MN_NOTIFY_WEBHOOK_URL), kind is filtered
// out by MN_NOTIFY_EVENTS, or (for TurnDone only) elapsed is under
// MinSeconds. The actual HTTP call runs asynchronously with its own timeout —
// a slow or unreachable webhook never delays or fails the turn.
func Send(kind Kind, text string, elapsed time.Duration) {
	url := WebhookURL()
	if url == "" || !enabledEvents()[kind] {
		return
	}
	if kind == TurnDone && elapsed.Seconds() < MinSeconds() {
		return
	}
	// Both "text" (Slack/Mattermost incoming webhooks) and "content" (Discord)
	// keys, so one URL works with either format without the caller having to
	// know which service it points at.
	body, err := json.Marshal(map[string]string{"text": text, "content": text})
	if err != nil {
		return
	}
	runAsync(func() { postWebhook(url, body) })
}

func postWebhook(url string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}
