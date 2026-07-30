package llm

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fastPolls shrinks the poll interval so tests don't wait; restores on cleanup.
func fastPolls(t *testing.T) {
	t.Helper()
	prev := videoPollInterval
	videoPollInterval = time.Millisecond
	t.Cleanup(func() { videoPollInterval = prev })
}

// videoRT routes the two Gemini video endpoints: predictLongRunning returns an
// operation name; fetchPredictOperation returns notDone first, then done.
func videoRT(t *testing.T, notDone, done string) roundTripperFunc {
	polls := 0
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, ":predictLongRunning"):
			return resp(200, `{"name":"projects/p/locations/l/operations/op1"}`), nil
		case strings.Contains(r.URL.Path, ":fetchPredictOperation"):
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "op1") {
				t.Errorf("fetch missing operation name: %s", b)
			}
			polls++
			if polls < 2 {
				return resp(200, notDone), nil
			}
			return resp(200, done), nil
		default:
			t.Fatalf("unexpected URL %s", r.URL)
			return nil, nil
		}
	})
}

// TestGenerateVideoAsyncFlow covers the confirmed live shape: an async
// predictLongRunning submit, polling fetchPredictOperation until done, and a
// response.videos[]{mimeType,bytesBase64Encoded} payload.
func TestGenerateVideoAsyncFlow(t *testing.T) {
	fastPolls(t)
	want := []byte("\x00\x00\x00\x18ftypmp42realvideo")
	done := `{"name":"projects/p/locations/l/operations/op1","done":true,"response":{"@type":"x","videos":[{"mimeType":"video/mp4","bytesBase64Encoded":"` +
		base64.StdEncoding.EncodeToString(want) + `"}]}}`
	c := videoTestClient(videoRT(t, `{"done":false}`, done))

	vids, err := c.GenerateVideo(context.Background(), "gcp:veo-3.1-fast-generate-001", "a cat", nil, VideoOptions{})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if len(vids) != 1 || vids[0].MediaType != "video/mp4" || string(vids[0].Data) != string(want) {
		t.Errorf("got %+v, want one mp4 matching input", vids)
	}
}

// TestGenerateVideoSendsOptions asserts VideoOptions are marshalled into the
// predictLongRunning "parameters" object, and that a zero VideoOptions keeps
// the prior default (aspectRatio 16:9, no resolution/duration fields).
func TestGenerateVideoSendsOptions(t *testing.T) {
	fastPolls(t)
	done := `{"done":true,"response":{"videos":[{"mimeType":"video/mp4","bytesBase64Encoded":"` +
		base64.StdEncoding.EncodeToString([]byte("v")) + `"}]}}`
	var submit string
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, ":predictLongRunning") {
			b, _ := io.ReadAll(r.Body)
			submit = string(b)
			return resp(200, `{"name":"op1"}`), nil
		}
		return resp(200, done), nil
	})

	// Explicit options are all forwarded.
	c := videoTestClient(rt)
	if _, err := c.GenerateVideo(context.Background(), "veo", "x", nil,
		VideoOptions{AspectRatio: "9:16", Resolution: "1080p", DurationSeconds: 6}); err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	for _, want := range []string{`"aspectRatio":"9:16"`, `"resolution":"1080p"`, `"durationSeconds":6`} {
		if !strings.Contains(submit, want) {
			t.Errorf("submit body missing %s:\n%s", want, submit)
		}
	}

	// Zero options: default 16:9, and no resolution/duration keys at all.
	submit = ""
	if _, err := c.GenerateVideo(context.Background(), "veo", "x", nil, VideoOptions{}); err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if !strings.Contains(submit, `"aspectRatio":"16:9"`) {
		t.Errorf("default submit should keep aspectRatio 16:9:\n%s", submit)
	}
	if strings.Contains(submit, "resolution") || strings.Contains(submit, "durationSeconds") {
		t.Errorf("zero options should omit resolution/duration:\n%s", submit)
	}
}

// TestGenerateVideoURIForm: a uri (no inline bytes) surfaces as a URL.
func TestGenerateVideoURIForm(t *testing.T) {
	fastPolls(t)
	done := `{"done":true,"response":{"videos":[{"mimeType":"video/mp4","uri":"https://cdn.test/clip.mp4"}]}}`
	c := videoTestClient(videoRT(t, `{"done":false}`, done))
	vids, err := c.GenerateVideo(context.Background(), "veo-3.0-generate-001", "a dog", nil, VideoOptions{})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if len(vids) != 1 || vids[0].URL != "https://cdn.test/clip.mp4" {
		t.Errorf("got %+v, want one URL", vids)
	}
}

