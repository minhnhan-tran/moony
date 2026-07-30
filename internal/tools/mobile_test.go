package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func newMobileTool(t *testing.T) *mobileTool {
	t.Helper()
	return &mobileTool{r: NewRegistry(t.TempDir())}
}

func TestMobileToolMetadata(t *testing.T) {
	m := newMobileTool(t)
	if m.Name() != "mobile" {
		t.Errorf("Name() = %q, want mobile", m.Name())
	}
	if !m.Mutates() {
		t.Error("mobile tool should report Mutates()=true (it installs apps / changes device state)")
	}
	// Schema must be valid JSON with the action enum.
	var schema map[string]any
	if err := json.Unmarshal(m.Schema(), &schema); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
	if !strings.Contains(string(m.Schema()), "screenshot") {
		t.Error("schema should advertise the screenshot action")
	}
}

func TestMobileToolValidation(t *testing.T) {
	m := newMobileTool(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		input   string
		wantErr string // substring
	}{
		{"missing action", `{}`, "action is required"},
		{"missing platform", `{"action":"boot"}`, "platform is required"},
		{"bad platform", `{"action":"boot","platform":"blackberry"}`, "unknown platform"},
		{"ios boot needs device", `{"action":"boot","platform":"ios"}`, "device_id"},
		{"ios install needs args", `{"action":"install","platform":"ios","device_id":"X"}`, "path"},
		{"ios launch needs bundle", `{"action":"launch","platform":"ios","device_id":"X"}`, "bundle_id"},
		{"android install needs path", `{"action":"install","platform":"android","device_id":"X"}`, "path"},
		{"android launch needs bundle", `{"action":"launch","platform":"android","device_id":"X"}`, "bundle_id"},
		{"android text needs text", `{"action":"text","platform":"android","device_id":"X"}`, "text requires text"},
		{"android key needs key", `{"action":"key","platform":"android","device_id":"X"}`, "key requires key"},
		{"ios screenshot needs device", `{"action":"screenshot","platform":"ios"}`, "device_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := m.ExecuteImages(ctx, json.RawMessage(c.input))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestMobileDevicesSmoke verifies the devices listing never hard-errors even
// when xcrun/adb/emulator are absent — it degrades to "(… unavailable: …)"
// notes, mirroring the browser tool's Chrome-optional behavior. Always runnable
// in CI regardless of installed SDKs.
func TestMobileDevicesSmoke(t *testing.T) {
	m := newMobileTool(t)
	out, imgs, err := m.ExecuteImages(context.Background(), json.RawMessage(`{"action":"devices"}`))
	if err != nil {
		t.Fatalf("devices listing should not hard-error, got %v", err)
	}
	if imgs != nil {
		t.Errorf("devices listing should not return images, got %d", len(imgs))
	}
	for _, want := range []string{"iOS simulators", "Android devices", "Android AVDs"} {
		if !strings.Contains(out, want) {
			t.Errorf("devices output missing %q section:\n%s", want, out)
		}
	}
}

func TestAndroidEscapeText(t *testing.T) {
	cases := map[string]string{
		"hello":       "hello",
		"hello world": "hello%sworld",
		"a&b":         `a\&b`,
		"$x":          `\$x`,
	}
	for in, want := range cases {
		if got := androidEscapeText(in); got != want {
			t.Errorf("androidEscapeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMobileRegistered(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDefaults(0)
	if r.Get("mobile") == nil {
		t.Error("mobile tool should be registered by RegisterDefaults")
	}
}
