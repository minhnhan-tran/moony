package web

import (
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/doctor"
	sessionpkg "github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

func TestDoctorStatusString(t *testing.T) {
	cases := map[doctor.Status]string{
		doctor.StatusOK:   "ok",
		doctor.StatusWarn: "warn",
		doctor.StatusFail: "fail",
	}
	for st, want := range cases {
		if got := doctorStatusString(st); got != want {
			t.Errorf("doctorStatusString(%v) = %q, want %q", st, got, want)
		}
	}
}

// TestDoctorLiveChecks exercises the live LSP/MCP helpers against a session with
// no LSP manager and no configured MCP servers — both should report OK with a
// clear "nothing running/configured" detail rather than erroring.
func TestDoctorLiveChecks(t *testing.T) {
	dir := t.TempDir()
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "test", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, WorkingDir: dir}
	ag := agent.New(cfg, &fakeStreamer{}, tools.NewRegistry(dir), store, sessionpkg.New(cfg.Model, dir))
	sess := newSession("s1", "t", ag)

	if c := lspLiveCheck(sess); c.Status != doctor.StatusOK || c.Name != "lsp (live)" {
		t.Errorf("lspLiveCheck = %+v, want OK lsp (live)", c)
	}
	if c := mcpLiveCheck(sess, dir); c.Status != doctor.StatusOK || c.Name != "mcp (live)" {
		t.Errorf("mcpLiveCheck = %+v, want OK mcp (live)", c)
	}

	// EmbedClient must be nil for a client that can't embed (fakeStreamer),
	// and CheckEmbeddings must then treat it as "not configured" (model unset).
	if ag.EmbedClient() != nil {
		t.Error("EmbedClient should be nil for a non-embedding client")
	}
}
