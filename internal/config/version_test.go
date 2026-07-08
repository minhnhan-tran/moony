package config

import "testing"

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.2.1", true},   // patch behind
		{"1.2.1", "1.3.0", true},   // minor behind
		{"0.17.0", "1.0.0", true},  // major behind
		{"1.2.1", "1.2.1", false},  // equal
		{"1.3.0", "1.2.9", false},  // ahead
		{"v1.2.0", "v1.2.1", true}, // tolerate leading "v"
		{"1.2", "1.2.1", false},    // unparseable (2 parts) -> no false positive
		{"1.2.1", "latest", false}, // unparseable rhs -> no false positive
		{"", "1.2.1", false},       // empty -> no false positive
	}
	for _, tc := range cases {
		if got := VersionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("VersionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
