package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// TestGenerateImageParsesImagesField covers an image-output model returning a
// generated image in the non-standard "images" field (Gemini-style) on the
// OpenAI-compatible route.
func TestGenerateImageParsesImagesField(t *testing.T) {
	want := []byte("\x89PNG\r\n\x1a\nfake")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(want)
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"here you go","images":[{"type":"image_url","image_url":{"url":%q}}]}}]}`, dataURL)

	var gotBody string
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return resp(200, body), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	imgs, err := c.GenerateImage(context.Background(), "gemini-3.1-flash-image", "a cat", nil)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" || string(imgs[0].Data) != string(want) {
		t.Errorf("got %d images, first=%+v; want one PNG matching input", len(imgs), imgs[0])
	}
	// The prompt and model must reach the request; stream must be false.
	if !strings.Contains(gotBody, "a cat") || !strings.Contains(gotBody, "gemini-3.1-flash-image") || !strings.Contains(gotBody, `"stream":false`) {
		t.Errorf("request body missing prompt/model/stream=false: %s", gotBody)
	}
}

// TestGenerateImageParsesContentParts covers the variant where the image comes
// back as an image_url part inside the message.content array.
func TestGenerateImageParsesContentParts(t *testing.T) {
	want := []byte("JPEGDATA")
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(want)
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":%q}}]}}]}`, dataURL)

	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, body), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	imgs, err := c.GenerateImage(context.Background(), "m", "x", nil)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(imgs) != 1 || imgs[0].MediaType != "image/jpeg" || string(imgs[0].Data) != string(want) {
		t.Errorf("content-part image = %+v, want JPEG", imgs)
	}
}

func TestGenerateImageErrors(t *testing.T) {
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, `{"choices":[{"message":{"content":"no image here"}}]}`), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	// No image in response → error.
	if _, err := c.GenerateImage(context.Background(), "m", "x", nil); err == nil {
		t.Error("expected error when the model returns no image")
	}
	// Empty model / prompt rejected without a network call.
	if _, err := c.GenerateImage(context.Background(), "", "x", nil); err == nil {
		t.Error("expected error for empty model")
	}
	if _, err := c.GenerateImage(context.Background(), "m", "  ", nil); err == nil {
		t.Error("expected error for empty prompt")
	}
}

