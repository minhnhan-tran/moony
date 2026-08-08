package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// The regression this fixes: under the DEFAULT policy, POST /jobs blocked the request
// while waiting for approval — but the prompt is rendered by the very UI whose fetch was
// still pending, and the manage modal covered the approval bar, so the user could not
// answer and the request hung forever.
//
// Every policy must now answer promptly.
func TestJobStartNeverBlocks(t *testing.T) {
	for _, pol := range []string{
		config.ApprovalPrompt, config.ApprovalSafe, config.ApprovalAuto, config.ApprovalDangerSkip,
	} {
		t.Run(pol, func(t *testing.T) {
			srv, id := jobsFixture(t, pol)

			type res struct {
				code int
				body map[string]any
			}
			done := make(chan res, 1)
			go func() {
				rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo hi"}`)
				done <- res{rec.Code, out}
			}()

			select {
			case got := <-done:
				switch pol {
				case config.ApprovalAuto, config.ApprovalDangerSkip:
					// No prompt needed: answered inline with the job id.
					if got.code != http.StatusOK {
						t.Fatalf("status %d, want 200", got.code)
					}
					if got.body["id"] == nil {
						t.Errorf("no job id in %v", got.body)
					}
					if got.body["pending"] == true {
						t.Error("no approval was needed, so the reply must not be pending")
					}
				default:
					// Approval needed: 202 + a ticket, never a blocked request.
					if got.code != http.StatusAccepted {
						t.Fatalf("status %d, want 202", got.code)
					}
					if got.body["pending"] != true {
						t.Errorf("expected pending:true, got %v", got.body)
					}
					if tkt, _ := got.body["ticket"].(string); tkt == "" {
						t.Error("no ticket to correlate the outcome with")
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("policy %s: POST /jobs blocked — the browser's fetch would hang forever", pol)
			}
		})
	}
}

// Tickets must be unique, or two tabs starting a job would react to each other's outcome.
func TestJobTicketsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tk := newJobTicket()
		if tk == "" {
			t.Fatal("empty ticket")
		}
		if seen[tk] {
			t.Fatalf("duplicate ticket %q", tk)
		}
		seen[tk] = true
	}
}

// The whole point of the fix: an approval can now actually be answered, and answering it
// starts the job. Previously the request held the connection open, so the prompt was
// unanswerable and this flow was impossible.
func TestJobStartApprovedThenRuns(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalPrompt)

	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo approved-probe"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	ticket, _ := out["ticket"].(string)
	if ticket == "" {
		t.Fatal("no ticket")
	}

	// Answer the prompt the way the browser's approval bar does.
	var approved bool
	for i := 0; i < 100; i++ {
		ar, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/approve", `{"decision":"approve"}`)
		if ar.Code == http.StatusNoContent {
			approved = true
			break
		}
		time.Sleep(20 * time.Millisecond) // the gate may not have raised the prompt yet
	}
	if !approved {
		t.Fatal("could not deliver the approval")
	}

	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()

	var started bool
	for i := 0; i < 150; i++ {
		for _, j := range sessionJobWires(sess) {
			if strings.Contains(j.Command, "approved-probe") {
				started = true
			}
		}
		if started {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !started {
		t.Error("approving the prompt did not start the job")
	}
}

// A denial must be reported, and must not start anything.
func TestJobStartDeniedDoesNotRun(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalPrompt)

	rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo denied-probe"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}
	for i := 0; i < 100; i++ {
		ar, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/approve", `{"decision":"deny"}`)
		if ar.Code == http.StatusNoContent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()

	time.Sleep(300 * time.Millisecond)
	for _, j := range sessionJobWires(sess) {
		if strings.Contains(j.Command, "denied-probe") {
			t.Error("a denied job was started anyway")
		}
	}
}

// The approval channel must be released even when the prompt is never answered, or the
// first unanswered start would block every later one with a 409 forever.
func TestJobPromptClaimReleasedAfterOutcome(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalPrompt)

	rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo first"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}
	// While the first is pending, a second start must be refused rather than racing it.
	rec2, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo second"}`)
	if rec2.Code != http.StatusConflict {
		t.Errorf("second concurrent start: status %d, want 409", rec2.Code)
	}

	// Answer the first; the claim must then be free.
	for i := 0; i < 100; i++ {
		ar, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/approve", `{"decision":"deny"}`)
		if ar.Code == http.StatusNoContent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var freed bool
	for i := 0; i < 100; i++ {
		rec3, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo third"}`)
		if rec3.Code == http.StatusAccepted {
			freed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !freed {
		t.Error("the approval claim was never released, so no further job could be started")
	}
}
