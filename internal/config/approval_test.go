package config

import "testing"

func TestNormalizeApprovalPolicy(t *testing.T) {
	cases := map[string]string{
		"":                             ApprovalPrompt, // default
		"prompt":                       ApprovalPrompt,
		"PROMPT":                       ApprovalPrompt,
		"all":                          ApprovalPrompt, // unknown → default
		"safe":                         ApprovalSafe,
		"read-only":                    ApprovalSafe,
		"auto":                         ApprovalAuto,
		"off":                          ApprovalAuto,
		"none":                         ApprovalAuto,
		"yolo":                         ApprovalAuto,
		"  Auto  ":                     ApprovalAuto,
		"danger-skip-permission":       ApprovalDangerSkip,
		"danger-skip-permissions":      ApprovalDangerSkip,
		"skip-permissions":             ApprovalDangerSkip,
		"dangerously-skip-permissions": ApprovalDangerSkip,
		"danger":                       ApprovalDangerSkip,
		"DANGER-SKIP":                  ApprovalDangerSkip,
	}
	for in, want := range cases {
		if got := NormalizeApprovalPolicy(in); got != want {
			t.Errorf("NormalizeApprovalPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeBashCommand(t *testing.T) {
	safe := []string{
		"ls -la",
		"cat go.mod",
		"grep -rn foo internal",
		"cat x.go | grep func | wc -l",
		"git status",
		"git diff --stat",
		"git log --oneline -5",
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"head -n 20 main.go && tail -n 5 main.go",
		"find . -name '*.go'",
		"rg TODO || true",
	}
	for _, c := range safe {
		if !SafeBashCommand(c) {
			t.Errorf("expected SAFE: %q", c)
		}
	}

	unsafe := []string{
		"",
		"rm -rf build",
		"mv a b",
		"echo hi > out.txt",    // redirection
		"cat x >> y",           // append redirection
		"go run ./cmd",         // executes arbitrary code
		"go install ./...",     // writes to GOBIN
		"go get example.com/x", // fetches/mutates deps
		"go mod tidy",          // mutates go.mod/sum
		"git push",             // mutating git
		"git commit -m x",      // mutating git
		"git checkout main",    // mutating git
		"ls $(rm x)",           // command substitution
		"ls `whoami`",          // backtick substitution
		"sleep 1 &",            // backgrounding
		"ls && rm x",           // one unsafe segment
		"cat a | tee b",        // tee writes
		"FOO=bar ls",           // env assignment prefix
		"python script.py",     // arbitrary interpreter
		"sed -i s/a/b/ f",      // in-place edit
	}
	for _, c := range unsafe {
		if SafeBashCommand(c) {
			t.Errorf("expected UNSAFE: %q", c)
		}
	}
}
