package web

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// readSourceFile reads a file from this package's own directory. The assertions below
// are about the SOURCE (which fields the mirror copies), which can't be observed from
// the built types alone.
func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

// The terminal-session mirror copies bridge.Status onto statusWire FIELD BY FIELD, so
// a new status field is silently dropped for mirrored clients unless it's added there
// too. This asserts the quota-fallback fields survive that hop.
func TestMirrorCarriesQuotaFallbackFields(t *testing.T) {
	src, err := readSourceFile("mirror.go")
	if err != nil {
		t.Fatalf("read mirror.go: %v", err)
	}
	for _, field := range []string{"QuotaFallbackFrom", "QuotaFallbackResetAt"} {
		if !strings.Contains(src, field+":") {
			t.Errorf("mirror.go does not copy %s — mirrored clients would never see an "+
				"active quota fallback, so the provider would appear to change for no reason", field)
		}
	}
}

// Guard the invariant generally: every field of statusWire that the mirror is
// responsible for should appear in mirror.go. Catches the same class of omission for
// future fields, which is otherwise invisible until someone notices missing UI.
func TestMirrorCopiesEveryQuotaStatusField(t *testing.T) {
	src, err := readSourceFile("mirror.go")
	if err != nil {
		t.Fatalf("read mirror.go: %v", err)
	}
	st := reflect.TypeOf(statusWire{})
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		if !strings.HasPrefix(name, "Quota") {
			continue // scope to the quota block this change touches
		}
		if !strings.Contains(src, name+":") {
			t.Errorf("statusWire.%s is not copied in mirror.go", name)
		}
	}
}
