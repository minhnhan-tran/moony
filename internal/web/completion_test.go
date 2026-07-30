package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// TestHandleComplete covers the paths of POST /api/complete that don't require a
// live LLM: malformed JSON is rejected, and an empty prefix+suffix short-circuits
// to an empty completion without making a model call.
func TestHandleComplete(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()

	t.Run("invalid JSON", func(t *testing.T) {
		rec := postJSON(t, h, "POST", "/api/complete", `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body)
		}
	})

	t.Run("empty context short-circuits", func(t *testing.T) {
		rec := postJSON(t, h, "POST", "/api/complete", `{"language":"go","prefix":"   ","suffix":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body)
		}
		var resp struct {
			Completion string `json:"completion"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not valid JSON: %v\nbody: %s", err, rec.Body)
		}
		if resp.Completion != "" {
			t.Errorf("completion = %q, want empty", resp.Completion)
		}
	})
}

func TestCleanCompletion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "foo()", "foo()"},
		{"trailing ws", "foo()  \n", "foo()"},
		{"fenced with lang", "```go\nfoo()\n```", "foo()"},
		{"fenced no lang", "```\nfoo()\n```", "foo()"},
		{"leading ws preserved", "\n\tfoo()", "\n\tfoo()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanCompletion(tc.in); got != tc.want {
				t.Errorf("cleanCompletion(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTailHeadBytes(t *testing.T) {
	if got := tailBytes("abcdef", 3); got != "def" {
		t.Errorf("tailBytes = %q, want %q", got, "def")
	}
	if got := tailBytes("ab", 5); got != "ab" {
		t.Errorf("tailBytes short = %q, want %q", got, "ab")
	}
	if got := headBytes("abcdef", 3); got != "abc" {
		t.Errorf("headBytes = %q, want %q", got, "abc")
	}
	if got := headBytes("ab", 5); got != "ab" {
		t.Errorf("headBytes short = %q, want %q", got, "ab")
	}
}