// TestGenerateVideoGeneratedSamplesFallback: the generativelanguage shape.
func TestGenerateVideoGeneratedSamplesFallback(t *testing.T) {
	fastPolls(t)
	raw := []byte("mp4bytes")
	done := `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"mimeType":"video/mp4","bytesBase64Encoded":"` +
		base64.StdEncoding.EncodeToString(raw) + `"}}]}}}`
	c := videoTestClient(videoRT(t, `{"done":false}`, done))
	vids, err := c.GenerateVideo(context.Background(), "veo", "x", nil, VideoOptions{})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if len(vids) != 1 || string(vids[0].Data) != string(raw) {
		t.Errorf("got %+v, want inline mp4 bytes", vids)
	}
}

func TestGenerateVideoContentFiltered(t *testing.T) {
	fastPolls(t)
	done := `{"done":true,"response":{"raiMediaFilteredCount":1,"videos":[]}}`
	c := videoTestClient(videoRT(t, `{"done":false}`, done))
	_, err := c.GenerateVideo(context.Background(), "veo", "x", nil, VideoOptions{})
	if err == nil || !strings.Contains(err.Error(), "content filter") {
		t.Errorf("want content-filter error, got %v", err)
	}
}

func TestGenerateVideoOperationError(t *testing.T) {
	fastPolls(t)
	done := `{"done":true,"error":{"code":9,"message":"quota exceeded"}}`
	c := videoTestClient(videoRT(t, `{"done":false}`, done))
	_, err := c.GenerateVideo(context.Background(), "veo", "x", nil, VideoOptions{})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("want operation error surfaced, got %v", err)
	}
}

func TestGenerateVideoErrors(t *testing.T) {
	// Empty prompt / model rejected before any network call.
	c := videoTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("should not hit network")
		return nil, nil
	}))
	if _, err := c.GenerateVideo(context.Background(), "", "x", nil, VideoOptions{}); err == nil {
		t.Error("want error for empty model")
	}
	if _, err := c.GenerateVideo(context.Background(), "m", "  ", nil, VideoOptions{}); err == nil {
		t.Error("want error for empty prompt")
	}

	// Non-200 on submit surfaces status + body (the Veo entitlement 400 case).
	c2 := videoTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(400, `{"message":"no accounts available"}`), nil
	}))
	if _, err := c2.GenerateVideo(context.Background(), "gcp:veo-3.0-generate-001", "x", nil, VideoOptions{}); err == nil ||
		!strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "no accounts available") {
		t.Errorf("want 400 body surfaced, got %v", err)
	}
}

// TestGenerateVideoUsesGeminiRoute: submit must hit the Gemini route's
// :predictLongRunning (NOT the OpenAI /chat/completions route), with the bare
// model name (no gcp: prefix).
func TestGenerateVideoUsesGeminiRoute(t *testing.T) {
	fastPolls(t)
	var submitURL string
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, ":predictLongRunning") {
			submitURL = r.URL.String()
			return resp(200, `{"name":"projects/p/operations/op1"}`), nil
		}
		return resp(200, `{"done":true,"response":{"videos":[{"mimeType":"video/mp4","bytesBase64Encoded":"YWJj"}]}}`), nil
	})
	c := videoTestClient(rt)
	if _, err := c.GenerateVideo(context.Background(), "gcp:veo-3.1-generate-001", "x", nil, VideoOptions{}); err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if !strings.Contains(submitURL, "/v1beta/models/veo-3.1-generate-001:predictLongRunning") {
		t.Errorf("submit URL = %q, want gemini route with bare model name", submitURL)
	}
	if strings.Contains(submitURL, "gcp:") || strings.Contains(submitURL, "/openai/") {
		t.Errorf("submit URL = %q must not carry gcp: prefix or hit the openai route", submitURL)
	}
}

// TestGenerateVideoCancel: a cancelled context stops polling promptly.
func TestGenerateVideoCancel(t *testing.T) {
	fastPolls(t)
	c := videoTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, ":predictLongRunning") {
			return resp(200, `{"name":"projects/p/operations/op1"}`), nil
		}
		return resp(200, `{"done":false}`), nil // never completes
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := c.GenerateVideo(ctx, "veo", "x", nil, VideoOptions{}); err == nil {
		t.Error("want timeout error for never-completing operation")
	}
}

func TestIsVideoGenModel(t *testing.T) {
	yes := []string{"gcp:veo-3.0-fast-generate-001", "gcp:veo-2.0-generate-001", "some-video-generation-model"}
	no := []string{"gcp:gemini-2.5-pro", "gcp:gemini-2.5-flash-image", "anthropic.claude-opus-4-8", ""}
	for _, id := range yes {
		if !isVideoGenModel(id) {
			t.Errorf("isVideoGenModel(%q) = false, want true", id)
		}
	}
	for _, id := range no {
		if isVideoGenModel(id) {
			t.Errorf("isVideoGenModel(%q) = true, want false", id)
		}
	}
}

// videoTestClient is newTestClient plus the Gemini API key GenerateVideo
// requires (video generation only exists on the native Gemini route).
func videoTestClient(rt http.RoundTripper) *Client {
	c := newTestClient(rt)
	c.cfg.GeminiAPIKey = "k"
	return c
}
