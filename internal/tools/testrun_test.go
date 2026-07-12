package tools

import (
	"strings"
	"testing"
)

func TestParseGoTestJSON(t *testing.T) {
	// A representative test2json stream: two passing tests, one failing test
	// (with output), one skip, plus a coverage line.
	stream := strings.Join([]string{
		`{"Action":"run","Package":"ex/pkg","Test":"TestA"}`,
		`{"Action":"pass","Package":"ex/pkg","Test":"TestA","Elapsed":0.01}`,
		`{"Action":"run","Package":"ex/pkg","Test":"TestB"}`,
		`{"Action":"output","Package":"ex/pkg","Test":"TestB","Output":"    foo_test.go:9: want 1 got 2\n"}`,
		`{"Action":"fail","Package":"ex/pkg","Test":"TestB","Elapsed":0.02}`,
		`{"Action":"run","Package":"ex/pkg","Test":"TestC"}`,
		`{"Action":"skip","Package":"ex/pkg","Test":"TestC"}`,
		`{"Action":"output","Package":"ex/pkg","Output":"coverage: 72.4% of statements\n"}`,
		`{"Action":"pass","Package":"ex/pkg","Test":"TestD","Elapsed":0.01}`,
		`{"Action":"fail","Package":"ex/pkg","Elapsed":0.05}`,
		`not json, should be ignored`,
	}, "\n")

	s := parseGoTestJSON([]byte(stream))
	if s.passed != 2 {
		t.Errorf("passed = %d, want 2", s.passed)
	}
	if s.failed != 1 {
		t.Errorf("failed = %d, want 1", s.failed)
	}
	if s.skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.skipped)
	}
	if s.coverage != "72.4% of statements" {
		t.Errorf("coverage = %q", s.coverage)
	}
	if len(s.failures) != 1 || s.failures[0].name != "ex/pkg.TestB" {
		t.Fatalf("failures = %+v", s.failures)
	}
	if !strings.Contains(s.failures[0].output, "want 1 got 2") {
		t.Errorf("failure output missing: %q", s.failures[0].output)
	}

	out := s.render(10000)
	if !strings.HasPrefix(out, "FAIL") {
		t.Errorf("render should start with FAIL: %q", out)
	}
	if !strings.Contains(out, "coverage 72.4%") || !strings.Contains(out, "ex/pkg.TestB") {
		t.Errorf("render missing details: %q", out)
	}
}

func TestParseGoTestJSONAllPass(t *testing.T) {
	stream := `{"Action":"pass","Package":"p","Test":"T1"}` + "\n" +
		`{"Action":"pass","Package":"p","Test":"T2"}`
	s := parseGoTestJSON([]byte(stream))
	if s.failed != 0 || s.passed != 2 {
		t.Fatalf("unexpected: %+v", s)
	}
	if got := s.render(10000); !strings.HasPrefix(got, "PASS") {
		t.Errorf("render = %q, want PASS prefix", got)
	}
}
