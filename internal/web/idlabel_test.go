package web

import "testing"

func TestLooksLikeIDMatchesSessionIDFormat(t *testing.T) {
	// moony session ids are "20060102-150405-<4 hex>" (session.newID).
	if !looksLikeID("20260730-213827-fc4a") {
		t.Error("session id not collapsed into a metric label — ids would leak into /metrics")
	}
	if looksLikeID("codebasemap") || looksLikeID("quickactions") {
		t.Error("route word wrongly collapsed to :id")
	}
}
