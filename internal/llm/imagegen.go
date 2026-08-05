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

	"github.com/pysty1995/moony/internal/provider"
)

// ImageRef is a reference image handed to GenerateImage as input — e.g. a
// screenshot or mockup to edit/annotate. Gemini's image models accept input
// images alongside the text prompt on the same chat request.
type ImageRef struct {
	MediaType string // e.g. "image/png"
	Data      []byte // raw image bytes
}

// GeneratedImage is a single image returned by an image-output model.
type GeneratedImage struct {
	MediaType string
	Data      []byte
}

// GenerateImage asks an image-output model (e.g. a Gemini "Flash Image" model on
// Floodgate's OpenAI route) to produce one or more images from a text prompt,
// optionally editing/using the given reference images as input. It's a one-shot,
// non-streaming Chat Completions call independent of the session's chat model
// and reasoning settings.
//
// Confirmed against live Floodgate (2026-07): Gemini image models come back as
// choices[0].message.audio = {"data": "<raw base64, NOT a data: URL>",
// "extra_content":{"google":{"mime_type":"image/png"}}} — the proxy reuses the
// OpenAI "audio" response modality slot for generated images, with content
// left null. We also defensively handle the documented-but-unseen shapes some
// Gemini/OpenAI-compatible proxies use instead: a non-standard "images" array
// of data-URL image_url parts, or an image_url content part in a content array.
//
// It reuses the same OpenAI-compatible base URL + auth as the chat endpoint, so
// it works on whichever backend the session is configured for (provider must be
// an OpenAI-compatible route — Gemini, OpenAI, OpenRouter, …).
func (c *Client) GenerateImage(ctx context.Context, model, prompt string, refs []ImageRef) ([]GeneratedImage, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("image model not configured")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("empty image prompt")
	}

	// One user turn: the text prompt followed by any reference images.
	parts := []oaiContentPart{{Type: "text", Text: prompt}}
	for _, r := range refs {
		if len(r.Data) == 0 {
			continue
		}
		parts = append(parts, oaiContentPart{
			Type:     "image_url",
			ImageURL: &oaiImageURL{URL: dataURI(r.MediaType, base64.StdEncoding.EncodeToString(r.Data))},
		})
	}
	reqBody, err := json.Marshal(oaiRequest{
		Model:    model,
		Messages: []oaiMessage{{Role: "user", Content: parts}},
		Stream:   false,
	})
	if err != nil {
		return nil, err
	}

	// Route to the image-specific backend when one is configured. Image generation
	// needs an OpenAI-compatible chat route that returns image parts, which few
	// backends have — so a session on a local or text-only provider would otherwise
	// have no way to generate images at all.
	target := provider.Normalize(c.cfg.LLMProvider)
	if ov := c.cfg.ImageProviderFor(); ov != "" {
		target = ov
	}
	rb := c.remoteBackendFor(target)
	if strings.TrimSpace(rb.baseURL) == "" {
		return nil, fmt.Errorf("image generation needs an OpenAI-compatible provider; %q has no base URL", target)
	}
	if rb.apiKey == "" && !rb.noKeyOK {
		return nil, fmt.Errorf("image generation on %q needs an API key — set %s", target, rb.keyEnv)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(rb.baseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	rb.setAuth(req)
	for k, v := range rb.extra {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image generation request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // images can be large
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image generation %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				// content is a plain string for text-only replies, or an array of
				// parts (some of type "image_url") when the model returns images.
				Content json.RawMessage `json:"content"`
				// images is a non-standard field some proxies use to carry generated
				// images as data-URL image_url parts.
				Images []oaiRespImagePart `json:"images"`
				// audio is where Floodgate actually returns Gemini's generated image:
				// raw base64 (no data: prefix) + the real mime type nested underneath.
				Audio *oaiRespAudio `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse image response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("image generation returned no choices")
	}

	var out []GeneratedImage
	msg := parsed.Choices[0].Message
	if msg.Audio != nil && msg.Audio.Data != "" {
		if g, ok := decodeBase64Image(msg.Audio.Data, msg.Audio.ExtraContent.Google.MimeType); ok {
			out = append(out, g)
		}
	}
	for _, im := range msg.Images {
		if g, ok := decodeDataURL(im.ImageURL.URL); ok {
			out = append(out, g)
		}
	}
	// Also scan content parts, if content came back as an array.
	if len(msg.Content) > 0 && msg.Content[0] == '[' {
		var cparts []oaiRespImagePart
		if json.Unmarshal(msg.Content, &cparts) == nil {
			for _, p := range cparts {
				if p.Type == "image_url" {
					if g, ok := decodeDataURL(p.ImageURL.URL); ok {
						out = append(out, g)
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model %q returned no image (is it an image-output model?)", model)
	}
	return out, nil
}

// oaiRespImagePart is an image the model returns, either in the message.images
// array or as an image_url content part.
type oaiRespImagePart struct {
	Type     string      `json:"type"`
	ImageURL oaiImageURL `json:"image_url"`
}

// oaiRespAudio is the shape Floodgate actually uses to carry a generated image
// on Gemini image models: message.audio.data is raw base64 (no "data:" prefix),
// with the real mime type nested under extra_content.google.mime_type.
type oaiRespAudio struct {
	Data         string `json:"data"`
	ExtraContent struct {
		Google struct {
			MimeType string `json:"mime_type"`
		} `json:"google"`
	} `json:"extra_content"`
}

// decodeBase64Image decodes a raw (non data-URL) base64 payload plus an
// explicit mime type, defaulting to image/png when the type is missing.
func decodeBase64Image(payload, mediaType string) (GeneratedImage, bool) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return GeneratedImage{}, false
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return GeneratedImage{MediaType: mediaType, Data: data}, true
}

// decodeDataURL decodes a "data:<mediatype>;base64,<data>" URL into raw bytes.
func decodeDataURL(s string) (GeneratedImage, bool) {
	if !strings.HasPrefix(s, "data:") {
		return GeneratedImage{}, false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return GeneratedImage{}, false
	}
	meta, payload := s[len("data:"):comma], s[comma+1:]
	mediaType := meta
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		mediaType = meta[:i] // strip ";base64"
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return GeneratedImage{}, false
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return GeneratedImage{MediaType: mediaType, Data: data}, true
}
