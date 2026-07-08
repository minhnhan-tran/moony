package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	t.Run("interval", func(t *testing.T) {
		s, err := ParseSpec("every 30m")
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind != KindInterval || s.IntervalSec != 1800 {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("interval days", func(t *testing.T) {
		s, err := ParseSpec("every 2d")
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind != KindInterval || s.IntervalSec != 2*24*3600 {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("daily", func(t *testing.T) {
		s, err := ParseSpec("daily 09:30")
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind != KindDaily || s.AtMinute != 9*60+30 {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("once", func(t *testing.T) {
		s, err := ParseSpec("once 2030-01-02T08:00")
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind != KindOnce || s.At.Year() != 2030 {
			t.Errorf("got %+v", s)
		}
	})
	for _, bad := range []string{"", "every", "every 30s", "daily 25:00", "weekly 3", "once notatime"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("ParseSpec(%q) should error", bad)
		}
	}
	t.Run("once in the past is rejected", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).Format("2006-01-02T15:04:05")
		if _, err := ParseSpec("once " + past); err == nil {
			t.Errorf("ParseSpec of a past one-shot %q should error", past)
		}
	})
}

func TestSaveUsesRestrictivePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	st := OpenAt(path)
	base, _ := ParseSpec("every 15m")
	base.Prompt = "secret prompt"
	if _, err := st.Add(base); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("schedules.json perm = %o, want 600 (may contain sensitive prompts)", perm)
	}
}

func TestFormatListAndSummary(t *testing.T) {
	if got := FormatList(nil); got != "" {
		t.Errorf("FormatList(empty) = %q, want empty string", got)
	}
	s, _ := ParseSpec("every 30m")
	s.ID = "abc123"
	s.Name = "smoke"
	s.Prompt = "run the smoke tests"
	s.WorkingDir = "/tmp/x"
	s.Enabled = true
	s.NextRun = time.Now().Add(30 * time.Minute)

	out := FormatList([]Schedule{s})
	for _, want := range []string{"abc123", "smoke", "[enabled]", "every 30m", "run the smoke tests", "/tmp/x"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatList missing %q in:\n%s", want, out)
		}
	}

	s.Enabled = false
	if sum := s.Summary(); !strings.Contains(sum, "smoke") || !strings.Contains(sum, "[disabled]") {
		t.Errorf("Summary = %q, want name + [disabled]", sum)
	}
	s.Name = ""
	if s.DisplayName() != "(unnamed)" {
		t.Errorf("DisplayName(empty) = %q, want (unnamed)", s.DisplayName())
	}
}

func TestDueAndNextAfter(t *testing.T) {
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, time.Local)

	// Interval: next is now+interval; due once we pass it.
	iv := Schedule{Kind: KindInterval, IntervalSec: 3600, Enabled: true}
	iv.NextRun = iv.nextAfter(now)
	if !iv.NextRun.Equal(now.Add(time.Hour)) {
		t.Errorf("interval next = %v", iv.NextRun)
	}
	if iv.Due(now) {
		t.Error("interval should not be due before its next run")
	}
	if !iv.Due(now.Add(2 * time.Hour)) {
		t.Error("interval should be due after its next run")
	}

	// Daily: 09:00 has passed today (now=10:00) -> tomorrow.
	d := Schedule{Kind: KindDaily, AtMinute: 9 * 60, Enabled: true}
	d.NextRun = d.nextAfter(now)
	if d.NextRun.Hour() != 9 || d.NextRun.Day() != 19 {
		t.Errorf("daily next = %v, want tomorrow 09:00", d.NextRun)
	}

	// Disabled schedules are never due.
	d.Enabled = false
	if d.Due(d.NextRun.Add(time.Hour)) {
		t.Error("disabled schedule must not be due")
	}
}

func TestAdvanceOnceDisables(t *testing.T) {
	now := time.Now()
	once := Schedule{Kind: KindOnce, At: now, Enabled: true, NextRun: now}
	once.advance(now)
	if once.Enabled {
		t.Error("a one-shot should disable itself after running")
	}
	if !once.NextRun.IsZero() {
		t.Error("a fired one-shot should have no next run")
	}

	iv := Schedule{Kind: KindInterval, IntervalSec: 600, Enabled: true}
	iv.advance(now)
	if iv.NextRun.Before(now) || !iv.Enabled {
		t.Errorf("interval should reschedule and stay enabled: %+v", iv)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st := OpenAt(filepath.Join(t.TempDir(), "schedules.json"))

	// Empty store loads as empty.
	if list, err := st.Load(); err != nil || len(list) != 0 {
		t.Fatalf("empty load: %v %v", list, err)
	}

	base, _ := ParseSpec("every 15m")
	base.Prompt = "summarize the repo"
	base.WorkingDir = "/tmp/x"
	added, err := st.Add(base)
	if err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || !added.Enabled || added.NextRun.IsZero() {
		t.Fatalf("Add did not initialize: %+v", added)
	}

	got, ok := st.Get(added.ID)
	if !ok || got.Prompt != "summarize the repo" {
		t.Fatalf("Get: %+v ok=%v", got, ok)
	}

	if err := st.SetEnabled(added.ID, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.Get(added.ID); g.Enabled {
		t.Error("SetEnabled(false) did not persist")
	}

	if err := st.Remove(added.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get(added.ID); ok {
		t.Error("Remove did not delete")
	}
	if err := st.Remove("nope"); err == nil {
		t.Error("removing a missing id should error")
	}
}
