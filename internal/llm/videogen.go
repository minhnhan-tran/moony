package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// GeneratedVideo is a single video returned by a video-output model. Exactly one
// of Data (inline bytes) or URL (a remote link the caller must fetch) is set.
type GeneratedVideo struct {
	MediaType string // e.g. "video/mp4"
	Data      []byte // raw video bytes, when returned inline (base64)
	URL       string // a remote URL, when the model returns a link instead of bytes
}

// videoPollInterval is how often GenerateVideo polls the long-running operation.
var videoPollInterval = 8 * time.Second

// VideoAspect16x9 and VideoAspect9x16 are the two aspect ratios Veo documents
// support (landscape and portrait). VideoOptions.AspectRatio isn't hard-limited
// to these here — a newer/other model may accept more — but the tool layer (see
// internal/tools/video_gen.go) only offers these two, since they're the ones
// verified to work across the Veo model family.
const (
	VideoAspect16x9 = "16:9"
	VideoAspect9x16 = "9:16"
)

// VideoOptions carries optional Veo generation parameters beyond the prompt and
// reference image. Zero values omit the corresponding "parameters" field and
// let Veo apply its own default — for AspectRatio specifically, that default is
// 16:9 (the hardcoded behavior before this type existed), so an empty
// VideoOptions reproduces the exact prior request.
type VideoOptions struct {
	AspectRatio     string // e.g. "16:9", "9:16"; "" -> Veo's own default (16:9)
	Resolution      string // e.g. "720p", "1080p"; "" omits the field (model default)
	DurationSeconds int    // seconds; 0 omits the field (model default)
}

// GenerateVideo generates a video from a text prompt (optionally conditioned on a
// reference image) using a Veo model on Floodgate's native Google GenAI (Gemini)
// route. Verified live against gcp:veo-3.1-fast-generate-001.
//
// Veo is NOT served on the OpenAI-compatible /chat/completions route (that
// returns 400 "no accounts available ... API: openai"); it's an async
// long-running operation on the Gemini route:
//
//	POST {gemini}/models/{model}:predictLongRunning
//	     {"instances":[{"prompt":"..."}],"parameters":{"aspectRatio":"16:9"}}
//	  -> {"name":"projects/.../operations/..."}
//	POST {gemini}/models/{model}:fetchPredictOperation  {"operationName":"..."}
//	  -> {"done":false}  ... then
//	     {"done":true,"response":{"videos":[{"mimeType":"video/mp4","bytesBase64Encoded":"..."}]}}
//
// opts threads optional Veo "parameters" (aspect ratio, resolution, duration)
// through; a zero VideoOptions keeps the prior default request (aspectRatio
// 16:9, nothing else). It blocks (polling every videoPollInterval) until the
// operation completes or ctx is cancelled, so callers should pass a generous
// deadline (video gen takes ~1 min for the fast model, longer otherwise).
func (c *Client) GenerateVideo(ctx context.Context, model, prompt string, refs []ImageRef, opts VideoOptions) ([]GeneratedVideo, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("video model not configured")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("empty video prompt")
	}
	// The Gemini route uses bare model names; our config/UI carry the "gcp:" prefix.
	m := strings.TrimPrefix(strings.TrimSpace(model), "gcp:")

	instance := map[string]any{"prompt": prompt}
	if len(refs) > 0 && len(refs[0].Data) > 0 { // image-to-video (first ref)
		instance["image"] = map[string]any{
			"bytesBase64Encoded": base64.StdEncoding.EncodeToString(refs[0].Data),
			"mimeType":           refs[0].MediaType,
		}
	}
	aspectRatio := strings.TrimSpace(opts.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = VideoAspect16x9 // unchanged default behavior
	}
	params := map[string]any{"aspectRatio": aspectRatio}
	if r := strings.TrimSpace(opts.Resolution); r != "" {
		params["resolution"] = r
	}
	if opts.DurationSeconds > 0 {
		params["durationSeconds"] = opts.DurationSeconds
	}
	reqBody, err := json.Marshal(map[string]any{
		"instances":  []any{instance},
		"parameters": params,
	})
	if err != nil {
		return nil, err
	}

	base := c.geminiBase()
	start, err := c.postGemini(ctx, "video generation", base+"/models/"+m+":predictLongRunning", reqBody)
	if err != nil {
		return nil, err
	}
	opName, _ := start["name"].(string)
	if opName == "" {
		return nil, fmt.Errorf("video generation did not return an operation name: %s", truncateJSON(start))
	}

	pollBody, _ := json.Marshal(map[string]string{"operationName": opName})
	fetchURL := base + "/models/" + m + ":fetchPredictOperation"
	for {
		// Managed timer, not time.After: this loop polls for minutes, so leaving a
		// live timer behind on every cancelled iteration is a real leak.
		timer := time.NewTimer(videoPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("video generation timed out/cancelled after starting (operation %s): %w", opName, ctx.Err())
		case <-timer.C:
		}
		op, err := c.postGemini(ctx, "video generation", fetchURL, pollBody)
		if err != nil {
			return nil, err
		}
		if opErr := operationError(op); opErr != "" {
			return nil, fmt.Errorf("video generation failed: %s", opErr)
		}
		if done, _ := op["done"].(bool); done {
			return parseVeoOperation(op)
		}
	}
}

// postGemini POSTs a JSON body to a Gemini-route URL with Floodgate auth and
// returns the decoded response object.
// postGemini POSTs a JSON body to a Floodgate native-Gemini-route endpoint and
// decodes the JSON response. op labels the operation ("video generation",
// "embeddings", …) so errors name the correct caller (this helper is shared by
// GenerateVideo and Embed).
func (c *Client) postGemini(ctx context.Context, op, url string, body []byte) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.setGeminiAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", op, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20)) // videos can be large
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", op, resp.Status, strings.TrimSpace(string(raw)))
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s response: %w", op, err)
	}
	return m, nil
}

