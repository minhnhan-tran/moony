package corpca

import "testing"

func TestPoolNotEmpty(t *testing.T) {
	p := Pool()
	if p == nil {
		t.Fatal("nil pool")
	}
	if len(p.Subjects()) == 0 { //nolint:staticcheck
		t.Fatal("expected non-empty pool")
	}
}
