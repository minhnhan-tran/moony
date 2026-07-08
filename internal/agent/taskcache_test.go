package agent

import "testing"

// #3: the sub-agent (task) result cache primitives — deterministic keying,
// get/put, and clear-on-mutation.
func TestTaskCache(t *testing.T) {
	a := &Agent{}
	k := taskCacheKey("explore", "find X")
	if k != taskCacheKey("explore", "find X") {
		t.Fatal("taskCacheKey must be deterministic")
	}
	if k == taskCacheKey("explore", "find Y") {
		t.Fatal("different prompt must yield a different key")
	}
	if k == taskCacheKey("planner", "find X") {
		t.Fatal("different agent must yield a different key")
	}
	if _, ok := a.taskCacheGet(k); ok {
		t.Fatal("empty cache should miss")
	}
	a.taskCachePut(k, "findings")
	if v, ok := a.taskCacheGet(k); !ok || v != "findings" {
		t.Fatalf("get after put = %q,%v", v, ok)
	}
	a.clearTaskCache()
	if _, ok := a.taskCacheGet(k); ok {
		t.Fatal("clearTaskCache should empty the cache")
	}
}
