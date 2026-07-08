package eval

// Fixtures are a small, self-contained set of starter evaluation tasks. They're
// deliberately tiny and language-local (Go, which the toolchain already has) so
// the harness can run end-to-end without external setup. Grow this set — or load
// project-specific tasks — as the real benchmark. Each task seeds a minimal repo
// and a Check command that only passes if the agent did the work correctly.
func Fixtures() []Task {
	return []Task{
		{
			Name:   "implement-add",
			Prompt: "Implement the Add function in add.go so it returns the sum of its two int arguments. Keep the package name 'calc'.",
			Files: map[string]string{
				"go.mod": "module calc\n\ngo 1.21\n",
				"add.go": "package calc\n\n// Add returns a + b.\nfunc Add(a, b int) int {\n\tpanic(\"not implemented\")\n}\n",
				"add_test.go": "package calc\n\nimport \"testing\"\n\n" +
					"func TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2,3) != 5\")\n\t}\n\tif Add(-1, 1) != 0 {\n\t\tt.Fatal(\"Add(-1,1) != 0\")\n\t}\n}\n",
			},
			Check: "go test ./...",
		},
		{
			Name:   "fix-build-error",
			Prompt: "The package doesn't compile. Find and fix the build error in greet.go without changing what the function is meant to do.",
			Files: map[string]string{
				"go.mod": "module greet\n\ngo 1.21\n",
				// Missing return + stray token: must be fixed to build.
				"greet.go": "package greet\n\nimport \"fmt\"\n\n// Greet returns a greeting for name.\nfunc Greet(name string) string {\n\tfmt.Sprintf(\"hello, %s\", name)\n}\n",
			},
			Check: "go build ./...",
		},
	}
}
