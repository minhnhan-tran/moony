package attach

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// extractOffice pulls readable text out of an Office Open XML file (docx, xlsx,
// pptx), which are ZIP archives of XML. It reads the parts that hold body text
// and collects the character data inside <…:t> elements (the run-text element
// shared by Word, Excel shared strings, and PowerPoint). No external deps.
func extractOffice(ext string, data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not a valid Office file: %w", err)
	}

	var parts []string
	switch ext {
	case ".docx":
		parts = []string{"word/document.xml"}
	case ".pptx":
		// One part per slide, in slide order.
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
				parts = append(parts, f.Name)
			}
		}
		sort.Strings(parts)
	case ".xlsx":
		// Shared strings hold most cell text; fall back to inline sheet text.
		parts = []string{"xl/sharedStrings.xml"}
	}

	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}

	var b strings.Builder
	for _, name := range parts {
		f := byName[name]
		if f == nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		text := xmlRunText(rc)
		rc.Close()
		if strings.TrimSpace(text) != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
		}
	}

	// xlsx fallback: if there were no shared strings, scan the worksheets for
	// inline text instead.
	if ext == ".xlsx" && strings.TrimSpace(b.String()) == "" {
		var sheets []string
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
				sheets = append(sheets, f.Name)
			}
		}
		sort.Strings(sheets)
		for _, name := range sheets {
			rc, err := byName[name].Open()
			if err != nil {
				continue
			}
			b.WriteString(xmlRunText(rc))
			rc.Close()
		}
	}

	return strings.TrimSpace(b.String()), nil
}

// xmlRunText streams an OOXML part and returns the text inside <…:t> run
// elements, inserting a newline at paragraph (<…:p>) and shared-string-item
// (<si>) boundaries so the result stays readable.
func xmlRunText(r io.Reader) string {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	capture := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				capture++
			}
		case xml.CharData:
			if capture > 0 {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if capture > 0 {
					capture--
				}
			case "p", "si", "br":
				b.WriteString("\n")
			}
		}
	}
	return collapseBlankLines(b.String())
}

// collapseBlankLines trims trailing spaces and squeezes runs of blank lines so
// extracted text isn't littered with empty paragraphs.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
