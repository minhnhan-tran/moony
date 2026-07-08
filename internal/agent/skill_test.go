package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/skills"
)

func TestSkillToolLoadsBody(t *testing.T) {
	tool := newSkillTool([]skills.Skill{
		{Name: "docx", Description: "Word docs", Body: "Do the docx thing.", Dir: "/skills/docx"},
		{Name: "pdf", Description: "PDFs", Body: "Do the pdf thing.", Dir: "/skills/pdf"},
	})

	// The catalog (name + description) is advertised in the description and schema.
	desc := tool.Description()
	if !strings.Contains(desc, "docx: Word docs") || !strings.Contains(desc, "pdf: PDFs") {
		t.Errorf("description missing skill catalog:\n%s", desc)
	}
	if !strings.Contains(string(tool.Schema()), `"enum"`) {
		t.Errorf("schema should constrain name to an enum: %s", tool.Schema())
	}
	if tool.Mutates() {
		t.Error("loading a skill should not be a mutating action")
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"docx"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Do the docx thing.") {
		t.Errorf("output missing skill body:\n%s", out)
	}
	if !strings.Contains(out, "/skills/docx") {
		t.Errorf("output should include the skill directory so bundled files can be read:\n%s", out)
	}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Error("unknown skill should error")
	}
}
