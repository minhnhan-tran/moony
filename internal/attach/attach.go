// Package attach turns user-supplied files and clipboard images into the
// content blocks the model accepts. Images become vision blocks, PDFs become
// document blocks (read natively by Claude), and Office files (docx/xlsx/pptx)
// are text-extracted locally since no model API ingests them directly. iWork
// files (pages/numbers/key) are first converted to their Office equivalent via
// an office converter service (see iwork.go), then text-extracted the same way.
// It is shared by the terminal (@mentions, /attach, /paste) and the web UI (file
// picker, drag-drop, clipboard paste).
package attach

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// Kind classifies how an attachment is delivered to the model.
type Kind string

const (
	KindImage    Kind = "image"    // vision block (png/jpeg/gif/webp)
	KindDocument Kind = "document" // document block, PDF (read natively by Claude)
	KindText     Kind = "text"     // locally-extracted text (Office files)
)

// Size caps. Images/PDFs match the Anthropic API limits; extracted Office text
// is capped so a huge spreadsheet can't blow up the context.
const (
	MaxImageBytes   = 5 * 1024 * 1024  // 5 MB per image
	MaxPDFBytes     = 32 * 1024 * 1024 // 32 MB per PDF
	MaxOfficeBytes  = 30 * 1024 * 1024 // 30 MB source archive
	MaxExtractChars = 200 * 1024       // 200 KB of extracted text
)

// Attachment is one resolved file ready to attach to a turn.
type Attachment struct {
	Kind      Kind
	Name      string // display/file name
	MediaType string // image/png, application/pdf (image & document kinds)
	Data      []byte // raw bytes (image & document kinds)
	Text      string // extracted text (text kind)
}

// imageMediaTypes maps lowercase extensions to image media types.
var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// officeExts is the set of Office Open XML extensions we text-extract.
var officeExts = map[string]bool{
	".docx": true,
	".xlsx": true,
	".pptx": true,
}

// Attachable reports whether a path looks like something this package handles
// (image, PDF, Office, or iWork file). The terminal uses it to decide whether an
// @-mention should attach as a block rather than inline as text.
func Attachable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := imageMediaTypes[ext]; ok {
		return true
	}
	if _, ok := iworkExts[ext]; ok {
		return true
	}
	return ext == ".pdf" || ext == ".svg" || officeExts[ext]
}

// FromPath reads a file from disk and resolves it into an Attachment, applying
// size caps and (for Office files) text extraction.
func FromPath(path string) (Attachment, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Attachment{}, err
	}
	if info.IsDir() {
		return Attachment{}, fmt.Errorf("%s is a directory", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Attachment{}, err
	}
	return FromBytes(filepath.Base(path), "", data)
}

