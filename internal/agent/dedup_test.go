package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

// #4: a fresh tool result byte-identical to a large result already in history is
// collapsed to a pointer stub; small or unique results are left untouched.
func TestDedupRepeatedResults(t *testing.T) {
	big := strings.Repeat("diff line\n", 200) // well over the size floor

	a := &Agent{messages: []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.ToolResultBlock("prev", big, false)},
	}}}
	calls := []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "new", Name: "bash"}}
	results := []llm.ContentBlock{llm.ToolResultBlock("new", big, false)}
	a.dedupRepeatedResults(calls, results)
	if !strings.Contains(results[0].Content, "identical") {
		t.Fatalf("identical large result should be stubbed, got:\n%s", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "bash") {
		t.Errorf("stub should name the tool, got:\n%s", results[0].Content)
	}

	// A unique large result is left intact.
	uniq := strings.Repeat("other\n", 200)
	results2 := []llm.ContentBlock{llm.ToolResultBlock("n2", uniq, false)}
	a.dedupRepeatedResults(calls, results2)
	if results2[0].Content != uniq {
		t.Errorf("unique result must not be stubbed")
	}

	// A small identical result is below the floor and left intact.
	small := "tiny output"
	a2 := &Agent{messages: []llm.Message{{
		Content: []llm.ContentBlock{llm.ToolResultBlock("p", small, false)},
	}}}
	results3 := []llm.ContentBlock{llm.ToolResultBlock("n3", small, false)}
	a2.dedupRepeatedResults(calls, results3)
	if results3[0].Content != small {
		t.Errorf("small result must not be stubbed, got:\n%s", results3[0].Content)
	}
}