// operationError extracts a long-running-operation error message, if present.
func operationError(op map[string]any) string {
	e, ok := op["error"].(map[string]any)
	if !ok {
		return ""
	}
	if msg, ok := e["message"].(string); ok && msg != "" {
		return msg
	}
	return truncateJSON(e)
}

// parseVeoOperation extracts videos from a completed Veo operation. Confirmed
// shape: response.videos[] = {mimeType, bytesBase64Encoded}. Also tolerates a
// URI form and the generativelanguage generatedSamples shape as fallbacks.
func parseVeoOperation(op map[string]any) ([]GeneratedVideo, error) {
	resp, _ := op["response"].(map[string]any)
	if resp == nil {
		return nil, fmt.Errorf("completed video operation had no response payload")
	}

	var out []GeneratedVideo
	collect := func(mimeType, b64, uri string) {
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		if b64 != "" {
			if data, err := base64.StdEncoding.DecodeString(b64); err == nil && len(data) > 0 {
				out = append(out, GeneratedVideo{MediaType: mimeType, Data: data})
				return
			}
		}
		if uri != "" {
			out = append(out, GeneratedVideo{MediaType: mimeType, URL: uri})
		}
	}

	// Confirmed: response.videos[] {mimeType, bytesBase64Encoded, [uri]}
	for _, v := range toSlice(resp["videos"]) {
		vm, _ := v.(map[string]any)
		collect(str(vm["mimeType"]), str(vm["bytesBase64Encoded"]), str(vm["uri"]))
	}
	// Fallback: response.generateVideoResponse.generatedSamples[].video{uri|bytesBase64Encoded}
	if gvr, ok := resp["generateVideoResponse"].(map[string]any); ok {
		for _, s := range toSlice(gvr["generatedSamples"]) {
			sm, _ := s.(map[string]any)
			vid, _ := sm["video"].(map[string]any)
			if vid != nil {
				collect(str(vid["mimeType"]), str(vid["bytesBase64Encoded"]), str(vid["uri"]))
			}
		}
	}

	if len(out) == 0 {
		if n, ok := resp["raiMediaFilteredCount"].(float64); ok && n > 0 {
			return nil, fmt.Errorf("video was blocked by content filters (raiMediaFilteredCount=%d)", int(n))
		}
		return nil, fmt.Errorf("completed video operation returned no video (unrecognized shape: %s)", truncateJSON(resp))
	}
	return out, nil
}

func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
func str(v any) string {
	s, _ := v.(string)
	return s
}
func truncateJSON(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// isVideoGenModel reports whether a model id is a video-generation model (e.g.
// Veo). Mirrors isImageGenModel; used by ListVideoModels and to keep these out
// of the chat/image pickers.
func isVideoGenModel(id string) bool {
	x := strings.ToLower(id)
	for _, marker := range []string{"veo", "video-generation", "-video", "video-"} {
		if strings.Contains(x, marker) {
			return true
		}
	}
	return false
}

// ListVideoModels lists Veo video-generation models from the native Gemini
// route (they are absent from the OpenAI catalog). Models are identified
// by the predictLongRunning action or a veo/video name; ids are returned with
// the "gcp:" prefix to match config.KnownVideoModels and the picker convention.
// Falls back to the curated config.KnownVideoModels list on error/none.
func (c *Client) ListVideoModels(ctx context.Context) ([]config.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.geminiBase()+"/models?pageSize=1000", nil)
	if err != nil {
		return config.KnownVideoModels, err
	}
	if err := c.setGeminiAuth(req); err != nil {
		return config.KnownVideoModels, err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return config.KnownVideoModels, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return config.KnownVideoModels, fmt.Errorf("list gemini models %s", resp.Status)
	}
	var parsed struct {
		Models []struct {
			Name             string   `json:"name"`
			DisplayName      string   `json:"displayName"`
			SupportedActions []string `json:"supportedActions"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return config.KnownVideoModels, err
	}
	var out []config.ModelInfo
	for _, m := range parsed.Models {
		bare := strings.TrimPrefix(m.Name, "models/")
		if !isVideoGenModel(bare) {
			continue
		}
		id := "gcp:" + bare
		name := m.DisplayName
		if name == "" {
			name = knownModelName(id)
		}
		out = append(out, config.ModelInfo{ID: id, Name: name})
	}
	if len(out) == 0 {
		return config.KnownVideoModels, fmt.Errorf("no video models available")
	}
	return out, nil
}

// geminiBase returns the NATIVE Gemini API base (…/v1beta), not the
// OpenAI-compatibility shim: Veo video generation is only exposed there
// (models/…:predictLongRunning). Derived from MN_GEMINI_BASE_URL when set so a
// proxy/gateway is honored.
func (c *Client) geminiBase() string {
	base := strings.TrimSpace(c.cfg.GeminiBaseURL)
	if base == "" {
		return "https://generativelanguage.googleapis.com/v1beta"
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/openai")
	return base
}

// setGeminiAuth applies the Gemini API key. Google accepts either the
// x-goog-api-key header or a bearer token; the header form also works through
// most gateways.
func (c *Client) setGeminiAuth(req *http.Request) error {
	key := strings.TrimSpace(c.cfg.GeminiAPIKey)
	if key == "" {
		return fmt.Errorf("video generation needs a Gemini API key — set MN_GEMINI_API_KEY")
	}
	req.Header.Set("x-goog-api-key", key)
	return nil
}
