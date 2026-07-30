package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// ImageGenerator produces images from a text prompt via an image-output model.
// *llm.Client satisfies it (GenerateImage on the OpenAI-compatible route). Kept
// as an interface so the tools package doesn't hard-depend on the concrete
// client and stays easy to fake in tests.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, model, prompt string, refs []llm.ImageRef) ([]llm.GeneratedImage, error)
}

// imageGenTool lets the model generate (or edit) images with an image-output
// model — e.g. a Gemini "Flash Image" model on Floodgate's OpenAI route. The
// generated image is returned inline (so it shows in the transcript and the
// model can see the result) and, when output_path is given, written into the
// workspace.
type imageGenTool struct {
	r     *Registry
	gen   ImageGenerator
	model string
}

// NewImageGenTool builds the generate_image tool bound to an image-output model.
func NewImageGenTool(r *Registry, gen ImageGenerator, model string) Tool {
	return &imageGenTool{r: r, gen: gen, model: model}
}

func (t *imageGenTool) Name() string { return "generate_image" }

func (t *imageGenTool) Description() string {
	return "Generate or edit an image with an image-output AI model (" + t.model +
		"). Useful for creating placeholder assets, icons, UI mockups, or diagrams for the project. " +
		"Provide a detailed 'prompt'. Optionally pass 'reference_images' (workspace-relative paths) to edit/annotate existing images (e.g. refine a screenshot or sketch). " +
		"Set 'output_path' to save the result into the workspace; otherwise it is saved to a temporary file. The image is always returned inline so you can see it."
}

func (t *imageGenTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {"type": "string", "description": "Detailed description of the image to generate, or the edit to apply to the reference image(s)."},
    "output_path": {"type": "string", "description": "Optional workspace-relative path to save the generated image (e.g. \"assets/icon.png\"). If omitted, a temporary file is used."},
    "reference_images": {"type": "array", "items": {"type": "string"}, "description": "Optional workspace-relative paths of input images to edit or use as reference."}
  },
  "required": ["prompt"]
}`)
}

// Mutates: image generation is a billable external call and can write a file
// into the workspace (output_path), so it goes through the approval gate.
func (t *imageGenTool) Mutates() bool { return true }

type imageGenInput struct {
	Prompt          string   `json:"prompt"`
	OutputPath      string   `json:"output_path"`
	ReferenceImages []string `json:"reference_images"`
}

func (t *imageGenTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	text, _, err := t.ExecuteImages(ctx, input)
	return text, err
}

func (t *imageGenTool) ExecuteImages(ctx context.Context, input json.RawMessage) (string, []Image, error) {
	var in imageGenInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", nil, fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", nil, fmt.Errorf("prompt is required")
	}

	// Load any reference images from the workspace.
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

	imgs, err := t.gen.GenerateImage(ctx, t.model, in.Prompt, refs)
	if err != nil {
		return "", nil, err
	}
	img := imgs[0] // first image; models typically return one

	// Persist it: into the workspace if output_path is given, else a temp file.
	var savedPath string
	if strings.TrimSpace(in.OutputPath) != "" {
		abs, err := t.r.resolve(in.OutputPath)
		if err != nil {
			return "", nil, fmt.Errorf("output_path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", nil, fmt.Errorf("create output dir: %w", err)
		}
		if err := os.WriteFile(abs, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("write image: %w", err)
		}
		savedPath = in.OutputPath
	} else {
		f, err := os.CreateTemp("", "sqe-genimage-*"+extFor(img.MediaType))
		if err == nil {
			_, _ = f.Write(img.Data)
			_ = f.Close()
			savedPath = f.Name()
		}
	}

	out := fmt.Sprintf("Generated a %s image (%d bytes) with %s", img.MediaType, len(img.Data), t.model)
	if savedPath != "" {
		out += ", saved to " + savedPath
	}
	out += "."

	toolImgs := make([]Image, 0, len(imgs))
	for _, g := range imgs {
		toolImgs = append(toolImgs, Image{MediaType: g.MediaType, Data: g.Data})
	}
	return out, toolImgs, nil
}

// mediaTypeFor guesses an image media type from a file extension (for reference
// inputs). Defaults to image/png.
func mediaTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// extFor maps a media type back to a file extension for the temp file name.
func extFor(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
