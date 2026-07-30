package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
)

// VideoGenerator produces videos from a text prompt via a video-output model.
// *llm.Client satisfies it (GenerateVideo on Floodgate's native Gemini route).
// Kept as an interface so the tools package stays easy to fake in tests.
type VideoGenerator interface {
	GenerateVideo(ctx context.Context, model, prompt string, refs []llm.ImageRef, opts llm.VideoOptions) ([]llm.GeneratedVideo, error)
}

// videoGenTimeout bounds a single generate_video call end-to-end (submit + poll).
const videoGenTimeout = 10 * time.Minute

// videoGenTool lets the model generate a video with a video-output model (a Veo
// model on Floodgate's native Gemini route). The clip is saved into the
// workspace and its path is returned as text (the model can't watch video); the
// bytes are surfaced to the UI separately (VideoProducer), never fed back to the
// model.
type videoGenTool struct {
	r     *Registry
	gen   VideoGenerator
	model string
}

// NewVideoGenTool builds the generate_video tool bound to a video-output model.
func NewVideoGenTool(r *Registry, gen VideoGenerator, model string) Tool {
	return &videoGenTool{r: r, gen: gen, model: model}
}

func (t *videoGenTool) Name() string { return "generate_video" }

func (t *videoGenTool) Description() string {
	return "Generate a short video with a video-output AI model (" + t.model +
		"). Provide a detailed 'prompt' describing the scene, motion, and style. " +
		"Optionally pass 'reference_images' (workspace-relative paths) to condition the video on existing images. " +
		"Set 'output_path' to save the clip into the workspace (e.g. \"assets/clip.mp4\"); otherwise a default path is used. " +
		"Video generation is slow (can take minutes) and billable. The tool returns the saved file path; the clip is shown in the web UI but not returned to you inline."
}

func (t *videoGenTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {"type": "string", "description": "Detailed description of the video to generate (scene, subjects, motion, camera, style)."},
    "output_path": {"type": "string", "description": "Optional workspace-relative path to save the generated video (e.g. \"assets/clip.mp4\"). If omitted, a default workspace path is used."},
    "reference_images": {"type": "array", "items": {"type": "string"}, "description": "Optional workspace-relative paths of input images to condition the video on."},
    "aspect_ratio": {"type": "string", "enum": ["16:9", "9:16"], "description": "Optional aspect ratio: \"16:9\" (landscape, default) or \"9:16\" (portrait)."},
    "resolution": {"type": "string", "description": "Optional output resolution, e.g. \"720p\" or \"1080p\". Support depends on the Veo model; omit to use the model default."},
    "duration_seconds": {"type": "integer", "description": "Optional clip length in seconds. Support/allowed range depends on the Veo model; omit to use the model default."}
  },
  "required": ["prompt"]
}`)
}

// Mutates: video generation is a slow, billable external call and writes a file
// into the workspace, so it goes through the approval gate.
func (t *videoGenTool) Mutates() bool { return true }

type videoGenInput struct {
	Prompt          string   `json:"prompt"`
	OutputPath      string   `json:"output_path"`
	ReferenceImages []string `json:"reference_images"`
	AspectRatio     string   `json:"aspect_ratio"`
	Resolution      string   `json:"resolution"`
	DurationSeconds int      `json:"duration_seconds"`
}

func (t *videoGenTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	text, _, err := t.ExecuteVideos(ctx, input)
	return text, err
}

func (t *videoGenTool) ExecuteVideos(ctx context.Context, input json.RawMessage) (string, []Video, error) {
	var in videoGenInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", nil, fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", nil, fmt.Errorf("prompt is required")
	}
	if ar := strings.TrimSpace(in.AspectRatio); ar != "" && ar != llm.VideoAspect16x9 && ar != llm.VideoAspect9x16 {
		return "", nil, fmt.Errorf("aspect_ratio must be %q or %q", llm.VideoAspect16x9, llm.VideoAspect9x16)
	}
	if in.DurationSeconds < 0 {
		return "", nil, fmt.Errorf("duration_seconds must be positive")
	}

	// Video generation is a long-running async operation (Veo polls for ~1 min on
	// the fast model, longer otherwise); give it a generous deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, videoGenTimeout)
	defer cancel()

	var refs []llm.ImageRef
	for _, p := range in.ReferenceImages {
		abs, err := t.r.resolve(p)
		if err != nil {
			return "", nil, fmt.Errorf("reference_images: %w", err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return "", nil, fmt.Errorf("read reference image %q: %w", p, err)
		}
		refs = append(refs, llm.ImageRef{MediaType: mediaTypeFor(abs), Data: data})
	}

	vids, err := t.gen.GenerateVideo(ctx, t.model, in.Prompt, refs, llm.VideoOptions{
		AspectRatio:     strings.TrimSpace(in.AspectRatio),
		Resolution:      strings.TrimSpace(in.Resolution),
		DurationSeconds: in.DurationSeconds,
	})
	if err != nil {
		return "", nil, err
	}
	if len(vids) == 0 {
		return "", nil, fmt.Errorf("video generation returned no video")
	}
	vid := vids[0] // first clip; models typically return one

	// Veo usually returns bytes inline; if it returns a URL instead, fetch the
	// bytes so we can save + display them (best effort).
	if len(vid.Data) == 0 && vid.URL != "" {
		if data, err := fetchVideoURL(ctx, t.r, vid.URL); err == nil {
			vid.Data = data
		} else {
			// Can't fetch — still report the URL so the caller isn't left empty.
			return fmt.Sprintf("Generated a video with %s, available at %s (could not download: %v).", t.model, vid.URL, err),
				[]Video{{MediaType: vid.MediaType, URL: vid.URL}}, nil
		}
	}
	if len(vid.Data) == 0 {
		return "", nil, fmt.Errorf("video generation returned no data")
	}

	outPath := strings.TrimSpace(in.OutputPath)
	if outPath == "" {
		outPath = "generated-video-" + time.Now().Format("20060102-150405") + extForVideo(vid.MediaType)
	}
	abs, err := t.r.resolve(outPath)
	if err != nil {
		return "", nil, fmt.Errorf("output_path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", nil, fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(abs, vid.Data, 0o644); err != nil {
		return "", nil, fmt.Errorf("write video: %w", err)
	}

	out := fmt.Sprintf("Generated a %s video (%d bytes) with %s, saved to %s.", vid.MediaType, len(vid.Data), t.model, outPath)
	toolVids := make([]Video, 0, len(vids))
	for _, g := range vids {
		toolVids = append(toolVids, Video{MediaType: g.MediaType, Data: g.Data, URL: g.URL})
	}
	return out, toolVids, nil
}

// fetchVideoURL downloads a video the model returned by link, capped so a huge
// clip can't exhaust memory.
func fetchVideoURL(ctx context.Context, r *Registry, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// extForVideo maps a video media type to a file extension for the default name.
func extForVideo(mediaType string) string {
	switch mediaType {
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ".mp4"
	}
}