// FromBytes resolves already-read bytes into an Attachment. mediaType may be
// empty (it's then inferred from the name); the web passes the browser's type.
func FromBytes(name, mediaType string, data []byte) (Attachment, error) {
	if len(data) == 0 {
		return Attachment{}, fmt.Errorf("%s is empty", name)
	}
	ext := strings.ToLower(filepath.Ext(name))

	// SVG: it's XML text, and Claude's vision API can't ingest image/svg+xml as an
	// image — so deliver the markup as text (ideal for reading/editing/converting
	// it). Checked before the image branch since the browser reports it as an
	// image/* type.
	if ext == ".svg" || mediaType == "image/svg+xml" {
		if len(data) > MaxImageBytes {
			return Attachment{}, fmt.Errorf("%s is %s; SVG files must be under %s", name, humanSize(len(data)), humanSize(MaxImageBytes))
		}
		text := string(data)
		if strings.TrimSpace(text) == "" {
			return Attachment{}, fmt.Errorf("%s is empty", name)
		}
		if len(text) > MaxExtractChars {
			text = text[:MaxExtractChars] + "\n…[truncated]"
		}
		return Attachment{Kind: KindText, Name: name, Text: text}, nil
	}

	// Images.
	if mt := imageMediaType(ext, mediaType); mt != "" {
		if len(data) > MaxImageBytes {
			return Attachment{}, fmt.Errorf("%s is %s; images must be under %s", name, humanSize(len(data)), humanSize(MaxImageBytes))
		}
		return Attachment{Kind: KindImage, Name: name, MediaType: mt, Data: data}, nil
	}

	// PDFs.
	if ext == ".pdf" || mediaType == "application/pdf" {
		if len(data) > MaxPDFBytes {
			return Attachment{}, fmt.Errorf("%s is %s; PDFs must be under %s", name, humanSize(len(data)), humanSize(MaxPDFBytes))
		}
		return Attachment{Kind: KindDocument, Name: name, MediaType: "application/pdf", Data: data}, nil
	}

	// Office files: extract text.
	if officeExts[ext] {
		if len(data) > MaxOfficeBytes {
			return Attachment{}, fmt.Errorf("%s is %s; Office files must be under %s", name, humanSize(len(data)), humanSize(MaxOfficeBytes))
		}
		text, err := extractOffice(ext, data)
		if err != nil {
			return Attachment{}, fmt.Errorf("could not read %s: %w", name, err)
		}
		if strings.TrimSpace(text) == "" {
			return Attachment{}, fmt.Errorf("no extractable text in %s", name)
		}
		if len(text) > MaxExtractChars {
			text = text[:MaxExtractChars] + "\n…[truncated]"
		}
		return Attachment{Kind: KindText, Name: name, Text: text}, nil
	}

	// iWork files: convert to the matching Office format via the office
	// converter service, then text-extract the result. The display name keeps
	// the original iWork extension so the user recognizes their file.
	if officeExt, ok := iworkExts[ext]; ok {
		if len(data) > MaxOfficeBytes {
			return Attachment{}, fmt.Errorf("%s is %s; iWork files must be under %s", name, humanSize(len(data)), humanSize(MaxOfficeBytes))
		}
		office, err := convertIWork(name, data)
		if err != nil {
			return Attachment{}, fmt.Errorf("could not convert %s: %w", name, err)
		}
		text, err := extractOffice(officeExt, office)
		if err != nil {
			return Attachment{}, fmt.Errorf("could not read converted %s: %w", name, err)
		}
		if strings.TrimSpace(text) == "" {
			return Attachment{}, fmt.Errorf("no extractable text in %s", name)
		}
		if len(text) > MaxExtractChars {
			text = text[:MaxExtractChars] + "\n…[truncated]"
		}
		return Attachment{Kind: KindText, Name: name, Text: text}, nil
	}

	return Attachment{}, fmt.Errorf("unsupported attachment type %q (supported: images, PDF, docx/xlsx/pptx, pages/numbers/key)", ext)
}

// imageMediaType returns the image media type for an extension or browser-
// supplied type, or "" if it isn't a supported image.
func imageMediaType(ext, mediaType string) string {
	if mt, ok := imageMediaTypes[ext]; ok {
		return mt
	}
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mediaType
	}
	return ""
}

// Block converts the attachment into a model content block.
func (a Attachment) Block() llm.ContentBlock {
	switch a.Kind {
	case KindImage:
		return llm.ImageBlock(a.MediaType, base64.StdEncoding.EncodeToString(a.Data))
	case KindDocument:
		return llm.DocumentBlock(a.MediaType, base64.StdEncoding.EncodeToString(a.Data), a.Name)
	default: // KindText
		return llm.TextBlock("--- attached file: " + a.Name + " ---\n" + a.Text)
	}
}

// Label is a short human description for the UI (transcript chips, logs).
func (a Attachment) Label() string {
	switch a.Kind {
	case KindImage:
		return fmt.Sprintf("image %s (%s)", a.Name, humanSize(len(a.Data)))
	case KindDocument:
		return fmt.Sprintf("PDF %s (%s)", a.Name, humanSize(len(a.Data)))
	default:
		return fmt.Sprintf("file %s (%s of text)", a.Name, humanSize(len(a.Text)))
	}
}

func humanSize(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