func TestDecodeDataURL(t *testing.T) {
	raw := []byte("hello")
	g, ok := decodeDataURL("data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw))
	if !ok || g.MediaType != "image/webp" || string(g.Data) != "hello" {
		t.Errorf("decodeDataURL = %+v, ok=%v", g, ok)
	}
	if _, ok := decodeDataURL("https://example.com/x.png"); ok {
		t.Error("non-data URL should not decode")
	}
	if _, ok := decodeDataURL("data:image/png;base64,!!!not-base64!!!"); ok {
		t.Error("invalid base64 should not decode")
	}
}

// TestGenerateImageParsesAudioField covers the shape actually returned by
// Floodgate for Gemini image models (confirmed live): the generated image
// comes back as message.audio.data (raw base64, no "data:" prefix) with the
// real mime type nested under extra_content.google.mime_type, and content is
// null. Before this was handled, GenerateImage errored with "returned no
// image" against every real image-output model.
func TestGenerateImageParsesAudioField(t *testing.T) {
	want := []byte("\x89PNG\r\n\x1a\nfake")
	b64 := base64.StdEncoding.EncodeToString(want)
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"audio":{"data":%q,"extra_content":{"google":{"mime_type":"image/png"}}}}}]}`, b64)

	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, body), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	imgs, err := c.GenerateImage(context.Background(), "gcp:gemini-2.5-flash-image", "a red circle", nil)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" || string(imgs[0].Data) != string(want) {
		t.Errorf("got %+v, want one PNG matching input", imgs)
	}
}

// TestGenerateImageAudioFieldDefaultsMimeType covers the audio.data payload
// arriving without extra_content (defensive: default to image/png).
func TestGenerateImageAudioFieldDefaultsMimeType(t *testing.T) {
	want := []byte("rawbytes")
	body := fmt.Sprintf(`{"choices":[{"message":{"content":null,"audio":{"data":%q}}}]}`, base64.StdEncoding.EncodeToString(want))
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, body), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	imgs, err := c.GenerateImage(context.Background(), "m", "x", nil)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" {
		t.Errorf("got %+v, want default image/png", imgs)
	}
}

func TestIsImageGenModel(t *testing.T) {
	yes := []string{"gcp:gemini-3.1-flash-image", "gcp:gemini-2.5-flash-image", "imagen-3.0-generate", "gcp:image-generation-x"}
	no := []string{"gcp:gemini-2.5-pro", "anthropic.claude-opus-4-8", "gcp:gemini-2.5-flash-tts", "text-to-speech-1", "gcp:veo-3.0-fast-generate-001", ""}
	for _, id := range yes {
		if !isImageGenModel(id) {
			t.Errorf("isImageGenModel(%q) = false, want true", id)
		}
	}
	for _, id := range no {
		if isImageGenModel(id) {
			t.Errorf("isImageGenModel(%q) = true, want false", id)
		}
	}
}

// TestIsNonChatModelExcludesVideo: Veo (and any video-generation) models must be
// kept out of the chat/Fast/Self-review model pickers — they can't chat. This
// regressed silently because the filter only considered "gemini"-family ids.
func TestIsNonChatModelExcludesVideo(t *testing.T) {
	video := []string{
		"gcp:veo-3.0-fast-generate-001", "gcp:veo-3.0-generate-001",
		"gcp:veo-3.1-fast-generate-001", "gcp:veo-3.1-generate-001",
		"gcp:veo-2.0-generate-001",
	}
	for _, id := range video {
		if !isNonChatModel(id) {
			t.Errorf("isNonChatModel(%q) = false, want true (video models aren't chat models)", id)
		}
		if isImageGenModel(id) {
			t.Errorf("isImageGenModel(%q) = true, want false (video != image)", id)
		}
	}
	// Real chat models must still pass through.
	for _, id := range []string{"gcp:gemini-2.5-pro", "gcp:gemini-3.1-flash-lite", "anthropic.claude-sonnet-4-6"} {
		if isNonChatModel(id) {
			t.Errorf("isNonChatModel(%q) = true, want false", id)
		}
	}
}

// TestListImageModelsFallback: when the OpenAI /models fetch yields no image
// models, the curated KnownImageModels fallback is returned.
func TestListImageModelsFallback(t *testing.T) {
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		// Only chat models, no image models.
		return resp(200, `{"data":[{"id":"gcp:gemini-2.5-pro"}]}`), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"
	got, _ := c.ListImageModels(context.Background())
	if len(got) != len(config.KnownImageModels) {
		t.Errorf("fallback list = %d models, want %d (KnownImageModels)", len(got), len(config.KnownImageModels))
	}

	// When the endpoint DOES return an image model, it's kept (no fallback).
	c2 := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, `{"data":[{"id":"gcp:gemini-2.5-pro"},{"id":"gcp:gemini-3.1-flash-image"}]}`), nil
	}))
	c2.cfg.LLMProvider = provider.OpenAI
	c2.cfg.OpenAIAPIKey = "k"
	c2.cfg.OpenAIBaseURL = "https://openai.test/v1"
	got2, _ := c2.ListImageModels(context.Background())
	if len(got2) != 1 || got2[0].ID != "gcp:gemini-3.1-flash-image" {
		t.Errorf("live image models = %+v, want just the flash-image model", got2)
	}
}

func TestHumanizeModelID(t *testing.T) {
	cases := map[string]string{
		"gcp:gemini-3.1-flash-lite-preview": "Gemini 3.1 Flash Lite Preview",
		"gcp:gemini-2.5-pro":                "Gemini 2.5 Pro",
		"gcp:gemini-3-flash-preview":        "Gemini 3 Flash Preview",
		"gemini-2.5-flash":                  "Gemini 2.5 Flash",
	}
	for in, want := range cases {
		if got := humanizeModelID(in); got != want {
			t.Errorf("humanizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKnownModelNamePrefersCatalog(t *testing.T) {
	// Curated catalog name wins over humanization.
	if got := knownModelName("gcp:gemini-3.1-pro-preview"); got != "Gemini 3.1 Pro preview" {
		t.Errorf("knownModelName(curated) = %q, want curated name", got)
	}
	// Unknown id → humanized, never blank.
	if got := knownModelName("gcp:gemini-9.9-flash"); got != "Gemini 9.9 Flash" {
		t.Errorf("knownModelName(unknown) = %q, want humanized", got)
	}
}

// The image route must honour ImageProvider: a session on a local/text-only backend
// otherwise has no way to generate images at all, since few providers serve an
// image-capable chat route.
func TestGenerateImageRoutesToImageProvider(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"ok","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]}}]}`

	var gotURL, gotAuth string
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		return resp(200, body), nil
	}))
	// Session is on a local backend with no key and no image capability…
	c.cfg.LLMProvider = provider.Ollama
	c.cfg.OllamaBaseURL = "http://localhost:11434/v1"
	// …while images are routed to a backend that can serve them.
	c.cfg.ImageProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "sk-image"
	c.cfg.OpenAIBaseURL = "https://openai.test/v1"

	if _, err := c.GenerateImage(context.Background(), "gemini-3.1-flash-image", "a cat", nil); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if !strings.Contains(gotURL, "openai.test") {
		t.Errorf("request went to %q, want the image provider's base URL", gotURL)
	}
	if !strings.Contains(gotAuth, "sk-image") {
		t.Errorf("Authorization = %q, want the image provider's key", gotAuth)
	}
}

// With no override the existing behaviour must be untouched.
func TestGenerateImageWithoutOverrideUsesSessionProvider(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"ok","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]}}]}`
	var gotURL string
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return resp(200, body), nil
	}))
	c.cfg.LLMProvider = provider.OpenAI
	c.cfg.OpenAIAPIKey = "k"
	c.cfg.OpenAIBaseURL = "https://session.test/v1"
	c.cfg.ImageProvider = "" // inherit

	if _, err := c.GenerateImage(context.Background(), "m", "a cat", nil); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if !strings.Contains(gotURL, "session.test") {
		t.Errorf("request went to %q, want the session provider", gotURL)
	}
}

// A misconfigured override must name the provider it tried, or the user has no way to
// tell which of the two backends is missing a key.
func TestGenerateImageOverrideErrorNamesProvider(t *testing.T) {
	c := newTestClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no request should be made when the target has no credentials")
		return nil, nil
	}))
	c.cfg.LLMProvider = provider.Ollama
	c.cfg.OllamaBaseURL = "http://localhost:11434/v1"
	c.cfg.ImageProvider = provider.OpenAI // no key set

	_, err := c.GenerateImage(context.Background(), "m", "a cat", nil)
	if err == nil {
		t.Fatal("expected an error for a keyless image provider")
	}
	if !strings.Contains(err.Error(), provider.OpenAI) {
		t.Errorf("error must name the attempted provider, got %q", err)
	}
}
