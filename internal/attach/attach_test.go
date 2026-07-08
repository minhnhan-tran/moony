package attach

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

// a 1x1 PNG.
var onePxPNG = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	data, _ := base64.StdEncoding.DecodeString(b64)
	return data
}()

func TestAttachable(t *testing.T) {
	yes := []string{"a.png", "b.JPG", "c.jpeg", "d.gif", "e.webp", "f.pdf", "g.docx", "h.xlsx", "i.pptx", "/abs/path/x.PNG", "deck.key", "notes.pages", "budget.NUMBERS"}
	for _, p := range yes {
		if !Attachable(p) {
			t.Errorf("Attachable(%q) = false, want true", p)
		}
	}
	no := []string{"a.go", "b.txt", "c.md", "noext", "d.tar.gz"}
	for _, p := range no {
		if Attachable(p) {
			t.Errorf("Attachable(%q) = true, want false", p)
		}
	}
}

func TestFromBytesImage(t *testing.T) {
	a, err := FromBytes("cat.png", "", onePxPNG)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindImage || a.MediaType != "image/png" {
		t.Fatalf("unexpected: %+v", a)
	}
	blk := a.Block()
	if blk.Type != llm.BlockImage || blk.Source == nil || blk.Source.MediaType != "image/png" {
		t.Fatalf("bad image block: %+v", blk)
	}
	if _, err := base64.StdEncoding.DecodeString(blk.Source.Data); err != nil {
		t.Fatalf("image data not valid base64: %v", err)
	}
}

func TestFromBytesImageByMediaType(t *testing.T) {
	// No image extension, but the browser-supplied media type identifies it.
	a, err := FromBytes("clipboard", "image/png", onePxPNG)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindImage {
		t.Fatalf("want image kind, got %v", a.Kind)
	}
}

func TestFromBytesPDF(t *testing.T) {
	a, err := FromBytes("doc.pdf", "", []byte("%PDF-1.4 fake"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindDocument || a.MediaType != "application/pdf" {
		t.Fatalf("unexpected: %+v", a)
	}
	blk := a.Block()
	if blk.Type != llm.BlockDocument || blk.Title != "doc.pdf" {
		t.Fatalf("bad document block: %+v", blk)
	}
}

func TestFromBytesSVGasText(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`)
	// Reported as an image/* type by the browser, but must be delivered as text.
	a, err := FromBytes("logo.svg", "image/svg+xml", svg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindText {
		t.Fatalf("want text kind for SVG, got %v", a.Kind)
	}
	if !strings.Contains(a.Text, "<rect") {
		t.Fatalf("SVG markup not carried as text: %q", a.Text)
	}
	blk := a.Block()
	if blk.Type != llm.BlockText {
		t.Fatalf("SVG should marshal to a text block, got %v", blk.Type)
	}
	// Also works when only the extension identifies it.
	if a2, err := FromBytes("logo.svg", "", svg); err != nil || a2.Kind != KindText {
		t.Fatalf("by-extension SVG failed: kind=%v err=%v", a2.Kind, err)
	}
}

func TestFromBytesImageTooLarge(t *testing.T) {
	big := make([]byte, MaxImageBytes+1)
	if _, err := FromBytes("big.png", "image/png", big); err == nil {
		t.Fatal("expected size error")
	}
}

func TestFromBytesUnsupported(t *testing.T) {
	if _, err := FromBytes("notes.txt", "text/plain", []byte("hi")); err == nil {
		t.Fatal("expected unsupported error for .txt")
	}
}

func TestFromBytesEmpty(t *testing.T) {
	if _, err := FromBytes("x.png", "image/png", nil); err == nil {
		t.Fatal("expected error for empty data")
	}
}

// makeDocx builds a minimal .docx (a zip with word/document.xml) containing the
// given runs of text.
func makeDocx(t *testing.T, runs ...string) []byte {
	t.Helper()
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="x"><w:body>`)
	for _, r := range runs {
		body.WriteString("<w:p><w:r><w:t>" + r + "</w:t></w:r></w:p>")
	}
	body.WriteString(`</w:body></w:document>`)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("word/document.xml")
	f.Write([]byte(body.String()))
	zw.Close()
	return buf.Bytes()
}

func TestFromBytesDocxExtractsText(t *testing.T) {
	data := makeDocx(t, "Hello world", "Second paragraph")
	a, err := FromBytes("report.docx", "", data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindText {
		t.Fatalf("want text kind, got %v", a.Kind)
	}
	if !strings.Contains(a.Text, "Hello world") || !strings.Contains(a.Text, "Second paragraph") {
		t.Fatalf("extracted text missing content: %q", a.Text)
	}
	blk := a.Block()
	if blk.Type != llm.BlockText || !strings.Contains(blk.Text, "report.docx") {
		t.Fatalf("text block should label the file: %q", blk.Text)
	}
}

func TestFromBytesDocxNoText(t *testing.T) {
	data := makeDocx(t) // no runs
	if _, err := FromBytes("empty.docx", "", data); err == nil {
		t.Fatal("expected error for docx with no extractable text")
	}
}

func TestFromPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(p, onePxPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := FromPath(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindImage || a.Name != "pic.png" {
		t.Fatalf("unexpected: %+v", a)
	}
}

func TestFromPathDirectory(t *testing.T) {
	if _, err := FromPath(t.TempDir()); err == nil {
		t.Fatal("expected error attaching a directory")
	}
}

func TestLabel(t *testing.T) {
	img := Attachment{Kind: KindImage, Name: "a.png", Data: make([]byte, 2048)}
	if !strings.Contains(img.Label(), "image a.png") {
		t.Errorf("image label = %q", img.Label())
	}
	doc := Attachment{Kind: KindDocument, Name: "b.pdf", Data: make([]byte, 1024)}
	if !strings.Contains(doc.Label(), "PDF b.pdf") {
		t.Errorf("doc label = %q", doc.Label())
	}
}
